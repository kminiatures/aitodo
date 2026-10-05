package web

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kminiatures/aitodo/internal/store"
)

func newServer(t *testing.T) (*store.Store, *httptest.Server) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	ts := httptest.NewServer(Handler(st, Options{}))
	t.Cleanup(ts.Close)
	return st, ts
}

func call(t *testing.T, ts *httptest.Server, method, path, body string, out any) int {
	t.Helper()
	var r io.Reader
	if body != "" {
		r = strings.NewReader(body)
	}
	req, _ := http.NewRequest(method, ts.URL+path, r)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if out != nil {
		if err := json.NewDecoder(res.Body).Decode(out); err != nil {
			t.Fatalf("%s %s: decode: %v", method, path, err)
		}
	}
	return res.StatusCode
}

func TestAPIFlow(t *testing.T) {
	_, ts := newServer(t)

	var s store.Session
	if code := call(t, ts, "POST", "/api/sessions", `{"name":"p"}`, &s); code != 200 || s.ID == 0 {
		t.Fatalf("create session: %d %+v", code, s)
	}
	var tasks []store.Task
	if call(t, ts, "GET", "/api/sessions/1/tasks", "", &tasks); tasks == nil || len(tasks) != 0 {
		t.Fatalf("empty session should return [], got %#v", tasks)
	}

	var a, b, sub store.Task
	call(t, ts, "POST", "/api/sessions/1/tasks", `{"title":"A"}`, &a)
	call(t, ts, "POST", "/api/sessions/1/tasks", `{"title":"B"}`, &b)
	call(t, ts, "POST", "/api/tasks/1/subtasks", `{"title":"A-1"}`, &sub)
	if sub.ParentID == nil || *sub.ParentID != a.ID {
		t.Fatalf("subtask parent: %+v", sub)
	}

	var moved store.Task
	call(t, ts, "POST", "/api/tasks/2/move", `{"delta":-1}`, &moved)
	call(t, ts, "GET", "/api/sessions/1/tasks", "", &tasks)
	if tasks[0].Title != "B" {
		t.Fatalf("move: order = %v", []string{tasks[0].Title, tasks[1].Title})
	}

	var done store.Task
	call(t, ts, "POST", "/api/tasks/3/status", `{"status":"done"}`, &done)
	if done.Status != "done" || done.DoneAt == nil {
		t.Fatalf("status: %+v", done)
	}
	if code := call(t, ts, "POST", "/api/tasks/3/status", `{"status":"bogus"}`, nil); code != 400 {
		t.Fatalf("invalid status: %d", code)
	}

	title := "A!"
	var ed store.Task
	call(t, ts, "PATCH", "/api/tasks/1", `{"title":"`+title+`","note":"n"}`, &ed)
	if ed.Title != title || ed.Note != "n" {
		t.Fatalf("edit: %+v", ed)
	}

	var c store.Comment
	call(t, ts, "POST", "/api/tasks/1/comments", `{"body":"hello"}`, &c)
	if c.Author == "" || c.Body != "hello" {
		t.Fatalf("comment: %+v", c)
	}
	var d store.TaskDetail
	call(t, ts, "GET", "/api/tasks/1", "", &d)
	if len(d.Comments) != 1 || len(d.Subtasks) != 1 {
		t.Fatalf("detail: %+v", d)
	}
	if code := call(t, ts, "DELETE", "/api/comments/1", "", nil); code != 200 {
		t.Fatalf("delete comment: %d", code)
	}

	if code := call(t, ts, "DELETE", "/api/tasks/1", "", nil); code != 200 {
		t.Fatalf("delete task: %d", code)
	}
	if code := call(t, ts, "GET", "/api/tasks/3", "", nil); code != 404 {
		t.Fatalf("subtask should be deleted with parent: %d", code)
	}

	call(t, ts, "PATCH", "/api/sessions/1", `{"status":"archived"}`, &s)
	var ss []store.Session
	call(t, ts, "GET", "/api/sessions", "", &ss)
	if len(ss) != 0 {
		t.Fatalf("archived session should be hidden: %+v", ss)
	}
	call(t, ts, "GET", "/api/sessions?all=1", "", &ss)
	if len(ss) != 1 {
		t.Fatalf("all=1 should include archived: %+v", ss)
	}
}

func TestSessionWorkdir(t *testing.T) {
	_, ts := newServer(t)
	if code := call(t, ts, "POST", "/api/sessions", `{"name":"rel","workdir":"some/rel"}`, nil); code != 400 {
		t.Fatalf("relative workdir should be rejected: %d", code)
	}
	var s store.Session
	call(t, ts, "POST", "/api/sessions", `{"name":"p","workdir":"~/x"}`, &s)
	if !filepath.IsAbs(s.Workdir) {
		t.Fatalf("~ should expand: %q", s.Workdir)
	}
	// 作業フォルダを空にすると解除される
	call(t, ts, "PATCH", "/api/sessions/1", `{"name":"p2","workdir":""}`, &s)
	if s.Name != "p2" || s.Workdir != "" {
		t.Fatalf("edit: %+v", s)
	}
}

func TestGuard(t *testing.T) {
	_, ts := newServer(t)

	// フォーム送信（Content-Type が JSON でない）は拒否
	res, err := http.Post(ts.URL+"/api/sessions", "application/x-www-form-urlencoded", strings.NewReader("name=x"))
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusUnsupportedMediaType {
		t.Fatalf("form post: %d", res.StatusCode)
	}

	// 別オリジンからの書き込みは拒否
	req, _ := http.NewRequest("POST", ts.URL+"/api/sessions", strings.NewReader(`{"name":"x"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "http://evil.example")
	res, _ = http.DefaultClient.Do(req)
	res.Body.Close()
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("cross-origin: %d", res.StatusCode)
	}

	// ループバック以外の Host は拒否（DNS リバインディング）
	req, _ = http.NewRequest("GET", ts.URL+"/api/sessions", nil)
	req.Host = "evil.example:7878"
	res, _ = http.DefaultClient.Do(req)
	res.Body.Close()
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("foreign host: %d", res.StatusCode)
	}

	// 静的ファイルは配信される
	res, _ = http.Get(ts.URL + "/")
	b, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != 200 || !strings.Contains(string(b), "app.js") {
		t.Fatalf("index: %d", res.StatusCode)
	}
}

func TestLoopback(t *testing.T) {
	for h, want := range map[string]bool{
		"localhost:1": true, "127.0.0.1:7878": true, "[::1]:80": true, "127.0.0.1": true,
		"0.0.0.0:7878": false, "192.168.0.2:1": false, "example.com": false,
	} {
		if got := IsLoopback(h); got != want {
			t.Errorf("IsLoopback(%q) = %v, want %v", h, got, want)
		}
	}
}
