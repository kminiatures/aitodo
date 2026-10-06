package mcp

import (
	"bufio"
	"encoding/json"
	"io"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kminiatures/aitodo/internal/store"
)

func TestGoNotifiesOnce(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	sess, _ := st.CreateSession("proj", "", t.TempDir())
	t.Setenv("AITODO_SESSION", "proj")
	tasks, _ := st.AddTasks(sess.ID, []store.NewTask{{Title: "やること", Body: "詳細"}})
	id := tasks[0].ID

	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	s := &server{st: st, version: "test", w: bufio.NewWriter(outW), watchInterval: 20 * time.Millisecond, watchDelay: 50 * time.Millisecond}
	done := make(chan error, 1)
	go func() { done <- serve(s, inR) }()
	lines := make(chan string, 16)
	go func() {
		sc := bufio.NewScanner(outR)
		for sc.Scan() {
			lines <- sc.Text()
		}
	}()
	next := func() string {
		select {
		case l := <-lines:
			return l
		case <-time.After(2 * time.Second):
			return ""
		}
	}

	io.WriteString(inW, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}`+"\n")
	if l := next(); !strings.Contains(l, `"claude/channel"`) {
		t.Fatalf("initialize should declare claude/channel: %s", l)
	}
	io.WriteString(inW, `{"jsonrpc":"2.0","method":"notifications/initialized"}`+"\n")
	st.SetStatus(id, store.StatusGo, nil)

	var n struct {
		ID     *json.RawMessage `json:"id"`
		Method string           `json:"method"`
		Params struct {
			Content string            `json:"content"`
			Meta    map[string]string `json:"meta"`
		} `json:"params"`
	}
	l := next()
	if err := json.Unmarshal([]byte(l), &n); err != nil {
		t.Fatalf("bad line %q: %v", l, err)
	}
	if n.ID != nil || n.Method != "notifications/claude/channel" || n.Params.Meta["task_id"] != "1" ||
		n.Params.Meta["session"] != "proj" || !strings.Contains(n.Params.Content, "やること") {
		t.Fatalf("unexpected notification: %s", l)
	}
	// 同じ go を何度も知らせない
	select {
	case l := <-lines:
		t.Fatalf("notified twice: %s", l)
	case <-time.After(150 * time.Millisecond):
	}
	// 取ったあと再び go にすると、また知らせる
	io.WriteString(inW, `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"task_claim","arguments":{"id":1}}}`+"\n")
	if l := next(); !strings.Contains(l, `\"status\": \"doing\"`) {
		t.Fatalf("task_claim: %s", l)
	}
	time.Sleep(60 * time.Millisecond) // 監視が doing を見て忘れるまで待つ
	st.SetStatus(id, store.StatusGo, nil)
	if l := next(); !strings.Contains(l, "notifications/claude/channel") {
		t.Fatalf("want re-notification, got %q", l)
	}

	inW.Close()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
