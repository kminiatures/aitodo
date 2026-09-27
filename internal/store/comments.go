package store

import (
	"errors"
	"fmt"
	"strings"
)

// Comment はタスクへのコメント（追記型のログ）。作業の途中経過・結果・レビューなどを残す。
type Comment struct {
	ID        int64  `json:"id"`
	TaskID    int64  `json:"task_id"`
	Author    string `json:"author"`
	Body      string `json:"body"`
	CreatedAt string `json:"created_at"`
}

// TaskDetail は 1 タスクの詳細（直下のサブタスクとコメントを含む）。
type TaskDetail struct {
	Task
	Subtasks []Task    `json:"subtasks"`
	Comments []Comment `json:"comments"`
}

func (s *Store) AddComment(taskID int64, author, body string) (*Comment, error) {
	body = strings.TrimSpace(body)
	if body == "" {
		return nil, errors.New("comment body is required")
	}
	t, err := s.GetTask(taskID)
	if err != nil {
		return nil, err
	}
	ts := now()
	var c Comment
	err = s.DB.QueryRow(`INSERT INTO comments(task_id, author, body, created_at) VALUES(?,?,?,?)
		RETURNING id, task_id, author, body, created_at`, taskID, strings.TrimSpace(author), body, ts).
		Scan(&c.ID, &c.TaskID, &c.Author, &c.Body, &c.CreatedAt)
	if err != nil {
		return nil, err
	}
	s.DB.Exec(`UPDATE tasks SET updated_at = ? WHERE id = ?`, ts, taskID)
	s.touchSession(s.DB, t.SessionID)
	return &c, nil
}

// ListComments は古い順に返す。
func (s *Store) ListComments(taskID int64) ([]Comment, error) {
	rows, err := s.DB.Query(`SELECT id, task_id, author, body, created_at FROM comments WHERE task_id = ? ORDER BY id`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Comment{}
	for rows.Next() {
		var c Comment
		if err := rows.Scan(&c.ID, &c.TaskID, &c.Author, &c.Body, &c.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *Store) DeleteComment(id int64) error {
	res, err := s.DB.Exec(`DELETE FROM comments WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("comment %d not found", id)
	}
	return nil
}

func (s *Store) GetTaskDetail(id int64) (*TaskDetail, error) {
	t, err := s.GetTask(id)
	if err != nil {
		return nil, err
	}
	subs, err := s.Children(id)
	if err != nil {
		return nil, err
	}
	cs, err := s.ListComments(id)
	if err != nil {
		return nil, err
	}
	return &TaskDetail{Task: *t, Subtasks: subs, Comments: cs}, nil
}
