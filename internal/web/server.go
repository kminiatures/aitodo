// Package web は aitodo の Web UI（JSON API と埋め込みの静的ファイル）を提供する。
package web

import (
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"mime"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/kminiatures/aitodo/internal/store"
)

//go:embed static
var staticFS embed.FS

type server struct {
	st      *store.Store
	initial int64 // 最初に選択するセッション（0 なら未指定）
}

// Options は Handler の設定。
type Options struct {
	InitialSession int64 // 最初に選択するセッション（0 なら未指定）
	AnyHost        bool  // ループバック以外の Host ヘッダーも受け付ける（--addr で外部公開したとき）
}

// Handler は Web UI と API をまとめた http.Handler を返す。
func Handler(st *store.Store, opt Options) http.Handler {
	initial := opt.InitialSession
	s := &server{st: st, initial: initial}
	mux := http.NewServeMux()

	mux.HandleFunc("GET /api/config", s.config)
	mux.HandleFunc("GET /api/sessions", s.listSessions)
	mux.HandleFunc("POST /api/sessions", s.createSession)
	mux.HandleFunc("PATCH /api/sessions/{id}", s.updateSession)
	mux.HandleFunc("DELETE /api/sessions/{id}", s.deleteSession)
	mux.HandleFunc("GET /api/sessions/{id}/tasks", s.listTasks)
	mux.HandleFunc("POST /api/sessions/{id}/tasks", s.addTask)

	mux.HandleFunc("GET /api/tasks/{id}", s.getTask)
	mux.HandleFunc("PATCH /api/tasks/{id}", s.updateTask)
	mux.HandleFunc("DELETE /api/tasks/{id}", s.deleteTask)
	mux.HandleFunc("POST /api/tasks/{id}/subtasks", s.addSubtask)
	mux.HandleFunc("POST /api/tasks/{id}/status", s.setStatus)
	mux.HandleFunc("POST /api/tasks/{id}/move", s.moveTask)
	mux.HandleFunc("POST /api/tasks/{id}/comments", s.addComment)
	mux.HandleFunc("DELETE /api/comments/{id}", s.deleteComment)

	sub, _ := fs.Sub(staticFS, "static")
	mux.Handle("GET /", http.FileServerFS(sub))
	return guard(mux, opt.AnyHost)
}

// guard はローカルで動かす前提の簡易防御。
//   - Host がループバック以外なら拒否（DNS リバインディング対策）
//   - 書き込み系は Content-Type: application/json を必須にし（DELETE を除く）、Origin があれば Host と一致させる（CSRF 対策）
func guard(next http.Handler, anyHost bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !anyHost && !loopbackHost(r.Host) {
			writeErr(w, http.StatusForbidden, errors.New("forbidden host"))
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			// DELETE は本文を持たない。別オリジンからの DELETE は CORS プリフライトが必要で、それを許可しないので通らない
			if r.Method != http.MethodDelete {
				mt, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
				if mt != "application/json" {
					writeErr(w, http.StatusUnsupportedMediaType, errors.New("Content-Type must be application/json"))
					return
				}
			}
			if o := r.Header.Get("Origin"); o != "" && o != "http://"+r.Host && o != "https://"+r.Host {
				writeErr(w, http.StatusForbidden, errors.New("cross-origin request"))
				return
			}
		}
		w.Header().Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}

// IsLoopback は host[:port] がループバック（localhost / 127.0.0.0/8 / ::1）か。
func IsLoopback(hostport string) bool { return loopbackHost(hostport) }

func loopbackHost(hostport string) bool {
	h := hostport
	if hh, _, err := net.SplitHostPort(hostport); err == nil {
		h = hh
	}
	h = strings.Trim(h, "[]")
	if h == "localhost" {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}

// ---------- helpers ----------

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, err error) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
}

// fail はストアのエラーを HTTP ステータスに振り分けて返す。
func fail(w http.ResponseWriter, err error) {
	code := http.StatusBadRequest
	if errors.Is(err, store.ErrNotFound) || strings.Contains(err.Error(), "not found") {
		code = http.StatusNotFound
	}
	writeErr(w, code, err)
}

func pathID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("invalid id %q", r.PathValue("id")))
		return 0, false
	}
	return id, true
}

func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(v); err != nil && !errors.Is(err, io.EOF) {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("invalid JSON: %w", err))
		return false
	}
	return true
}

// checkWorkdir はブラウザから渡された作業フォルダを確かめる。サーバーの cwd に意味は無いので相対パスは受け付けない。
func checkWorkdir(dir string) error {
	if dir == "" || dir == "~" || strings.HasPrefix(dir, "~/") || filepath.IsAbs(dir) {
		return nil
	}
	return fmt.Errorf("workdir must be an absolute path or start with ~/ (got %q)", dir)
}

// humanAuthor は Web UI から書くコメントの投稿者名（TUI と同じく $USER）。
func humanAuthor() string {
	if u := os.Getenv("USER"); u != "" {
		return u
	}
	return "human"
}

// ---------- sessions ----------

func (s *server) config(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]any{"initial_session": s.initial, "author": humanAuthor()})
}

func (s *server) listSessions(w http.ResponseWriter, r *http.Request) {
	xs, err := s.st.ListSessions(r.URL.Query().Get("all") == "1")
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, xs)
}

func (s *server) createSession(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		Workdir     string `json:"workdir"`
	}
	if !decode(w, r, &in) {
		return
	}
	if err := checkWorkdir(strings.TrimSpace(in.Workdir)); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	x, err := s.st.CreateSession(in.Name, in.Description, in.Workdir)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, x)
}

func (s *server) updateSession(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var in struct {
		Name        *string `json:"name"`
		Description *string `json:"description"`
		Workdir     *string `json:"workdir"`
		Status      *string `json:"status"`
	}
	if !decode(w, r, &in) {
		return
	}
	if in.Workdir != nil {
		if err := checkWorkdir(strings.TrimSpace(*in.Workdir)); err != nil {
			writeErr(w, http.StatusBadRequest, err)
			return
		}
	}
	x, err := s.st.UpdateSession(id, store.SessionPatch{Name: in.Name, Description: in.Description, Workdir: in.Workdir, Status: in.Status})
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, x)
}

func (s *server) deleteSession(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if err := s.st.DeleteSession(id); err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

// ---------- tasks ----------

func (s *server) listTasks(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if _, err := s.st.GetSession(id); err != nil {
		fail(w, err)
		return
	}
	ts, err := s.st.ListTasks(id, nil)
	if err != nil {
		fail(w, err)
		return
	}
	if ts == nil {
		ts = []store.Task{}
	}
	writeJSON(w, ts)
}

type newTaskIn struct {
	Title string `json:"title"`
	Body  string `json:"body"`
}

func (s *server) addTask(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var in newTaskIn
	if !decode(w, r, &in) {
		return
	}
	t, err := s.st.AddTask(id, in.Title, in.Body)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, t)
}

func (s *server) addSubtask(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var in newTaskIn
	if !decode(w, r, &in) {
		return
	}
	t, err := s.st.AddSubtask(id, in.Title, in.Body)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, t)
}

func (s *server) getTask(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	d, err := s.st.GetTaskDetail(id)
	if err != nil {
		fail(w, err)
		return
	}
	if d.Subtasks == nil {
		d.Subtasks = []store.Task{}
	}
	if d.Comments == nil {
		d.Comments = []store.Comment{}
	}
	writeJSON(w, d)
}

func (s *server) updateTask(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var in struct {
		Title    *string `json:"title"`
		Body     *string `json:"body"`
		Note     *string `json:"note"`
		ParentID *int64  `json:"parent_id"`
	}
	if !decode(w, r, &in) {
		return
	}
	t, err := s.st.UpdateTask(id, store.TaskPatch{Title: in.Title, Body: in.Body, Note: in.Note, ParentID: in.ParentID})
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, t)
}

func (s *server) deleteTask(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if err := s.st.DeleteTask(id); err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

func (s *server) setStatus(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var in struct {
		Status string  `json:"status"`
		Note   *string `json:"note"`
	}
	if !decode(w, r, &in) {
		return
	}
	t, err := s.st.SetStatus(id, in.Status, in.Note)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, t)
}

// moveTask は {"delta": -1|1} で兄弟の中を 1 つ上下するか、{"index": N} で位置を指定する。
func (s *server) moveTask(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var in struct {
		Delta *int `json:"delta"`
		Index *int `json:"index"`
	}
	if !decode(w, r, &in) {
		return
	}
	var idx int
	switch {
	case in.Index != nil:
		idx = *in.Index
	case in.Delta != nil:
		cur, err := s.st.TaskIndex(id)
		if err != nil {
			fail(w, err)
			return
		}
		idx = cur + *in.Delta
	default:
		writeErr(w, http.StatusBadRequest, errors.New("delta or index is required"))
		return
	}
	t, err := s.st.MoveTask(id, idx)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, t)
}

// ---------- comments ----------

func (s *server) addComment(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var in struct {
		Body   string `json:"body"`
		Author string `json:"author"`
	}
	if !decode(w, r, &in) {
		return
	}
	if strings.TrimSpace(in.Author) == "" {
		in.Author = humanAuthor()
	}
	c, err := s.st.AddComment(id, in.Author, in.Body)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, c)
}

func (s *server) deleteComment(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if err := s.st.DeleteComment(id); err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}
