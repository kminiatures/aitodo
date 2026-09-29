package store

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func open(t *testing.T) *Store {
	t.Helper()
	st, err := Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func TestResolveByDirLongestPrefix(t *testing.T) {
	st := open(t)
	root := t.TempDir()
	sub := filepath.Join(root, "a", "b")
	os.MkdirAll(sub, 0o755)
	os.MkdirAll(root+"-other", 0o755)
	st.CreateSession("root", "", root)
	st.CreateSession("a", "", filepath.Join(root, "a"))
	s, err := st.ResolveByDir(sub)
	if err != nil || s.Name != "a" {
		t.Fatalf("got %v %v, want a", s, err)
	}
	s, err = st.ResolveByDir(root)
	if err != nil || s.Name != "root" {
		t.Fatalf("got %v %v, want root", s, err)
	}
	if _, err := st.ResolveByDir(root + "-other"); err == nil {
		t.Fatal("sibling with shared prefix must not match")
	}
}

func TestNextClaim(t *testing.T) {
	st := open(t)
	s, _ := st.CreateSession("s", "", "")
	st.AddTasks(s.ID, []NewTask{{Title: "1"}, {Title: "2"}})
	a, _ := st.NextTask(s.ID, true, false)
	b, _ := st.NextTask(s.ID, true, false)
	if a.ID != b.ID || a.Status != StatusDoing {
		t.Fatalf("doing task should be returned again: %+v %+v", a, b)
	}
	st.SetStatus(a.ID, StatusDone, nil)
	c, _ := st.NextTask(s.ID, false, false)
	if c.Title != "2" || c.Status != StatusTodo {
		t.Fatalf("peek should not claim: %+v", c)
	}
	st.SetStatus(c.ID, StatusSkipped, nil)
	if d, err := st.NextTask(s.ID, true, true); d != nil || err != nil {
		t.Fatalf("want nil, got %+v %v", d, err)
	}
	s, _ = st.GetSession(s.ID)
	if s.Done != 2 || s.Total != 2 {
		t.Fatalf("counts %d/%d", s.Done, s.Total)
	}
}

func TestMoveAndCascade(t *testing.T) {
	st := open(t)
	s, _ := st.CreateSession("s", "", "")
	ts, _ := st.AddTasks(s.ID, []NewTask{{Title: "a"}, {Title: "b"}, {Title: "c"}})
	st.MoveTask(ts[2].ID, 0)
	l, _ := st.ListTasks(s.ID, nil)
	if l[0].Title != "c" || l[1].Title != "a" || l[2].Title != "b" {
		t.Fatalf("order %v %v %v", l[0].Title, l[1].Title, l[2].Title)
	}
	st.DeleteSession(s.ID)
	if _, err := st.GetTask(ts[0].ID); err == nil {
		t.Fatal("tasks should be deleted with session")
	}
}

func TestFreshClaimDistinct(t *testing.T) {
	st := open(t)
	s, _ := st.CreateSession("s", "", "")
	st.AddTasks(s.ID, []NewTask{{Title: "1"}, {Title: "2"}})
	a, _ := st.NextTask(s.ID, true, true)
	b, _ := st.NextTask(s.ID, true, true)
	if a == nil || b == nil || a.ID == b.ID {
		t.Fatalf("fresh claims must differ: %+v %+v", a, b)
	}
}

func TestSubtasksTreeAndNext(t *testing.T) {
	st := open(t)
	s, _ := st.CreateSession("s", "", "")
	ts, err := st.AddTasks(s.ID, []NewTask{
		{Title: "parent", Subtasks: []NewTask{{Title: "c1"}, {Title: "c2", Subtasks: []NewTask{{Title: "g1"}}}}},
		{Title: "last"},
	})
	if err != nil || len(ts) != 5 {
		t.Fatalf("%v %d", err, len(ts))
	}
	// 後からトップレベルにタスクを足しても、子は親の直後に並ぶ
	extra, _ := st.AddSubtask(ts[0].ID, "c3", "")
	l, _ := st.ListTasks(s.ID, nil)
	var got []string
	for _, x := range l {
		got = append(got, strings.Repeat(".", x.Depth)+x.Title)
	}
	if strings.Join(got, " ") != "parent .c1 .c2 ..g1 .c3 last" {
		t.Fatalf("tree order: %v", got)
	}
	if l[0].SubtasksTotal != 3 {
		t.Fatalf("parent subtasks_total = %d", l[0].SubtasksTotal)
	}
	// next は未完了の子を持たないタスクだけを返す: c1 → g1 → c2 → c3 → parent → last
	want := []string{"c1", "g1", "c2", "c3", "parent", "last"}
	for _, w := range want {
		n, err := st.NextTask(s.ID, true, false)
		if err != nil || n == nil || n.Title != w {
			t.Fatalf("next = %+v %v, want %s", n, err, w)
		}
		st.SetStatus(n.ID, StatusDone, nil)
	}
	// 兄弟内での並べ替え
	st.MoveTask(extra.ID, 0)
	kids, _ := st.Children(ts[0].ID)
	if kids[0].ID != extra.ID {
		t.Fatalf("move within siblings failed: %v", kids[0].Title)
	}
	// 循環する付け替えは拒否、トップレベルへの移動は可能
	gid := ts[3].ID
	if _, err := st.UpdateTask(ts[0].ID, TaskPatch{ParentID: &gid}); err == nil {
		t.Fatal("cycle must be rejected")
	}
	zero := int64(0)
	if x, err := st.UpdateTask(gid, TaskPatch{ParentID: &zero}); err != nil || x.ParentID != nil {
		t.Fatalf("reparent to top: %+v %v", x, err)
	}
	// 親を消すと子孫も消える
	st.DeleteTask(ts[0].ID)
	l, _ = st.ListTasks(s.ID, nil)
	if len(l) != 2 { // last と g1（トップに移動済み）
		t.Fatalf("remaining %d", len(l))
	}
}

func TestComments(t *testing.T) {
	st := open(t)
	s, _ := st.CreateSession("s", "", "")
	tk, _ := st.AddTask(s.ID, "x", "")
	if _, err := st.AddComment(tk.ID, "ai", "  "); err == nil {
		t.Fatal("empty comment must fail")
	}
	st.AddComment(tk.ID, "ai", "first")
	c2, _ := st.AddComment(tk.ID, "koba", "second\nline")
	d, _ := st.GetTaskDetail(tk.ID)
	if d.CommentCount != 2 || len(d.Comments) != 2 || d.Comments[1].Author != "koba" {
		t.Fatalf("%+v", d)
	}
	st.DeleteComment(c2.ID)
	st.DeleteTask(tk.ID)
	var n int
	st.DB.QueryRow(`SELECT COUNT(*) FROM comments`).Scan(&n)
	if n != 0 {
		t.Fatal("comments should cascade")
	}
}

func TestMigrateFromV1(t *testing.T) {
	path := filepath.Join(t.TempDir(), "v1.db")
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(schemaV1 + `PRAGMA user_version = 1;
		INSERT INTO sessions(name, created_at, updated_at) VALUES('old', 'x', 'x');
		INSERT INTO tasks(session_id, title, created_at, updated_at) VALUES(1, 'legacy', 'x', 'x');`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	l, err := st.ListTasks(1, nil)
	if err != nil || len(l) != 1 || l[0].Title != "legacy" || l[0].ParentID != nil {
		t.Fatalf("%+v %v", l, err)
	}
	if _, err := st.AddComment(l[0].ID, "ai", "ok"); err != nil {
		t.Fatal(err)
	}
}

// 着手日時は初めて doing になったときに記録され、完了後も保持、todo に戻すと消える。
func TestStartedAt(t *testing.T) {
	st := open(t)
	s, _ := st.CreateSession("s", "", "")
	st.AddTasks(s.ID, []NewTask{{Title: "1"}, {Title: "2"}})
	a, _ := st.NextTask(s.ID, true, false)
	if a.StartedAt == nil || a.DoneAt != nil {
		t.Fatalf("claim should set started_at: %+v", a)
	}
	started := *a.StartedAt
	st.SetStatus(a.ID, StatusBlocked, nil)
	a, _ = st.SetStatus(a.ID, StatusDoing, nil)
	if a.StartedAt == nil || *a.StartedAt != started {
		t.Fatalf("started_at should be kept: %v", a.StartedAt)
	}
	a, _ = st.SetStatus(a.ID, StatusDone, nil)
	if a.StartedAt == nil || *a.StartedAt != started || a.DoneAt == nil {
		t.Fatalf("done: %+v", a)
	}
	if d, ok := a.WorkTime(time.Now()); !ok || d < 0 {
		t.Fatalf("work time %v %v", d, ok)
	}
	a, _ = st.SetStatus(a.ID, StatusTodo, nil)
	if a.StartedAt != nil || a.DoneAt != nil {
		t.Fatalf("todo should clear times: %+v", a)
	}
	// 着手せずに完了したタスクは着手日時なし
	ts, _ := st.ListTasks(s.ID, nil)
	b, _ := st.SetStatus(ts[1].ID, StatusDone, nil)
	if b.StartedAt != nil {
		t.Fatalf("started_at should stay nil: %v", *b.StartedAt)
	}
	if _, ok := b.WorkTime(time.Now()); ok {
		t.Fatal("no work time without started_at")
	}
}
