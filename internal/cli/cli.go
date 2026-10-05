// Package cli は aitodo のコマンドラインインターフェース（人間と AI 向け API コマンド群）。
package cli

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/kminiatures/aitodo/docs"
	"github.com/kminiatures/aitodo/internal/mcp"
	"github.com/kminiatures/aitodo/internal/store"
	"github.com/kminiatures/aitodo/internal/tui"
	"github.com/kminiatures/aitodo/internal/web"
)

var Version = "1.0.0"

type app struct {
	dbPath string
	json   bool
	st     *store.Store
	out    io.Writer
	in     io.Reader
}

// Main は終了コードを返す。
func Main(args []string) int {
	a := &app{out: os.Stdout, in: os.Stdin}
	rest := []string{}
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "--json":
			a.json = true
		case args[i] == "--db" && i+1 < len(args):
			a.dbPath = args[i+1]
			i++
		case strings.HasPrefix(args[i], "--db="):
			a.dbPath = strings.TrimPrefix(args[i], "--db=")
		case args[i] == "--":
			rest = append(rest, args[i:]...)
			i = len(args)
		default:
			rest = append(rest, args[i])
		}
	}
	if os.Getenv("AITODO_JSON") == "1" {
		a.json = true
	}
	if err := a.run(rest); err != nil {
		if a.json {
			b, _ := json.Marshal(map[string]string{"error": err.Error()})
			fmt.Fprintln(os.Stderr, string(b))
		} else {
			fmt.Fprintln(os.Stderr, "aitodo:", err)
		}
		return 1
	}
	return 0
}

func (a *app) open() error {
	if a.st != nil {
		return nil
	}
	st, err := store.Open(a.dbPath)
	if err != nil {
		return err
	}
	a.st = st
	return nil
}

func (a *app) run(args []string) error {
	if len(args) == 0 {
		if !isTerminal(os.Stdin) || !isTerminal(os.Stdout) {
			// AI のツール実行など TTY が無い環境では TUI を起動せず概要を出す
			if err := a.open(); err != nil {
				return err
			}
			defer a.st.Close()
			if err := a.cmdStatus(nil); err != nil {
				fmt.Fprint(a.out, usage)
			}
			return nil
		}
		return a.cmdTUI(nil)
	}
	cmd, rest := args[0], args[1:]
	switch cmd {
	case "help", "-h", "--help":
		fmt.Fprint(a.out, usage)
		return nil
	case "version", "--version":
		fmt.Fprintln(a.out, "aitodo", Version)
		return nil
	case "manual", "man":
		return a.cmdManual(rest)
	}
	if err := a.open(); err != nil {
		return err
	}
	defer a.st.Close()
	switch cmd {
	case "tui", "ui":
		return a.cmdTUI(rest)
	case "mcp":
		return mcp.Serve(a.st, os.Stdin, os.Stdout, Version)
	case "web", "serve":
		return a.cmdWeb(rest)
	case "session", "sessions", "s":
		return a.cmdSession(rest)
	case "task", "tasks", "t":
		return a.cmdTask(rest)
	// よく使うものはトップレベルのショートカット
	case "next":
		return a.taskNext(rest)
	case "add":
		return a.taskAdd(rest)
	case "import":
		return a.taskImport(rest)
	case "ls", "list":
		return a.taskList(rest)
	case "done":
		return a.taskStatus(store.StatusDone, rest)
	case "start":
		return a.taskStatus(store.StatusDoing, rest)
	case "status":
		return a.cmdStatus(rest)
	case "where", "current":
		return a.sessionCurrent(rest)
	case "comment", "comments":
		return a.cmdComment(rest)
	case "sub", "subtask":
		return a.subAdd(rest)
	}
	return fmt.Errorf("unknown command %q (see `aitodo help`)", cmd)
}

const usage = `aitodo - AI 向け TODO（セッション → タスク）

使い方:
  aitodo                         TUI を起動（マウス対応）
  aitodo manual                  AI/API 向けマニュアルを表示
  aitodo mcp                     MCP サーバー（stdio）として起動
  aitodo web [--addr HOST:PORT] [-s REF] [--open]
                                 Web UI を起動（既定 http://127.0.0.1:7878）

セッション:
  aitodo session add NAME [--dir PATH] [--desc TEXT]
  aitodo session list [--all]
  aitodo session show [REF]
  aitodo session edit [REF] [--name N] [--dir PATH] [--desc TEXT]
  aitodo session archive|unarchive [REF]
  aitodo session rm REF
  aitodo session current [--dir PATH]     作業フォルダからセッションを解決

タスク（-s REF 省略時は $AITODO_SESSION → カレントディレクトリで解決）:
  aitodo task add [-s REF] TITLE [--body TEXT] [--parent ID]
  aitodo sub PARENT_ID TITLE [--body TEXT]     サブタスクを追加
  aitodo task import [-s REF] [--parent ID] < plan.txt
                                 1 行 1 タスク。インデントした箇条書きはサブタスク、
                                 インデントした地の文は本文 / JSON 配列（subtasks で入れ子）
  aitodo task list [-s REF] [--status todo,doing] [--all]
  aitodo task show ID
  aitodo task next [-s REF] [--claim] [--fresh]
  aitodo task start|done|skip|block|reopen ID [--note TEXT] [--comment TEXT]
  aitodo task edit ID [--title T] [--body B] [--note N] [--parent ID|0]
  aitodo task move ID (--up | --down | --to INDEX)
  aitodo task rm ID

コメント（タスクへの追記型ログ。作業経過・結果・レビューなど）:
  aitodo comment ID TEXT... [--author NAME]   TEXT が - なら stdin から
  aitodo comment list ID
  aitodo comment rm COMMENT_ID
  投稿者の既定値は $AITODO_AUTHOR、なければ "ai"

ショートカット: add, sub, import, ls, next, start, done, comment, status, where

グローバルオプション:
  --json         機械可読な JSON で出力（エラーは stderr に {"error": ...}）
  --db PATH      DB ファイル（既定: $AITODO_DB または ~/.aitodo/aitodo.db）
`

// ---------- helpers ----------

func (a *app) emit(v any, text func()) error {
	if a.json {
		enc := json.NewEncoder(a.out)
		enc.SetIndent("", "  ")
		return enc.Encode(v)
	}
	text()
	return nil
}

func (a *app) resolveSession(ref string) (*store.Session, error) {
	if ref == "" {
		ref = os.Getenv("AITODO_SESSION")
	}
	if ref != "" {
		return a.st.FindSession(ref)
	}
	cwd, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	return a.st.ResolveByDir(cwd)
}

func parseID(s string) (int64, error) {
	id, err := strconv.ParseInt(strings.TrimPrefix(s, "#"), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid id %q", s)
	}
	return id, nil
}

func needOne(p *parsed, what string) (string, error) {
	if len(p.pos) != 1 {
		return "", fmt.Errorf("expected exactly one %s", what)
	}
	return p.pos[0], nil
}

var marks = map[string]string{
	store.StatusTodo:    "[ ]",
	store.StatusDoing:   "[>]",
	store.StatusDone:    "[x]",
	store.StatusSkipped: "[-]",
	store.StatusBlocked: "[!]",
}

func Mark(status string) string { return marks[status] }

func tildify(p string) string {
	if home, err := os.UserHomeDir(); err == nil && p != "" {
		if p == home {
			return "~"
		}
		if strings.HasPrefix(p, home+string(filepath.Separator)) {
			return "~" + p[len(home):]
		}
	}
	return p
}

func (a *app) printSession(s *store.Session) {
	wd := tildify(s.Workdir)
	if wd == "" {
		wd = "(no workdir)"
	}
	arch := ""
	if s.Status == store.SessionArchived {
		arch = "  [archived]"
	}
	fmt.Fprintf(a.out, "#%d  %s  %d/%d done  %s%s\n", s.ID, s.Name, s.Done, s.Total, wd, arch)
}

func (a *app) printTask(t *store.Task) {
	extra := ""
	if t.SubtasksTotal > 0 {
		extra += fmt.Sprintf("  (%d/%d)", t.SubtasksDone, t.SubtasksTotal)
	}
	if t.CommentCount > 0 {
		extra += fmt.Sprintf("  [%d comments]", t.CommentCount)
	}
	fmt.Fprintf(a.out, "%s%s #%d %s%s\n", strings.Repeat("  ", t.Depth), Mark(t.Status), t.ID, t.Title, extra)
}

func (a *app) printTaskDetail(d *store.TaskDetail) {
	t := &d.Task
	fmt.Fprintf(a.out, "#%d %s\n", t.ID, t.Title)
	fmt.Fprintf(a.out, "status:  %s\n", t.Status)
	fmt.Fprintf(a.out, "session: %d\n", t.SessionID)
	if t.ParentID != nil {
		fmt.Fprintf(a.out, "parent:  #%d\n", *t.ParentID)
	}
	fmt.Fprintf(a.out, "created: %s\n", localTime(t.CreatedAt))
	if t.StartedAt != nil {
		fmt.Fprintf(a.out, "started: %s\n", localTime(*t.StartedAt))
	}
	if t.DoneAt != nil {
		fmt.Fprintf(a.out, "%-8s %s\n", t.Status+":", localTime(*t.DoneAt))
	}
	if d, ok := t.WorkTime(time.Now()); ok {
		fmt.Fprintf(a.out, "took:    %s\n", tui.FmtDuration(d))
	}
	if t.Body != "" {
		fmt.Fprintf(a.out, "body:\n%s\n", indent(t.Body))
	}
	if t.Note != "" {
		fmt.Fprintf(a.out, "note:\n%s\n", indent(t.Note))
	}
	if len(d.Subtasks) > 0 {
		fmt.Fprintf(a.out, "subtasks (%d/%d):\n", t.SubtasksDone, t.SubtasksTotal)
		for i := range d.Subtasks {
			fmt.Fprint(a.out, "  ")
			a.printTask(&d.Subtasks[i])
		}
	}
	if len(d.Comments) > 0 {
		fmt.Fprintf(a.out, "comments (%d):\n", len(d.Comments))
		for i := range d.Comments {
			a.printComment(&d.Comments[i])
		}
	}
}

func (a *app) printComment(c *store.Comment) {
	author := c.Author
	if author == "" {
		author = "?"
	}
	fmt.Fprintf(a.out, "  [c%d] %s  %s\n%s\n", c.ID, author, localTime(c.CreatedAt), indent(indent(c.Body)))
}

// localTime は RFC3339 (UTC) の日時をローカル時刻の "2006-01-02 15:04" にする。
func localTime(s string) string {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return s
	}
	return t.Local().Format("2006-01-02 15:04")
}

func indent(s string) string {
	return "  " + strings.ReplaceAll(strings.TrimRight(s, "\n"), "\n", "\n  ")
}

// ---------- manual / tui ----------

func (a *app) cmdManual(args []string) error {
	topic := ""
	if len(args) > 0 {
		topic = args[0]
	}
	switch topic {
	case "", "agent", "ai":
		fmt.Fprint(a.out, docs.Manual)
	case "snippet":
		fmt.Fprint(a.out, docs.Snippet)
	default:
		return fmt.Errorf("unknown manual topic %q (topics: agent, snippet)", topic)
	}
	return nil
}

func (a *app) cmdTUI(args []string) error {
	p, err := parseArgs(args, flagSpec{value: []string{"s=session"}})
	if err != nil {
		return err
	}
	if err := a.open(); err != nil {
		return err
	}
	initial, err := a.initialSession(p)
	if err != nil {
		return err
	}
	return tui.Run(a.st, initial)
}

// initialSession は -s で指定されたセッション、なければカレントディレクトリから解決したセッションの ID（無ければ 0）。
func (a *app) initialSession(p *parsed) (int64, error) {
	if p.has("session") {
		s, err := a.st.FindSession(p.get("session"))
		if err != nil {
			return 0, err
		}
		return s.ID, nil
	}
	if cwd, err := os.Getwd(); err == nil {
		if s, err := a.st.ResolveByDir(cwd); err == nil {
			return s.ID, nil
		}
	}
	return 0, nil
}

func (a *app) cmdWeb(args []string) error {
	p, err := parseArgs(args, flagSpec{value: []string{"s=session", "addr"}, bools: []string{"open"}})
	if err != nil {
		return err
	}
	initial, err := a.initialSession(p)
	if err != nil {
		return err
	}
	addr := p.get("addr")
	if addr == "" {
		addr = "127.0.0.1:7878"
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	h := web.Handler(a.st, web.Options{InitialSession: initial, AnyHost: !web.IsLoopback(addr)})
	host, port, _ := net.SplitHostPort(ln.Addr().String())
	ip := net.ParseIP(host)
	query := ""
	if initial != 0 {
		query = fmt.Sprintf("?session=%d", initial)
	}
	urlFor := func(h string) string { return "http://" + net.JoinHostPort(h, port) + "/" + query }
	u := urlFor(host)
	if ip == nil || ip.IsUnspecified() || ip.IsLoopback() {
		u = urlFor("localhost")
	}
	fmt.Fprintf(a.out, "aitodo web: %s  (Ctrl+C で終了)\n", u)
	if ip != nil && ip.IsUnspecified() {
		// 0.0.0.0 などで待ち受けたときは、スマホなど同じネットワークの端末から開ける URL も出す
		for _, lan := range lanAddrs() {
			fmt.Fprintf(a.out, "            %s\n", urlFor(lan))
		}
	}
	if !web.IsLoopback(addr) {
		fmt.Fprintln(os.Stderr, "aitodo web: 警告: ループバック以外で待ち受けています。認証は無いので信頼できるネットワークでのみ使ってください")
	}
	if p.bools["open"] {
		openBrowser(u)
	}
	return http.Serve(ln, h)
}

// lanAddrs はこのマシンのループバック以外の IPv4 アドレス。
func lanAddrs() []string {
	var out []string
	addrs, _ := net.InterfaceAddrs()
	for _, a := range addrs {
		if n, ok := a.(*net.IPNet); ok && !n.IP.IsLoopback() && n.IP.To4() != nil && !n.IP.IsLinkLocalUnicast() {
			out = append(out, n.IP.String())
		}
	}
	return out
}

func openBrowser(u string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", u)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", u)
	default:
		cmd = exec.Command("xdg-open", u)
	}
	_ = cmd.Start()
}

// ---------- session ----------

func (a *app) cmdSession(args []string) error {
	if len(args) == 0 {
		return a.sessionList(nil)
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "add", "new", "create":
		return a.sessionAdd(rest)
	case "list", "ls":
		return a.sessionList(rest)
	case "show", "get":
		return a.sessionShow(rest)
	case "edit", "update", "set":
		return a.sessionEdit(rest, nil)
	case "archive":
		st := store.SessionArchived
		return a.sessionEdit(rest, &st)
	case "unarchive", "restore":
		st := store.SessionActive
		return a.sessionEdit(rest, &st)
	case "rm", "delete", "remove":
		return a.sessionRm(rest)
	case "current", "where":
		return a.sessionCurrent(rest)
	}
	return fmt.Errorf("unknown session subcommand %q", sub)
}

func (a *app) sessionAdd(args []string) error {
	p, err := parseArgs(args, flagSpec{value: []string{"d=dir", "workdir", "desc", "description"}})
	if err != nil {
		return err
	}
	if len(p.pos) == 0 {
		return errors.New("usage: aitodo session add NAME [--dir PATH] [--desc TEXT]")
	}
	dir := p.get("dir")
	if p.has("workdir") {
		dir = p.get("workdir")
	}
	desc := p.get("desc") + p.get("description")
	s, err := a.st.CreateSession(strings.Join(p.pos, " "), desc, dir)
	if err != nil {
		return err
	}
	return a.emit(s, func() { a.printSession(s) })
}

func (a *app) sessionList(args []string) error {
	p, err := parseArgs(args, flagSpec{bools: []string{"a=all"}})
	if err != nil {
		return err
	}
	ss, err := a.st.ListSessions(p.bools["all"])
	if err != nil {
		return err
	}
	return a.emit(ss, func() {
		if len(ss) == 0 {
			fmt.Fprintln(a.out, "(no sessions)")
		}
		for i := range ss {
			a.printSession(&ss[i])
		}
	})
}

func (a *app) sessionRef(p *parsed) (*store.Session, error) {
	ref := p.get("session")
	if len(p.pos) > 0 {
		ref = strings.Join(p.pos, " ")
	}
	return a.resolveSession(ref)
}

func (a *app) sessionShow(args []string) error {
	p, err := parseArgs(args, flagSpec{value: []string{"s=session"}})
	if err != nil {
		return err
	}
	s, err := a.sessionRef(p)
	if err != nil {
		return err
	}
	ts, err := a.st.ListTasks(s.ID, nil)
	if err != nil {
		return err
	}
	return a.emit(map[string]any{"session": s, "tasks": ts}, func() {
		a.printSession(s)
		if s.Description != "" {
			fmt.Fprintln(a.out, indent(s.Description))
		}
		fmt.Fprintln(a.out)
		for i := range ts {
			a.printTask(&ts[i])
		}
	})
}

func (a *app) sessionEdit(args []string, status *string) error {
	p, err := parseArgs(args, flagSpec{value: []string{"s=session", "name", "d=dir", "workdir", "desc", "description"}})
	if err != nil {
		return err
	}
	s, err := a.sessionRef(p)
	if err != nil {
		return err
	}
	patch := store.SessionPatch{Name: p.ptr("name"), Workdir: p.ptr("dir"), Description: p.ptr("desc"), Status: status}
	if p.has("workdir") {
		patch.Workdir = p.ptr("workdir")
	}
	if p.has("description") {
		patch.Description = p.ptr("description")
	}
	s, err = a.st.UpdateSession(s.ID, patch)
	if err != nil {
		return err
	}
	return a.emit(s, func() { a.printSession(s) })
}

func (a *app) sessionRm(args []string) error {
	p, err := parseArgs(args, flagSpec{})
	if err != nil {
		return err
	}
	if len(p.pos) == 0 {
		return errors.New("usage: aitodo session rm REF (REF is required for safety)")
	}
	s, err := a.st.FindSession(strings.Join(p.pos, " "))
	if err != nil {
		return err
	}
	if err := a.st.DeleteSession(s.ID); err != nil {
		return err
	}
	return a.emit(map[string]any{"deleted_session": s.ID}, func() {
		fmt.Fprintf(a.out, "deleted session #%d %s (%d tasks)\n", s.ID, s.Name, s.Total)
	})
}

func (a *app) sessionCurrent(args []string) error {
	p, err := parseArgs(args, flagSpec{value: []string{"d=dir"}})
	if err != nil {
		return err
	}
	dir := p.get("dir")
	if dir == "" {
		if dir, err = os.Getwd(); err != nil {
			return err
		}
	}
	s, err := a.st.ResolveByDir(dir)
	if err != nil {
		return err
	}
	return a.emit(s, func() { a.printSession(s) })
}

func (a *app) cmdStatus(args []string) error {
	p, err := parseArgs(args, flagSpec{value: []string{"s=session"}})
	if err != nil {
		return err
	}
	s, err := a.sessionRef(p)
	if err != nil {
		return err
	}
	next, err := a.st.NextTask(s.ID, false, false)
	if err != nil {
		return err
	}
	return a.emit(map[string]any{"session": s, "next": next, "remaining": s.Total - s.Done}, func() {
		a.printSession(s)
		fmt.Fprintf(a.out, "remaining: %d\n", s.Total-s.Done)
		if next != nil {
			fmt.Fprint(a.out, "next: ")
			a.printTask(next)
		} else {
			fmt.Fprintln(a.out, "next: (none — all tasks finished)")
		}
	})
}

// ---------- task ----------

func (a *app) cmdTask(args []string) error {
	if len(args) == 0 {
		return a.taskList(nil)
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "add", "new", "create":
		return a.taskAdd(rest)
	case "import", "bulk":
		return a.taskImport(rest)
	case "list", "ls":
		return a.taskList(rest)
	case "show", "get":
		return a.taskShow(rest)
	case "next":
		return a.taskNext(rest)
	case "start", "doing":
		return a.taskStatus(store.StatusDoing, rest)
	case "done", "check", "complete":
		return a.taskStatus(store.StatusDone, rest)
	case "skip":
		return a.taskStatus(store.StatusSkipped, rest)
	case "block":
		return a.taskStatus(store.StatusBlocked, rest)
	case "reopen", "undo", "todo", "uncheck":
		return a.taskStatus(store.StatusTodo, rest)
	case "set-status", "status":
		if len(rest) < 1 {
			return errors.New("usage: aitodo task status STATUS ID [--note TEXT]")
		}
		return a.taskStatus(rest[0], rest[1:])
	case "edit", "update":
		return a.taskEdit(rest)
	case "move", "mv":
		return a.taskMove(rest)
	case "rm", "delete", "remove":
		return a.taskRm(rest)
	case "comment", "comments":
		return a.cmdComment(rest)
	case "sub", "subtask":
		return a.subAdd(rest)
	}
	return fmt.Errorf("unknown task subcommand %q", sub)
}

func (a *app) taskAdd(args []string) error {
	p, err := parseArgs(args, flagSpec{value: []string{"s=session", "b=body", "p=parent"}})
	if err != nil {
		return err
	}
	if len(p.pos) == 0 {
		return errors.New("usage: aitodo task add [-s REF] TITLE [--body TEXT] [--parent ID]")
	}
	if p.has("parent") {
		pid, err := parseID(p.get("parent"))
		if err != nil {
			return err
		}
		t, err := a.st.AddSubtask(pid, strings.Join(p.pos, " "), p.get("body"))
		if err != nil {
			return err
		}
		return a.emit(t, func() { a.printTask(t) })
	}
	s, err := a.resolveSession(p.get("session"))
	if err != nil {
		return err
	}
	t, err := a.st.AddTask(s.ID, strings.Join(p.pos, " "), p.get("body"))
	if err != nil {
		return err
	}
	return a.emit(t, func() { a.printTask(t) })
}

// subAdd は `aitodo sub PARENT_ID TITLE...`。
func (a *app) subAdd(args []string) error {
	p, err := parseArgs(args, flagSpec{value: []string{"b=body"}})
	if err != nil {
		return err
	}
	if len(p.pos) < 2 {
		return errors.New("usage: aitodo sub PARENT_ID TITLE [--body TEXT]")
	}
	pid, err := parseID(p.pos[0])
	if err != nil {
		return err
	}
	t, err := a.st.AddSubtask(pid, strings.Join(p.pos[1:], " "), p.get("body"))
	if err != nil {
		return err
	}
	return a.emit(t, func() { a.printTask(t) })
}

var bulletRe = regexp.MustCompile(`^\s*(?:[-*+]\s+(?:\[[ xX]\]\s+)?|\d+[.)]\s+)`)

// ParseImport はテキスト（1 行 1 タスク、インデント行は直前タスクの body）または JSON を解釈する。
func ParseImport(data []byte) ([]store.NewTask, error) {
	trim := strings.TrimSpace(string(data))
	if strings.HasPrefix(trim, "[") {
		var raw []json.RawMessage
		if err := json.Unmarshal([]byte(trim), &raw); err != nil {
			return nil, fmt.Errorf("invalid JSON: %w", err)
		}
		out := []store.NewTask{}
		for _, r := range raw {
			var s string
			if json.Unmarshal(r, &s) == nil {
				out = append(out, store.NewTask{Title: s})
				continue
			}
			var nt store.NewTask
			if err := json.Unmarshal(r, &nt); err != nil {
				return nil, fmt.Errorf("invalid task item: %s", r)
			}
			out = append(out, nt)
		}
		return out, nil
	}
	type node struct {
		task   store.NewTask
		kids   []*node
		indent int
	}
	var roots []*node
	var stack []*node // 現在の祖先（インデントの浅い順）
	sc := bufio.NewScanner(strings.NewReader(string(data)))
	sc.Buffer(make([]byte, 1024*1024), 1024*1024)
	for sc.Scan() {
		line := strings.ReplaceAll(sc.Text(), "\t", "    ")
		trim := strings.TrimSpace(line)
		if trim == "" || strings.HasPrefix(trim, "#") {
			continue
		}
		ind := len(line) - len(strings.TrimLeft(line, " "))
		isBullet := bulletRe.MatchString(line)
		if ind >= 2 && !isBullet && len(stack) > 0 {
			// インデントされた地の文 → 直前のタスクの本文
			last := stack[len(stack)-1]
			if last.task.Body != "" {
				last.task.Body += "\n"
			}
			last.task.Body += trim
			continue
		}
		n := &node{task: store.NewTask{Title: strings.TrimSpace(bulletRe.ReplaceAllString(line, ""))}, indent: ind}
		for len(stack) > 0 && stack[len(stack)-1].indent >= ind {
			stack = stack[:len(stack)-1]
		}
		if len(stack) == 0 {
			roots = append(roots, n)
		} else {
			parent := stack[len(stack)-1]
			parent.kids = append(parent.kids, n)
		}
		stack = append(stack, n)
	}
	var build func([]*node) []store.NewTask
	build = func(ns []*node) []store.NewTask {
		out := []store.NewTask{}
		for _, n := range ns {
			t := n.task
			if len(n.kids) > 0 {
				t.Subtasks = build(n.kids)
			}
			out = append(out, t)
		}
		return out
	}
	return build(roots), sc.Err()
}

func (a *app) taskImport(args []string) error {
	p, err := parseArgs(args, flagSpec{value: []string{"s=session", "f=file", "p=parent"}})
	if err != nil {
		return err
	}
	var s *store.Session
	if !p.has("parent") {
		if s, err = a.resolveSession(p.get("session")); err != nil {
			return err
		}
	}
	var data []byte
	if f := p.get("file"); f != "" && f != "-" {
		data, err = os.ReadFile(f)
	} else {
		data, err = io.ReadAll(a.in)
	}
	if err != nil {
		return err
	}
	items, err := ParseImport(data)
	if err != nil {
		return err
	}
	var ts []store.Task
	if p.has("parent") {
		pid, err := parseID(p.get("parent"))
		if err != nil {
			return err
		}
		ts, err = a.st.AddSubtasks(pid, items)
		if err != nil {
			return err
		}
	} else if ts, err = a.st.AddTasks(s.ID, items); err != nil {
		return err
	}
	return a.emit(ts, func() {
		for i := range ts {
			a.printTask(&ts[i])
		}
	})
}

func (a *app) taskList(args []string) error {
	p, err := parseArgs(args, flagSpec{value: []string{"s=session", "status"}, bools: []string{"a=all", "pending"}})
	if err != nil {
		return err
	}
	ref := p.get("session")
	if ref == "" && len(p.pos) > 0 {
		ref = strings.Join(p.pos, " ")
	}
	s, err := a.resolveSession(ref)
	if err != nil {
		return err
	}
	var statuses []string
	if p.has("status") {
		for _, x := range strings.Split(p.get("status"), ",") {
			if x = strings.TrimSpace(x); x != "" {
				statuses = append(statuses, x)
			}
		}
	}
	if p.bools["pending"] {
		statuses = []string{store.StatusTodo, store.StatusDoing, store.StatusBlocked}
	}
	ts, err := a.st.ListTasks(s.ID, statuses)
	if err != nil {
		return err
	}
	return a.emit(ts, func() {
		fmt.Fprintf(a.out, "# %s (%d/%d done)\n", s.Name, s.Done, s.Total)
		for i := range ts {
			a.printTask(&ts[i])
		}
	})
}

func (a *app) taskShow(args []string) error {
	p, err := parseArgs(args, flagSpec{})
	if err != nil {
		return err
	}
	ref, err := needOne(p, "task ID")
	if err != nil {
		return err
	}
	id, err := parseID(ref)
	if err != nil {
		return err
	}
	d, err := a.st.GetTaskDetail(id)
	if err != nil {
		return err
	}
	return a.emit(d, func() { a.printTaskDetail(d) })
}

func (a *app) taskNext(args []string) error {
	p, err := parseArgs(args, flagSpec{value: []string{"s=session"}, bools: []string{"c=claim", "fresh"}})
	if err != nil {
		return err
	}
	s, err := a.resolveSession(p.get("session"))
	if err != nil {
		return err
	}
	t, err := a.st.NextTask(s.ID, p.bools["claim"], p.bools["fresh"])
	if err != nil {
		return err
	}
	if t == nil {
		return a.emit(nil, func() { fmt.Fprintln(a.out, "(no remaining tasks)") })
	}
	// 過去のコメントやサブタスクも文脈として一緒に返す
	d, err := a.st.GetTaskDetail(t.ID)
	if err != nil {
		return err
	}
	return a.emit(d, func() { a.printTaskDetail(d) })
}

func (a *app) taskStatus(status string, args []string) error {
	p, err := parseArgs(args, flagSpec{value: []string{"n=note", "m=message", "c=comment", "author"}})
	if err != nil {
		return err
	}
	if len(p.pos) == 0 {
		return fmt.Errorf("usage: aitodo task %s ID... [--note TEXT] [--comment TEXT]", status)
	}
	note := p.ptr("note")
	if p.has("message") {
		note = p.ptr("message")
	}
	var out []*store.Task
	for _, ref := range p.pos {
		id, err := parseID(ref)
		if err != nil {
			return err
		}
		t, err := a.st.SetStatus(id, status, note)
		if err != nil {
			return err
		}
		if p.has("comment") {
			if _, err := a.st.AddComment(id, a.author(p), p.get("comment")); err != nil {
				return err
			}
			if t, err = a.st.GetTask(id); err != nil {
				return err
			}
		}
		out = append(out, t)
	}
	var v any = out
	if len(out) == 1 {
		v = out[0]
	}
	return a.emit(v, func() {
		for _, t := range out {
			a.printTask(t)
		}
	})
}

func (a *app) taskEdit(args []string) error {
	p, err := parseArgs(args, flagSpec{value: []string{"title", "b=body", "n=note", "p=parent"}})
	if err != nil {
		return err
	}
	ref, err := needOne(p, "task ID")
	if err != nil {
		return err
	}
	id, err := parseID(ref)
	if err != nil {
		return err
	}
	patch := store.TaskPatch{Title: p.ptr("title"), Body: p.ptr("body"), Note: p.ptr("note")}
	if p.has("parent") {
		var pid int64
		if v := p.get("parent"); v != "0" && v != "" && v != "none" {
			if pid, err = parseID(v); err != nil {
				return err
			}
		}
		patch.ParentID = &pid
	}
	if _, err := a.st.UpdateTask(id, patch); err != nil {
		return err
	}
	d, err := a.st.GetTaskDetail(id)
	if err != nil {
		return err
	}
	return a.emit(d, func() { a.printTaskDetail(d) })
}

func (a *app) taskMove(args []string) error {
	p, err := parseArgs(args, flagSpec{value: []string{"to"}, bools: []string{"up", "down", "top", "bottom"}})
	if err != nil {
		return err
	}
	ref, err := needOne(p, "task ID")
	if err != nil {
		return err
	}
	id, err := parseID(ref)
	if err != nil {
		return err
	}
	idx, err := a.st.TaskIndex(id)
	if err != nil {
		return err
	}
	switch {
	case p.has("to"):
		n, err := strconv.Atoi(p.get("to"))
		if err != nil {
			return fmt.Errorf("invalid --to %q", p.get("to"))
		}
		idx = n
	case p.bools["up"]:
		idx--
	case p.bools["down"]:
		idx++
	case p.bools["top"]:
		idx = 0
	case p.bools["bottom"]:
		idx = 1 << 30
	default:
		return errors.New("specify --up, --down, --top, --bottom or --to INDEX")
	}
	t, err := a.st.MoveTask(id, idx)
	if err != nil {
		return err
	}
	return a.emit(t, func() { a.printTask(t) })
}

func (a *app) taskRm(args []string) error {
	p, err := parseArgs(args, flagSpec{})
	if err != nil {
		return err
	}
	if len(p.pos) == 0 {
		return errors.New("usage: aitodo task rm ID...")
	}
	ids := []int64{}
	for _, ref := range p.pos {
		id, err := parseID(ref)
		if err != nil {
			return err
		}
		if err := a.st.DeleteTask(id); err != nil {
			return err
		}
		ids = append(ids, id)
	}
	return a.emit(map[string]any{"deleted_tasks": ids}, func() {
		for _, id := range ids {
			fmt.Fprintf(a.out, "deleted task #%d\n", id)
		}
	})
}

func isTerminal(f *os.File) bool {
	fi, err := f.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}

// ---------- comment ----------

func (a *app) author(p *parsed) string {
	if v := p.get("author"); v != "" {
		return v
	}
	if v := os.Getenv("AITODO_AUTHOR"); v != "" {
		return v
	}
	return "ai"
}

func (a *app) cmdComment(args []string) error {
	if len(args) > 0 {
		switch args[0] {
		case "list", "ls":
			return a.commentList(args[1:])
		case "rm", "delete", "remove":
			return a.commentRm(args[1:])
		case "add":
			args = args[1:]
		}
	}
	p, err := parseArgs(args, flagSpec{value: []string{"author"}})
	if err != nil {
		return err
	}
	if len(p.pos) == 1 { // ID だけならコメント一覧
		return a.commentList(args)
	}
	if len(p.pos) < 2 {
		return errors.New("usage: aitodo comment TASK_ID TEXT... [--author NAME]  (TEXT が - なら stdin)")
	}
	id, err := parseID(p.pos[0])
	if err != nil {
		return err
	}
	body := strings.Join(p.pos[1:], " ")
	if body == "-" {
		b, err := io.ReadAll(a.in)
		if err != nil {
			return err
		}
		body = string(b)
	}
	c, err := a.st.AddComment(id, a.author(p), body)
	if err != nil {
		return err
	}
	return a.emit(c, func() {
		fmt.Fprintf(a.out, "commented on #%d:\n", id)
		a.printComment(c)
	})
}

func (a *app) commentList(args []string) error {
	p, err := parseArgs(args, flagSpec{})
	if err != nil {
		return err
	}
	ref, err := needOne(p, "task ID")
	if err != nil {
		return err
	}
	id, err := parseID(ref)
	if err != nil {
		return err
	}
	if _, err := a.st.GetTask(id); err != nil {
		return err
	}
	cs, err := a.st.ListComments(id)
	if err != nil {
		return err
	}
	return a.emit(cs, func() {
		if len(cs) == 0 {
			fmt.Fprintln(a.out, "(no comments)")
		}
		for i := range cs {
			a.printComment(&cs[i])
		}
	})
}

func (a *app) commentRm(args []string) error {
	p, err := parseArgs(args, flagSpec{})
	if err != nil {
		return err
	}
	ref, err := needOne(p, "comment ID")
	if err != nil {
		return err
	}
	id, err := parseID(strings.TrimPrefix(ref, "c"))
	if err != nil {
		return err
	}
	if err := a.st.DeleteComment(id); err != nil {
		return err
	}
	return a.emit(map[string]any{"deleted_comment": id}, func() { fmt.Fprintf(a.out, "deleted comment c%d\n", id) })
}
