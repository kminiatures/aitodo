package store

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// ---- tasks ----

// taskCols は FROM tasks（別名なし）で使う。サブタスク数・コメント数は相関サブクエリで算出する。
const taskCols = `id, session_id, parent_id, title, body, status, note, position, created_at, updated_at, done_at,
	(SELECT COUNT(*) FROM tasks c WHERE c.parent_id = tasks.id),
	(SELECT COUNT(*) FROM tasks c WHERE c.parent_id = tasks.id AND c.status IN ('done','skipped')),
	(SELECT COUNT(*) FROM comments m WHERE m.task_id = tasks.id)`

func scanTask(sc interface{ Scan(...any) error }) (*Task, error) {
	var x Task
	var doneAt sql.NullString
	var parent sql.NullInt64
	err := sc.Scan(&x.ID, &x.SessionID, &parent, &x.Title, &x.Body, &x.Status, &x.Note, &x.Position, &x.CreatedAt, &x.UpdatedAt, &doneAt,
		&x.SubtasksTotal, &x.SubtasksDone, &x.CommentCount)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if doneAt.Valid {
		x.DoneAt = &doneAt.String
	}
	if parent.Valid {
		x.ParentID = &parent.Int64
	}
	return &x, err
}

func finished(status string) bool { return status == StatusDone || status == StatusSkipped }

// AddTasks はセッションのトップレベルにタスクを追加する（Subtasks は再帰的に子として追加）。
func (s *Store) AddTasks(sessionID int64, items []NewTask) ([]Task, error) {
	return s.addTasks(sessionID, nil, items)
}

// AddSubtasks は parentID の子としてタスクを追加する。セッションは親と同じになる。
func (s *Store) AddSubtasks(parentID int64, items []NewTask) ([]Task, error) {
	p, err := s.GetTask(parentID)
	if err != nil {
		return nil, err
	}
	return s.addTasks(p.SessionID, &parentID, items)
}

func (s *Store) addTasks(sessionID int64, parentID *int64, items []NewTask) ([]Task, error) {
	if len(items) == 0 {
		return nil, errors.New("no tasks given")
	}
	tx, err := s.DB.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var exists int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM sessions WHERE id = ?`, sessionID).Scan(&exists); err != nil {
		return nil, err
	}
	if exists == 0 {
		return nil, fmt.Errorf("session %d not found", sessionID)
	}
	var pos int
	if err := tx.QueryRow(`SELECT COALESCE(MAX(position), 0) FROM tasks WHERE session_id = ?`, sessionID).Scan(&pos); err != nil {
		return nil, err
	}
	t := now()
	ids := []int64{}
	var insert func(parent *int64, items []NewTask) error
	insert = func(parent *int64, items []NewTask) error {
		for _, it := range items {
			title := strings.TrimSpace(it.Title)
			if title == "" {
				return errors.New("task title is required")
			}
			pos++
			res, err := tx.Exec(`INSERT INTO tasks(session_id, parent_id, title, body, status, position, created_at, updated_at) VALUES(?,?,?,?,?,?,?,?)`,
				sessionID, parent, title, it.Body, StatusTodo, pos, t, t)
			if err != nil {
				return err
			}
			id, _ := res.LastInsertId()
			ids = append(ids, id)
			if len(it.Subtasks) > 0 {
				if err := insert(&id, it.Subtasks); err != nil {
					return err
				}
			}
		}
		return nil
	}
	if err := insert(parentID, items); err != nil {
		return nil, err
	}
	s.touchSession(tx, sessionID)
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	out := make([]Task, 0, len(ids))
	for _, id := range ids {
		x, err := s.GetTask(id)
		if err != nil {
			return nil, err
		}
		out = append(out, *x)
	}
	return out, nil
}

func (s *Store) AddTask(sessionID int64, title, body string) (*Task, error) {
	ts, err := s.AddTasks(sessionID, []NewTask{{Title: title, Body: body}})
	if err != nil {
		return nil, err
	}
	return &ts[0], nil
}

func (s *Store) AddSubtask(parentID int64, title, body string) (*Task, error) {
	ts, err := s.AddSubtasks(parentID, []NewTask{{Title: title, Body: body}})
	if err != nil {
		return nil, err
	}
	return &ts[0], nil
}

func (s *Store) GetTask(id int64) (*Task, error) {
	x, err := scanTask(s.DB.QueryRow(`SELECT `+taskCols+` FROM tasks WHERE id = ?`, id))
	if errors.Is(err, ErrNotFound) {
		return nil, fmt.Errorf("task %d not found", id)
	}
	return x, err
}

// Children は直下のサブタスクを並び順で返す。
func (s *Store) Children(id int64) ([]Task, error) {
	return s.queryTasks(`SELECT `+taskCols+` FROM tasks WHERE parent_id = ? ORDER BY position, id`, id)
}

type querier interface {
	Query(string, ...any) (*sql.Rows, error)
}

func (s *Store) queryTasks(q string, args ...any) ([]Task, error) {
	return queryTasksWith(s.DB, q, args...)
}

func queryTasksWith(db querier, q string, args ...any) ([]Task, error) {
	rows, err := db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Task{}
	for rows.Next() {
		x, err := scanTask(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *x)
	}
	return out, rows.Err()
}

// treeOrder はフラットなタスク列を「親 → 子（深さ優先）」の順に並べ、Depth を設定する。
// 入力は position 順であること。
func treeOrder(all []Task) []Task {
	byParent := map[int64][]int{}
	ids := map[int64]bool{}
	for _, t := range all {
		ids[t.ID] = true
	}
	var roots []int
	for i, t := range all {
		if t.ParentID != nil && ids[*t.ParentID] {
			byParent[*t.ParentID] = append(byParent[*t.ParentID], i)
		} else {
			roots = append(roots, i)
		}
	}
	out := make([]Task, 0, len(all))
	var walk func(idx []int, depth int)
	walk = func(idx []int, depth int) {
		for _, i := range idx {
			t := all[i]
			t.Depth = depth
			out = append(out, t)
			walk(byParent[t.ID], depth+1)
		}
	}
	walk(roots, 0)
	return out
}

// ListTasks はセッションのタスクを木の順（親の直後に子）で返す。statuses が空なら全件。
// フィルタは木の順に並べた後で適用するため、Depth は元の木構造のまま。
func (s *Store) ListTasks(sessionID int64, statuses []string) ([]Task, error) {
	for _, st := range statuses {
		if err := ValidateStatus(st); err != nil {
			return nil, err
		}
	}
	all, err := s.listTree(s.DB, sessionID)
	if err != nil {
		return nil, err
	}
	if len(statuses) == 0 {
		return all, nil
	}
	out := []Task{}
	for _, t := range all {
		for _, st := range statuses {
			if t.Status == st {
				out = append(out, t)
				break
			}
		}
	}
	return out, nil
}

func (s *Store) listTree(q querier, sessionID int64) ([]Task, error) {
	all, err := queryTasksWith(q, `SELECT `+taskCols+` FROM tasks WHERE session_id = ? ORDER BY position, id`, sessionID)
	if err != nil {
		return nil, err
	}
	return treeOrder(all), nil
}

func ValidateStatus(st string) error {
	for _, v := range ValidStatuses {
		if st == v {
			return nil
		}
	}
	return fmt.Errorf("invalid status %q (valid: %s)", st, strings.Join(ValidStatuses, ", "))
}

// SetStatus はタスクのステータスを変更する。note が nil でなければメモも更新する。
func (s *Store) SetStatus(id int64, status string, note *string) (*Task, error) {
	if err := ValidateStatus(status); err != nil {
		return nil, err
	}
	cur, err := s.GetTask(id)
	if err != nil {
		return nil, err
	}
	t := now()
	var doneAt any
	if finished(status) {
		if cur.DoneAt != nil && finished(cur.Status) {
			doneAt = *cur.DoneAt
		} else {
			doneAt = t
		}
	}
	n := cur.Note
	if note != nil {
		n = *note
	}
	if _, err := s.DB.Exec(`UPDATE tasks SET status = ?, note = ?, done_at = ?, updated_at = ? WHERE id = ?`,
		status, n, doneAt, t, id); err != nil {
		return nil, err
	}
	s.touchSession(s.DB, cur.SessionID)
	return s.GetTask(id)
}

type TaskPatch struct {
	Title *string
	Body  *string
	Note  *string
	// ParentID を変えると親を付け替える。0 を指定するとトップレベルに戻す。
	ParentID *int64
}

func (s *Store) UpdateTask(id int64, p TaskPatch) (*Task, error) {
	cur, err := s.GetTask(id)
	if err != nil {
		return nil, err
	}
	sets, args := []string{}, []any{}
	if p.Title != nil {
		tt := strings.TrimSpace(*p.Title)
		if tt == "" {
			return nil, errors.New("task title cannot be empty")
		}
		sets, args = append(sets, "title = ?"), append(args, tt)
	}
	if p.Body != nil {
		sets, args = append(sets, "body = ?"), append(args, *p.Body)
	}
	if p.Note != nil {
		sets, args = append(sets, "note = ?"), append(args, *p.Note)
	}
	if p.ParentID != nil {
		var parent any
		if *p.ParentID != 0 {
			if err := s.checkParent(cur, *p.ParentID); err != nil {
				return nil, err
			}
			parent = *p.ParentID
		}
		var pos int
		if err := s.DB.QueryRow(`SELECT COALESCE(MAX(position), 0) + 1 FROM tasks WHERE session_id = ?`, cur.SessionID).Scan(&pos); err != nil {
			return nil, err
		}
		sets, args = append(sets, "parent_id = ?", "position = ?"), append(args, parent, pos)
	}
	if len(sets) > 0 {
		sets, args = append(sets, "updated_at = ?"), append(args, now())
		args = append(args, id)
		if _, err := s.DB.Exec(`UPDATE tasks SET `+strings.Join(sets, ", ")+` WHERE id = ?`, args...); err != nil {
			return nil, err
		}
		s.touchSession(s.DB, cur.SessionID)
	}
	return s.GetTask(id)
}

// checkParent は task を parentID の子にしてよいか（同一セッション・循環なし）を確かめる。
func (s *Store) checkParent(task *Task, parentID int64) error {
	p, err := s.GetTask(parentID)
	if err != nil {
		return err
	}
	if p.SessionID != task.SessionID {
		return fmt.Errorf("parent #%d belongs to another session", parentID)
	}
	for cur := p; ; {
		if cur.ID == task.ID {
			return fmt.Errorf("cannot move #%d under its own descendant #%d", task.ID, parentID)
		}
		if cur.ParentID == nil {
			return nil
		}
		if cur, err = s.GetTask(*cur.ParentID); err != nil {
			return err
		}
	}
}

// DeleteTask はタスクを削除する。サブタスクとコメントも一緒に消える。
func (s *Store) DeleteTask(id int64) error {
	cur, err := s.GetTask(id)
	if err != nil {
		return err
	}
	if _, err := s.DB.Exec(`DELETE FROM tasks WHERE id = ?`, id); err != nil {
		return err
	}
	s.touchSession(s.DB, cur.SessionID)
	return nil
}

func (s *Store) siblings(t *Task) ([]Task, error) {
	return s.queryTasks(`SELECT `+taskCols+` FROM tasks WHERE session_id = ? AND parent_id IS ? ORDER BY position, id`,
		t.SessionID, t.ParentID)
}

// MoveTask はタスクを兄弟（同じ親を持つタスク）の中の index（0 始まり）へ移動する。
func (s *Store) MoveTask(id int64, index int) (*Task, error) {
	cur, err := s.GetTask(id)
	if err != nil {
		return nil, err
	}
	sib, err := s.siblings(cur)
	if err != nil {
		return nil, err
	}
	order := make([]int64, 0, len(sib))
	slots := make([]int, 0, len(sib)) // 兄弟が占めている position をそのまま再利用する
	for _, t := range sib {
		slots = append(slots, t.Position)
		if t.ID != id {
			order = append(order, t.ID)
		}
	}
	index = max(0, min(index, len(order)))
	order = append(order[:index], append([]int64{id}, order[index:]...)...)
	// position が重複していると並びが決まらないので、兄弟全体で昇順かつ一意な値を振り直す
	for i := 1; i < len(slots); i++ {
		if slots[i] <= slots[i-1] {
			slots[i] = slots[i-1] + 1
		}
	}
	tx, err := s.DB.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	for i, tid := range order {
		if _, err := tx.Exec(`UPDATE tasks SET position = ? WHERE id = ?`, slots[i], tid); err != nil {
			return nil, err
		}
	}
	s.touchSession(tx, cur.SessionID)
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return s.GetTask(id)
}

// TaskIndex は兄弟の中での 0 始まりの並び位置を返す。
func (s *Store) TaskIndex(id int64) (int, error) {
	cur, err := s.GetTask(id)
	if err != nil {
		return 0, err
	}
	sib, err := s.siblings(cur)
	if err != nil {
		return 0, err
	}
	for i, t := range sib {
		if t.ID == id {
			return i, nil
		}
	}
	return 0, nil
}

// actionable は「未完了のサブタスクを持たない」タスクか。子を先に片付け、全部終わったら親に戻る。
func actionable(t *Task) bool { return t.SubtasksDone == t.SubtasksTotal }

// NextTask は次に取り組むべきタスクを木の順で返す。対象は未完了のサブタスクを持たないタスクだけ。
//   - fresh=false: 進行中(doing)があればそれを返す（中断からの再開用）。なければ最初の todo
//   - claim=true : todo を doing に変える。一覧の取得と更新を 1 つの immediate トランザクションで行うので、
//     複数のエージェントが同時に呼んでも同じタスクを二重に取らない
//   - fresh=true : doing を無視して常に新しい todo を返す（複数エージェントの並列実行用）
//
// 残りが無ければ nil。
func (s *Store) NextTask(sessionID int64, claim, fresh bool) (*Task, error) {
	var q querier = s.DB
	var tx *sql.Tx
	if claim {
		var err error
		if tx, err = s.DB.Begin(); err != nil { // _txlock=immediate により書き込みロックを先に取る
			return nil, err
		}
		defer tx.Rollback()
		q = tx
	}
	all, err := s.listTree(q, sessionID)
	if err != nil {
		return nil, err
	}
	if !fresh {
		for i := range all {
			if all[i].Status == StatusDoing && actionable(&all[i]) {
				return &all[i], nil
			}
		}
	}
	var cand *Task
	for i := range all {
		if all[i].Status == StatusTodo && actionable(&all[i]) {
			cand = &all[i]
			break
		}
	}
	if cand == nil || !claim {
		return cand, nil
	}
	t := now()
	if _, err := tx.Exec(`UPDATE tasks SET status = 'doing', updated_at = ? WHERE id = ?`, t, cand.ID); err != nil {
		return nil, err
	}
	s.touchSession(tx, sessionID)
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return s.GetTask(cand.ID)
}
