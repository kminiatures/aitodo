// Package mcp は aitodo を MCP (Model Context Protocol) サーバーとして stdio で提供する。
// 改行区切り JSON-RPC 2.0。stdout にはプロトコルメッセージ以外を書かないこと。
package mcp

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"

	"github.com/kminiatures/aitodo/internal/store"
)

const latestProtocol = "2025-06-18"

type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type server struct {
	st      *store.Store
	version string
	mu      sync.Mutex
	w       *bufio.Writer
}

func Serve(st *store.Store, in io.Reader, out io.Writer, version string) error {
	s := &server{st: st, version: version, w: bufio.NewWriter(out)}
	r := bufio.NewReader(in)
	for {
		line, err := r.ReadBytes('\n')
		if len(strings.TrimSpace(string(line))) > 0 {
			s.handleLine(line)
		}
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
	}
}

func (s *server) send(v any) {
	b, err := json.Marshal(v)
	if err != nil {
		fmt.Fprintln(os.Stderr, "aitodo mcp: marshal:", err)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.w.Write(b)
	s.w.WriteByte('\n')
	s.w.Flush()
}

func (s *server) handleLine(line []byte) {
	trim := strings.TrimSpace(string(line))
	if strings.HasPrefix(trim, "[") { // バッチ（古い仕様）にも一応対応
		var batch []json.RawMessage
		if err := json.Unmarshal([]byte(trim), &batch); err != nil {
			s.send(response{JSONRPC: "2.0", ID: json.RawMessage("null"), Error: &rpcError{-32700, "parse error"}})
			return
		}
		for _, m := range batch {
			s.handleLine(m)
		}
		return
	}
	var req request
	if err := json.Unmarshal([]byte(trim), &req); err != nil {
		s.send(response{JSONRPC: "2.0", ID: json.RawMessage("null"), Error: &rpcError{-32700, "parse error"}})
		return
	}
	isNotification := len(req.ID) == 0 || string(req.ID) == "null"
	if req.Method == "" { // クライアントからのレスポンス等は無視
		return
	}
	result, rerr := s.dispatch(&req)
	if isNotification {
		return
	}
	resp := response{JSONRPC: "2.0", ID: req.ID}
	if rerr != nil {
		resp.Error = rerr
	} else {
		resp.Result = result
	}
	s.send(resp)
}

func (s *server) dispatch(req *request) (any, *rpcError) {
	switch req.Method {
	case "initialize":
		var p struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		json.Unmarshal(req.Params, &p)
		pv := p.ProtocolVersion
		if pv == "" {
			pv = latestProtocol
		}
		return map[string]any{
			"protocolVersion": pv,
			"capabilities":    map[string]any{"tools": map[string]any{"listChanged": false}},
			"serverInfo":      map[string]any{"name": "aitodo", "title": "aitodo — AI-oriented TODO", "version": s.version},
			"instructions":    instructions,
		}, nil
	case "ping":
		return map[string]any{}, nil
	case "tools/list":
		return map[string]any{"tools": toolDefs}, nil
	case "tools/call":
		var p struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return nil, &rpcError{-32602, "invalid params"}
		}
		return s.callTool(p.Name, p.Arguments), nil
	case "resources/list":
		return map[string]any{"resources": []any{}}, nil
	case "prompts/list":
		return map[string]any{"prompts": []any{}}, nil
	}
	if strings.HasPrefix(req.Method, "notifications/") {
		return nil, nil
	}
	return nil, &rpcError{-32601, "method not found: " + req.Method}
}

const instructions = `aitodo is a persistent TODO list for AI agents, organised as sessions -> tasks.
A session usually corresponds to a working folder (workdir). When "session" is omitted, tools resolve the session
from "workdir" or, failing that, from the server's current directory (longest-prefix match).
Typical loop: task_next(claim=true) -> do the work -> task_done(id, note, comment) -> repeat until task_next returns null.
Write a plan up front with task_add_bulk (items may nest "subtasks"). Tasks can have subtasks (parent_id); task_next
only returns tasks whose subtasks are all finished, so children come first and the parent comes back last for a final check.
Use task_comment to log progress, findings and results on a task (append-only, visible to humans in the TUI);
"note" is the single short result summary. Record blockers with task_set_status(status="blocked", note=...).`

// ---------- tool definitions ----------

func obj(props map[string]any, required ...string) map[string]any {
	m := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		m["required"] = required
	}
	return m
}

func str(desc string) map[string]any { return map[string]any{"type": "string", "description": desc} }
func integer(desc string) map[string]any {
	return map[string]any{"type": "integer", "description": desc}
}
func boolean(desc string) map[string]any {
	return map[string]any{"type": "boolean", "description": desc}
}

var (
	sessionProp = str("Session ID or exact name. Optional: if omitted the session is resolved from `workdir` or the server's cwd.")
	workdirProp = str("Directory used to resolve the session when `session` is omitted (longest workdir prefix match). Usually your project root.")
	statusEnum  = map[string]any{"type": "string", "enum": store.ValidStatuses,
		"description": "todo = not started, doing = in progress, done = finished, skipped = intentionally not done, blocked = cannot proceed (explain in note)"}
)

func withSession(props map[string]any) map[string]any {
	props["session"] = sessionProp
	props["workdir"] = workdirProp
	return props
}

var toolDefs = []map[string]any{
	{"name": "session_list", "description": "List sessions with progress counts (total/done/doing) and workdir.",
		"inputSchema": obj(map[string]any{"include_archived": boolean("Include archived sessions.")})},
	{"name": "session_create", "description": "Create a session (a unit of work that owns tasks). Set workdir to the project folder so the session can be found from that directory later.",
		"inputSchema": obj(map[string]any{
			"name":        str("Unique session name."),
			"workdir":     str("Working folder for this session (absolute path recommended; '~' is expanded)."),
			"description": str("Goal / context of the session."),
		}, "name")},
	{"name": "session_get", "description": "Get a session and all of its tasks in order. Use this to read the full plan.",
		"inputSchema": obj(withSession(map[string]any{}))},
	{"name": "session_update", "description": "Update a session's name, workdir, description or status (active|archived).",
		"inputSchema": obj(withSession(map[string]any{
			"name":        str("New name."),
			"new_workdir": str("New working folder."),
			"description": str("New description."),
			"status":      map[string]any{"type": "string", "enum": []string{"active", "archived"}},
		}))},
	{"name": "task_add", "description": "Append one task to a session, or a subtask under parent_id.",
		"inputSchema": obj(withSession(map[string]any{
			"title":     str("Short imperative title."),
			"body":      str("Details, acceptance criteria, file paths, etc."),
			"parent_id": integer("Make it a subtask of this task (session is taken from the parent)."),
		}), "title")},
	{"name": "task_add_bulk", "description": "Append many tasks at once, in order. Use this to write a whole plan. Items may contain nested `subtasks` (same shape, any depth).",
		"inputSchema": obj(withSession(map[string]any{
			"tasks": map[string]any{"type": "array", "description": "Tasks in execution order.",
				"items": obj(map[string]any{
					"title": str("Task title."),
					"body":  str("Optional details."),
					"subtasks": map[string]any{"type": "array", "description": "Nested subtasks: objects with title, body, subtasks.",
						"items": map[string]any{"type": "object"}},
				}, "title")},
			"parent_id": integer("Add all tasks as subtasks of this task."),
		}), "tasks")},
	{"name": "task_list", "description": "List tasks of a session in order. Optionally filter by status.",
		"inputSchema": obj(withSession(map[string]any{
			"status": map[string]any{"type": "array", "items": statusEnum, "description": "Only these statuses (default: all)."},
		}))},
	{"name": "task_get", "description": "Get one task by ID with its direct subtasks and all comments.",
		"inputSchema": obj(map[string]any{"id": integer("Task ID.")}, "id")},
	{"name": "task_next", "description": "Return the task to work on next (with its comments and subtasks): an in-progress (doing) task if any, else the first todo, considering only tasks whose subtasks are all finished. With claim=true the todo is atomically marked doing. Returns null when nothing is left.",
		"inputSchema": obj(withSession(map[string]any{
			"claim": boolean("Mark the returned todo task as doing."),
			"fresh": boolean("Ignore tasks already in progress and always take a new todo. Use when several agents work on the same session in parallel."),
		}))},
	{"name": "task_start", "description": "Mark a task as doing (in progress).",
		"inputSchema": obj(map[string]any{"id": integer("Task ID.")}, "id")},
	{"name": "task_done", "description": "Check off a task (status=done). Put a one-line result summary in note; put longer details in comment.",
		"inputSchema": obj(map[string]any{
			"id":      integer("Task ID."),
			"note":    str("Short result summary (overwrites the previous note)."),
			"comment": str("Optional detailed result to append as a comment (what changed, where, how verified, follow-ups)."),
			"author":  str("Comment author name (default \"ai\")."),
		}, "id")},
	{"name": "task_set_status", "description": "Set any status (todo|doing|done|skipped|blocked) with an optional note/comment. Use blocked + note to explain why you cannot proceed.",
		"inputSchema": obj(map[string]any{"id": integer("Task ID."), "status": statusEnum, "note": str("Optional note."),
			"comment": str("Optional comment to append."), "author": str("Comment author name (default \"ai\").")}, "id", "status")},
	{"name": "task_comment", "description": "Append a comment to a task: progress, findings, results, questions for the human. Comments are append-only and shown in the TUI.",
		"inputSchema": obj(map[string]any{"id": integer("Task ID."), "body": str("Comment text (markdown ok)."),
			"author": str("Author name, e.g. your agent name (default \"ai\").")}, "id", "body")},
	{"name": "task_comments", "description": "List all comments on a task, oldest first.",
		"inputSchema": obj(map[string]any{"id": integer("Task ID.")}, "id")},
	{"name": "task_edit", "description": "Edit a task's title, body or note, or move it under another parent.",
		"inputSchema": obj(map[string]any{"id": integer("Task ID."), "title": str("New title."), "body": str("New body."), "note": str("New note."),
			"parent_id": integer("New parent task ID; 0 makes it a top-level task.")}, "id")},
	{"name": "task_move", "description": "Move a task to a 0-based position among its siblings (tasks with the same parent).",
		"inputSchema": obj(map[string]any{"id": integer("Task ID."), "index": integer("0-based target index.")}, "id", "index")},
	{"name": "task_delete", "description": "Delete a task permanently, including its subtasks and comments. Prefer task_set_status(skipped) to keep history.",
		"inputSchema": obj(map[string]any{"id": integer("Task ID.")}, "id")},
}

// ---------- tool calls ----------

type args struct {
	Session         *json.RawMessage `json:"session"`
	Workdir         string           `json:"workdir"`
	NewWorkdir      *string          `json:"new_workdir"`
	Name            *string          `json:"name"`
	Description     *string          `json:"description"`
	Status          json.RawMessage  `json:"status"`
	IncludeArchived bool             `json:"include_archived"`
	Title           *string          `json:"title"`
	Body            *string          `json:"body"`
	Note            *string          `json:"note"`
	Tasks           []store.NewTask  `json:"tasks"`
	ID              int64            `json:"id"`
	Index           int              `json:"index"`
	Claim           bool             `json:"claim"`
	ParentID        *int64           `json:"parent_id"`
	Comment         *string          `json:"comment"`
	Author          string           `json:"author"`
	Fresh           bool             `json:"fresh"`
}

func (a *args) sessionRef() string {
	if a.Session == nil {
		return ""
	}
	var s string
	if json.Unmarshal(*a.Session, &s) == nil {
		return s
	}
	var n json.Number
	if json.Unmarshal(*a.Session, &n) == nil {
		return n.String()
	}
	return ""
}

func (a *args) statusString() string {
	var s string
	json.Unmarshal(a.Status, &s)
	return s
}

func (a *args) statusList() []string {
	var l []string
	if json.Unmarshal(a.Status, &l) == nil {
		return l
	}
	if s := a.statusString(); s != "" {
		return []string{s}
	}
	return nil
}

func (s *server) resolve(a *args) (*store.Session, error) {
	if ref := a.sessionRef(); ref != "" {
		return s.st.FindSession(ref)
	}
	dir := a.Workdir
	if dir == "" {
		if ref := os.Getenv("AITODO_SESSION"); ref != "" {
			return s.st.FindSession(ref)
		}
		var err error
		if dir, err = os.Getwd(); err != nil {
			return nil, err
		}
	}
	return s.st.ResolveByDir(dir)
}

func (s *server) callTool(name string, raw json.RawMessage) map[string]any {
	var a args
	if len(raw) > 0 && string(raw) != "null" {
		if err := json.Unmarshal(raw, &a); err != nil {
			return toolError(fmt.Errorf("invalid arguments: %w", err))
		}
	}
	v, err := s.runTool(name, &a)
	if err != nil {
		return toolError(err)
	}
	b, _ := json.MarshalIndent(v, "", "  ")
	return map[string]any{"content": []any{map[string]any{"type": "text", "text": string(b)}}, "isError": false}
}

func toolError(err error) map[string]any {
	return map[string]any{"content": []any{map[string]any{"type": "text", "text": "error: " + err.Error()}}, "isError": true}
}

func (s *server) runTool(name string, a *args) (any, error) {
	switch name {
	case "session_list":
		return s.st.ListSessions(a.IncludeArchived)
	case "session_create":
		if a.Name == nil {
			return nil, errors.New("name is required")
		}
		desc := ""
		if a.Description != nil {
			desc = *a.Description
		}
		return s.st.CreateSession(*a.Name, desc, a.Workdir)
	case "session_get":
		sess, err := s.resolve(a)
		if err != nil {
			return nil, err
		}
		ts, err := s.st.ListTasks(sess.ID, nil)
		if err != nil {
			return nil, err
		}
		return map[string]any{"session": sess, "tasks": ts}, nil
	case "session_update":
		sess, err := s.resolve(a)
		if err != nil {
			return nil, err
		}
		patch := store.SessionPatch{Name: a.Name, Description: a.Description, Workdir: a.NewWorkdir}
		if st := a.statusString(); st != "" {
			patch.Status = &st
		}
		if patch == (store.SessionPatch{}) {
			return nil, errors.New("nothing to update: pass name, new_workdir, description or status (`workdir` only selects the session)")
		}
		return s.st.UpdateSession(sess.ID, patch)
	case "task_add":
		if a.Title == nil {
			return nil, errors.New("title is required")
		}
		body := ""
		if a.Body != nil {
			body = *a.Body
		}
		if a.ParentID != nil && *a.ParentID != 0 {
			return s.st.AddSubtask(*a.ParentID, *a.Title, body)
		}
		sess, err := s.resolve(a)
		if err != nil {
			return nil, err
		}
		return s.st.AddTask(sess.ID, *a.Title, body)
	case "task_add_bulk":
		if a.ParentID != nil && *a.ParentID != 0 {
			return s.st.AddSubtasks(*a.ParentID, a.Tasks)
		}
		sess, err := s.resolve(a)
		if err != nil {
			return nil, err
		}
		return s.st.AddTasks(sess.ID, a.Tasks)
	case "task_list":
		sess, err := s.resolve(a)
		if err != nil {
			return nil, err
		}
		return s.st.ListTasks(sess.ID, a.statusList())
	case "task_get":
		return s.st.GetTaskDetail(a.ID)
	case "task_comment":
		body := ""
		if a.Body != nil {
			body = *a.Body
		}
		return s.st.AddComment(a.ID, a.author(), body)
	case "task_comments":
		if _, err := s.st.GetTask(a.ID); err != nil {
			return nil, err
		}
		return s.st.ListComments(a.ID)
	case "task_next":
		sess, err := s.resolve(a)
		if err != nil {
			return nil, err
		}
		t, err := s.st.NextTask(sess.ID, a.Claim, a.Fresh)
		if err != nil {
			return nil, err
		}
		if t == nil {
			return map[string]any{"task": nil, "message": "no remaining tasks in session " + sess.Name}, nil
		}
		d, err := s.st.GetTaskDetail(t.ID)
		if err != nil {
			return nil, err
		}
		return map[string]any{"task": d, "remaining": sess.Total - sess.Done}, nil
	case "task_start":
		return s.setStatus(a, store.StatusDoing)
	case "task_done":
		return s.setStatus(a, store.StatusDone)
	case "task_set_status":
		return s.setStatus(a, a.statusString())
	case "task_edit":
		if _, err := s.st.UpdateTask(a.ID, store.TaskPatch{Title: a.Title, Body: a.Body, Note: a.Note, ParentID: a.ParentID}); err != nil {
			return nil, err
		}
		return s.st.GetTaskDetail(a.ID)
	case "task_move":
		return s.st.MoveTask(a.ID, a.Index)
	case "task_delete":
		if _, err := s.st.GetTask(a.ID); err != nil {
			return nil, err
		}
		return map[string]any{"deleted_task": a.ID}, s.st.DeleteTask(a.ID)
	}
	return nil, fmt.Errorf("unknown tool %q", name)
}

func (a *args) author() string {
	if a.Author != "" {
		return a.Author
	}
	if v := os.Getenv("AITODO_AUTHOR"); v != "" {
		return v
	}
	return "ai"
}

// setStatus はステータス変更と（指定があれば）コメント追記をまとめて行う。
func (s *server) setStatus(a *args, status string) (any, error) {
	if _, err := s.st.SetStatus(a.ID, status, a.Note); err != nil {
		return nil, err
	}
	if a.Comment != nil && strings.TrimSpace(*a.Comment) != "" {
		if _, err := s.st.AddComment(a.ID, a.author(), *a.Comment); err != nil {
			return nil, err
		}
	}
	return s.st.GetTask(a.ID)
}
