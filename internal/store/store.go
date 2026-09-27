// Package store は aitodo の SQLite 永続化層。CLI / MCP / TUI から共有される。
package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// タスクのステータス。
const (
	StatusTodo    = "todo"
	StatusDoing   = "doing"
	StatusDone    = "done"
	StatusSkipped = "skipped"
	StatusBlocked = "blocked"
)

// ValidStatuses は受け付けるタスクステータスの一覧。
var ValidStatuses = []string{StatusTodo, StatusDoing, StatusDone, StatusSkipped, StatusBlocked}

// セッションのステータス。
const (
	SessionActive   = "active"
	SessionArchived = "archived"
)

var ErrNotFound = errors.New("not found")

type Session struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Workdir     string `json:"workdir"`
	Status      string `json:"status"`
	CreatedAt   string `json:"created_at"`
	UpdatedAt   string `json:"updated_at"`
	Total       int    `json:"total"`
	Done        int    `json:"done"`
	Doing       int    `json:"doing"`
}

type Task struct {
	ID        int64   `json:"id"`
	SessionID int64   `json:"session_id"`
	ParentID  *int64  `json:"parent_id"`
	Title     string  `json:"title"`
	Body      string  `json:"body"`
	Status    string  `json:"status"`
	Note      string  `json:"note"`
	Position  int     `json:"position"`
	CreatedAt string  `json:"created_at"`
	UpdatedAt string  `json:"updated_at"`
	DoneAt    *string `json:"done_at"`
	// 以下は算出値
	Depth         int `json:"depth"`          // 木の深さ（トップレベル = 0）。ListTasks でのみ設定
	SubtasksTotal int `json:"subtasks_total"` // 直下のサブタスク数
	SubtasksDone  int `json:"subtasks_done"`  // 直下のサブタスクのうち done/skipped
	CommentCount  int `json:"comment_count"`
}

// NewTask はバルク追加用の入力。Subtasks で入れ子にできる。
type NewTask struct {
	Title    string    `json:"title"`
	Body     string    `json:"body"`
	Subtasks []NewTask `json:"subtasks,omitempty"`
}

type Store struct {
	DB   *sql.DB
	Path string
}

// DefaultPath は $AITODO_DB、なければ ~/.aitodo/aitodo.db。
func DefaultPath() string {
	if p := os.Getenv("AITODO_DB"); p != "" {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "aitodo.db"
	}
	return filepath.Join(home, ".aitodo", "aitodo.db")
}

const schemaVersion = 2

const schemaV1 = `
CREATE TABLE sessions (
	id          INTEGER PRIMARY KEY AUTOINCREMENT,
	name        TEXT NOT NULL UNIQUE,
	description TEXT NOT NULL DEFAULT '',
	workdir     TEXT NOT NULL DEFAULT '',
	status      TEXT NOT NULL DEFAULT 'active',
	created_at  TEXT NOT NULL,
	updated_at  TEXT NOT NULL
);
CREATE TABLE tasks (
	id          INTEGER PRIMARY KEY AUTOINCREMENT,
	session_id  INTEGER NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
	title       TEXT NOT NULL,
	body        TEXT NOT NULL DEFAULT '',
	status      TEXT NOT NULL DEFAULT 'todo',
	note        TEXT NOT NULL DEFAULT '',
	position    INTEGER NOT NULL DEFAULT 0,
	created_at  TEXT NOT NULL,
	updated_at  TEXT NOT NULL,
	done_at     TEXT
);
CREATE INDEX idx_tasks_session ON tasks(session_id, position);
`

// v2: サブタスク（parent_id）とコメント
const schemaV2 = `
ALTER TABLE tasks ADD COLUMN parent_id INTEGER REFERENCES tasks(id) ON DELETE CASCADE;
CREATE INDEX idx_tasks_parent ON tasks(parent_id);
CREATE TABLE comments (
	id         INTEGER PRIMARY KEY AUTOINCREMENT,
	task_id    INTEGER NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
	author     TEXT NOT NULL DEFAULT '',
	body       TEXT NOT NULL,
	created_at TEXT NOT NULL
);
CREATE INDEX idx_comments_task ON comments(task_id, id);
`

func Open(path string) (*Store, error) {
	if path == "" {
		path = DefaultPath()
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return nil, err
	}
	dsn := "file:" + (&url.URL{Path: abs}).EscapedPath() +
		"?_txlock=immediate&_pragma=busy_timeout(10000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)&_pragma=synchronous(NORMAL)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	s := &Store{DB: db, Path: abs}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return s, nil
}

func (s *Store) Close() error { return s.DB.Close() }

func (s *Store) migrate() error {
	var v int
	if err := s.DB.QueryRow("PRAGMA user_version").Scan(&v); err != nil {
		return err
	}
	if v >= schemaVersion {
		return nil
	}
	tx, err := s.DB.Begin() // immediate: 同時に開いた他プロセスとはここで直列化される
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// 待っている間に他のプロセスが移行を済ませているかもしれないので読み直す
	if err := tx.QueryRow("PRAGMA user_version").Scan(&v); err != nil {
		return err
	}
	if v >= schemaVersion {
		return nil
	}
	if v < 1 {
		if _, err := tx.Exec(schemaV1); err != nil {
			return err
		}
	}
	if v < 2 {
		if _, err := tx.Exec(schemaV2); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(fmt.Sprintf("PRAGMA user_version = %d", schemaVersion)); err != nil {
		return err
	}
	return tx.Commit()
}

func now() string { return time.Now().UTC().Format(time.RFC3339) }

// NormalizeDir は作業フォルダを絶対パス化し、シンボリックリンクを解決する。
func NormalizeDir(dir string) (string, error) {
	if dir == "" {
		return "", nil
	}
	if strings.HasPrefix(dir, "~/") || dir == "~" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		dir = filepath.Join(home, strings.TrimPrefix(dir, "~"))
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	if real, err := filepath.EvalSymlinks(abs); err == nil {
		abs = real
	}
	return filepath.Clean(abs), nil
}

// ---- sessions ----

const sessionCols = `s.id, s.name, s.description, s.workdir, s.status, s.created_at, s.updated_at,
	(SELECT COUNT(*) FROM tasks t WHERE t.session_id = s.id),
	(SELECT COUNT(*) FROM tasks t WHERE t.session_id = s.id AND t.status IN ('done','skipped')),
	(SELECT COUNT(*) FROM tasks t WHERE t.session_id = s.id AND t.status = 'doing')`

func scanSession(sc interface{ Scan(...any) error }) (*Session, error) {
	var x Session
	err := sc.Scan(&x.ID, &x.Name, &x.Description, &x.Workdir, &x.Status, &x.CreatedAt, &x.UpdatedAt, &x.Total, &x.Done, &x.Doing)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &x, err
}

func (s *Store) CreateSession(name, desc, workdir string) (*Session, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, errors.New("session name is required")
	}
	wd, err := NormalizeDir(workdir)
	if err != nil {
		return nil, err
	}
	t := now()
	res, err := s.DB.Exec(`INSERT INTO sessions(name, description, workdir, status, created_at, updated_at) VALUES(?,?,?,?,?,?)`,
		name, desc, wd, SessionActive, t, t)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return nil, fmt.Errorf("session %q already exists", name)
		}
		return nil, err
	}
	id, _ := res.LastInsertId()
	return s.GetSession(id)
}

func (s *Store) GetSession(id int64) (*Session, error) {
	return scanSession(s.DB.QueryRow(`SELECT `+sessionCols+` FROM sessions s WHERE s.id = ?`, id))
}

func (s *Store) ListSessions(includeArchived bool) ([]Session, error) {
	q := `SELECT ` + sessionCols + ` FROM sessions s`
	if !includeArchived {
		q += ` WHERE s.status = 'active'`
	}
	q += ` ORDER BY s.status = 'archived', s.id`
	rows, err := s.DB.Query(q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Session{}
	for rows.Next() {
		x, err := scanSession(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *x)
	}
	return out, rows.Err()
}

// FindSession は ID（数値）またはセッション名で検索する。
func (s *Store) FindSession(ref string) (*Session, error) {
	ref = strings.TrimSpace(ref)
	if id, err := strconv.ParseInt(strings.TrimPrefix(ref, "#"), 10, 64); err == nil {
		if x, err := s.GetSession(id); err == nil {
			return x, nil
		}
	}
	x, err := scanSession(s.DB.QueryRow(`SELECT `+sessionCols+` FROM sessions s WHERE s.name = ?`, ref))
	if errors.Is(err, ErrNotFound) {
		return nil, fmt.Errorf("session %q not found", ref)
	}
	return x, err
}

// ResolveByDir は dir を含む作業フォルダを持つ active セッションのうち、最も深い（最長一致）ものを返す。
func (s *Store) ResolveByDir(dir string) (*Session, error) {
	d, err := NormalizeDir(dir)
	if err != nil {
		return nil, err
	}
	sessions, err := s.ListSessions(false)
	if err != nil {
		return nil, err
	}
	var best *Session
	for i := range sessions {
		wd := sessions[i].Workdir
		if wd == "" {
			continue
		}
		if d == wd || strings.HasPrefix(d, strings.TrimSuffix(wd, string(filepath.Separator))+string(filepath.Separator)) {
			if best == nil || len(wd) > len(best.Workdir) {
				best = &sessions[i]
			}
		}
	}
	if best == nil {
		return nil, fmt.Errorf("no session has a workdir containing %s (use --session or `aitodo session add NAME --dir .`)", d)
	}
	return best, nil
}

type SessionPatch struct {
	Name        *string
	Description *string
	Workdir     *string
	Status      *string
}

func (s *Store) UpdateSession(id int64, p SessionPatch) (*Session, error) {
	sets := []string{}
	args := []any{}
	if p.Name != nil {
		n := strings.TrimSpace(*p.Name)
		if n == "" {
			return nil, errors.New("session name cannot be empty")
		}
		sets, args = append(sets, "name = ?"), append(args, n)
	}
	if p.Description != nil {
		sets, args = append(sets, "description = ?"), append(args, *p.Description)
	}
	if p.Workdir != nil {
		wd, err := NormalizeDir(*p.Workdir)
		if err != nil {
			return nil, err
		}
		sets, args = append(sets, "workdir = ?"), append(args, wd)
	}
	if p.Status != nil {
		if *p.Status != SessionActive && *p.Status != SessionArchived {
			return nil, fmt.Errorf("invalid session status %q (active|archived)", *p.Status)
		}
		sets, args = append(sets, "status = ?"), append(args, *p.Status)
	}
	if len(sets) > 0 {
		sets, args = append(sets, "updated_at = ?"), append(args, now())
		args = append(args, id)
		res, err := s.DB.Exec(`UPDATE sessions SET `+strings.Join(sets, ", ")+` WHERE id = ?`, args...)
		if err != nil {
			if strings.Contains(err.Error(), "UNIQUE") {
				return nil, fmt.Errorf("session %q already exists", *p.Name)
			}
			return nil, err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return nil, ErrNotFound
		}
	}
	return s.GetSession(id)
}

func (s *Store) DeleteSession(id int64) error {
	res, err := s.DB.Exec(`DELETE FROM sessions WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) touchSession(ex interface {
	Exec(string, ...any) (sql.Result, error)
}, id int64) {
	ex.Exec(`UPDATE sessions SET updated_at = ? WHERE id = ?`, now(), id)
}

// UnmarshalJSON は "タイトル" だけの文字列表記も受け付ける（入れ子の subtasks 内でも）。
func (n *NewTask) UnmarshalJSON(b []byte) error {
	var s string
	if json.Unmarshal(b, &s) == nil {
		*n = NewTask{Title: s}
		return nil
	}
	type plain NewTask
	var p plain
	if err := json.Unmarshal(b, &p); err != nil {
		return err
	}
	*n = NewTask(p)
	return nil
}
