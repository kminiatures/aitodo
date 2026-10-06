// Package tui は aitodo のマウス対応ターミナル UI。
//
// 描画関数 render() が「行の配列」と「クリック可能領域」を同時に返すため、
// 描画とマウスのヒットテストが常に一致する。
package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/mattn/go-runewidth"

	"github.com/kminiatures/aitodo/internal/store"
)

const refreshInterval = 1500 * time.Millisecond

// ---------- styles ----------

var (
	stTitle    = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("0")).Background(lipgloss.Color("6"))
	stDim      = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	stBorder   = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	stBorderOn = lipgloss.NewStyle().Foreground(lipgloss.Color("6"))
	stHeadOn   = lipgloss.NewStyle().Foreground(lipgloss.Color("6")).Bold(true)
	stSel      = lipgloss.NewStyle().Reverse(true)
	stSelDim   = lipgloss.NewStyle().Background(lipgloss.Color("237"))
	stDone     = lipgloss.NewStyle().Foreground(lipgloss.Color("8")).Strikethrough(true)
	stDoing    = lipgloss.NewStyle().Foreground(lipgloss.Color("3")).Bold(true)
	stBlocked  = lipgloss.NewStyle().Foreground(lipgloss.Color("1"))
	stSkipped  = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	stGo       = lipgloss.NewStyle().Foreground(lipgloss.Color("5")).Bold(true)
	stButton   = lipgloss.NewStyle().Foreground(lipgloss.Color("15")).Background(lipgloss.Color("238"))
	stButtonOn = lipgloss.NewStyle().Foreground(lipgloss.Color("0")).Background(lipgloss.Color("6")).Bold(true)
	stErr      = lipgloss.NewStyle().Foreground(lipgloss.Color("1")).Bold(true)
	stOK       = lipgloss.NewStyle().Foreground(lipgloss.Color("2"))
	stLabel    = lipgloss.NewStyle().Foreground(lipgloss.Color("6"))
	stMenu     = lipgloss.NewStyle().Foreground(lipgloss.Color("15")).Background(lipgloss.Color("236"))
)

var marks = map[string]string{
	store.StatusTodo:    "[ ]",
	store.StatusDoing:   "[>]",
	store.StatusDone:    "[x]",
	store.StatusSkipped: "[-]",
	store.StatusBlocked: "[!]",
	store.StatusGo:      "[*]",
}

// ---------- model ----------

const (
	focusSessions = 0
	focusTasks    = 1
)

type regionKind int

const (
	rSession regionKind = iota
	rTask
	rCheckbox
	rFold
	rButton
	rFormField
	rFormButton
	rFormCandidate
	rSessionPane
	rTaskPane
	rMenuItem
)

type region struct {
	y, x0, x1 int // x1 は排他的
	kind      regionKind
	idx       int
	action    string
}

type model struct {
	st     *store.Store
	w, h   int
	focus  int
	msg    string
	msgErr bool

	sessions []store.Session
	tasks    []store.Task
	sIdx     int
	tIdx     int
	sOff     int
	tOff     int

	showArchived bool
	hideDone     bool
	wantSession  int64

	collapsed map[int64]bool     // 畳んでいるタスク（TUI 内だけの表示状態）
	parent    map[int64]int64    // タスク ID → 親 ID（現セッションの全タスク）
	tree      map[int64]treeInfo // 表示中タスクの子の有無など

	form *form
	view *taskView // タスク詳細（コメント全文）ビュー
	menu *ctxMenu  // 右クリックのコンテキストメニュー

	help    bool // キー操作のヘルプ（? で開閉）
	helpOff int  // ヘルプのスクロール位置

	lastClickAt time.Time
	lastClickY  int

	detailH    int  // ユーザーがドラッグで決めた詳細枠の高さ（0 = 既定）
	dragDetail bool // 詳細枠の上辺をドラッグ中
}

// treeInfo は表示中タスクの折りたたみに関する情報。
type treeInfo struct {
	hasKids        bool // 表示対象の子がある（開閉できる）
	doing, blocked int  // 畳んで隠れている子孫のうち着手中・ブロック中の数
	goes           int  // 同じく go の数
}

type tickMsg time.Time

func tick() tea.Cmd {
	return tea.Tick(refreshInterval, func(t time.Time) tea.Msg { return tickMsg(t) })
}

// Run は TUI を起動する。initialSession が 0 でなければそのセッションを選択した状態で開く。
func Run(st *store.Store, initialSession int64) error {
	m := newModel(st, initialSession)
	p := tea.NewProgram(m, tea.WithAltScreen(), tea.WithMouseAllMotion())
	_, err := p.Run()
	return err
}

func newModel(st *store.Store, initialSession int64) *model {
	m := &model{st: st, w: 100, h: 30, wantSession: initialSession, collapsed: map[int64]bool{}}
	if v, _ := st.GetSetting(settingDetailH); v != "" {
		m.detailH, _ = strconv.Atoi(v)
	}
	if initialSession != 0 {
		m.focus = focusTasks
	}
	m.reload()
	return m
}

func (m *model) Init() tea.Cmd { return tick() }

func (m *model) curSession() *store.Session {
	if m.sIdx >= 0 && m.sIdx < len(m.sessions) {
		return &m.sessions[m.sIdx]
	}
	return nil
}

func (m *model) curTask() *store.Task {
	if m.tIdx >= 0 && m.tIdx < len(m.tasks) {
		return &m.tasks[m.tIdx]
	}
	return nil
}

func (m *model) setMsg(s string)  { m.msg, m.msgErr = s, false }
func (m *model) setErr(err error) { m.msg, m.msgErr = err.Error(), true }

// reload は DB から再読み込みし、選択を ID ベースで保つ。
func (m *model) reload() {
	var selS, selT int64
	if s := m.curSession(); s != nil {
		selS = s.ID
	}
	if m.wantSession != 0 {
		selS, m.wantSession = m.wantSession, 0
	}
	if t := m.curTask(); t != nil {
		selT = t.ID
	}
	ss, err := m.st.ListSessions(m.showArchived)
	if err != nil {
		m.setErr(err)
		return
	}
	m.sessions = ss
	m.sIdx = clamp(m.sIdx, 0, len(ss)-1)
	for i := range ss {
		if ss[i].ID == selS {
			m.sIdx = i
		}
	}
	m.tasks = nil
	m.parent, m.tree = map[int64]int64{}, map[int64]treeInfo{}
	if s := m.curSession(); s != nil {
		ts, err := m.st.ListTasks(s.ID, nil)
		if err != nil {
			m.setErr(err)
			return
		}
		for _, t := range ts {
			if t.ParentID != nil {
				m.parent[t.ID] = *t.ParentID
			}
		}
		if m.hideDone {
			f := ts[:0]
			for _, t := range ts {
				if t.Status != store.StatusDone && t.Status != store.StatusSkipped {
					f = append(f, t)
				}
			}
			ts = f
		}
		m.tasks = m.fold(ts)
	}
	m.tIdx = clamp(m.tIdx, 0, len(m.tasks)-1)
	// 選択していたタスクが畳まれて隠れたら、見えている一番近い祖先を選ぶ
	for id, found := selT, false; id != 0 && !found; id = m.parent[id] {
		for i := range m.tasks {
			if m.tasks[i].ID == id {
				m.tIdx, found = i, true
			}
		}
	}
	// ここでは選択行へスクロールを戻さない（自動更新がホイールスクロールを打ち消さないように）
	g := m.geom()
	m.sOff = clamp(m.sOff, 0, max(0, len(m.sessions)-g.rows))
	m.tOff = clamp(m.tOff, 0, max(0, len(m.tasks)-g.rows))
}

// fold は畳んだタスクの子孫を取り除き、m.tree を作る。ts は木の順（親の直後に子孫）。
func (m *model) fold(ts []store.Task) []store.Task {
	vis := make([]store.Task, 0, len(ts))
	var owner int64 // 子孫を隠している畳んだタスク
	ownerDepth := -1
	for i, t := range ts {
		if ownerDepth >= 0 && t.Depth > ownerDepth {
			ti := m.tree[owner]
			switch t.Status {
			case store.StatusDoing:
				ti.doing++
			case store.StatusBlocked:
				ti.blocked++
			case store.StatusGo:
				ti.goes++
			}
			m.tree[owner] = ti
			continue
		}
		ownerDepth = -1
		if i+1 < len(ts) && ts[i+1].Depth > t.Depth {
			m.tree[t.ID] = treeInfo{hasKids: true}
			if m.collapsed[t.ID] {
				owner, ownerDepth = t.ID, t.Depth
			}
		}
		vis = append(vis, t)
	}
	return vis
}

// reveal は id の祖先をすべて開く。開いたものがあれば true。
func (m *model) reveal(id int64) bool {
	changed := false
	for p := m.parent[id]; p != 0; p = m.parent[p] {
		if m.collapsed[p] {
			delete(m.collapsed, p)
			changed = true
		}
	}
	return changed
}

// foldLeft は ← の動き: 開いた親を畳む → 親へ移る → セッション枠へ移る。
func (m *model) foldLeft() {
	t := m.curTask()
	if t == nil {
		m.focus = focusSessions
		return
	}
	if m.tree[t.ID].hasKids && !m.collapsed[t.ID] {
		m.collapsed[t.ID] = true
		m.reload()
		return
	}
	if p := m.parent[t.ID]; p != 0 {
		for i := range m.tasks {
			if m.tasks[i].ID == p {
				m.tIdx = i
				m.ensureVisible()
				return
			}
		}
	}
	m.focus = focusSessions
}

// foldRight は → の動き: 畳んだ親を開く → 最初の子へ移る。
func (m *model) foldRight() {
	t := m.curTask()
	if t == nil || !m.tree[t.ID].hasKids {
		return
	}
	if m.collapsed[t.ID] {
		delete(m.collapsed, t.ID)
		m.reload()
		return
	}
	m.tIdx++
	m.ensureVisible()
}

func clamp(v, lo, hi int) int {
	if hi < lo {
		return lo
	}
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// ---------- layout ----------

type geom struct {
	sw, tw     int // セッション / タスク枠の幅
	paneTop    int // 枠の上辺 y
	paneH      int // 枠の高さ（上下辺込み）
	rows       int // 枠内の行数
	detailTop  int
	detailH    int
	buttonsY   int
	helpY      int
	sx, tx     int // 各枠の左端 x
	innerWidth func(int) int
}

func (m *model) geom() geom {
	g := geom{}
	g.sw = clamp(m.w/3, 22, 40)
	if g.sw > m.w-20 {
		g.sw = m.w / 2
	}
	g.tw = m.w - g.sw
	g.sx, g.tx = 0, g.sw
	g.detailH = m.detailH
	if g.detailH == 0 {
		g.detailH = m.h / 3
	}
	g.detailH = clamp(g.detailH, minDetailH, m.maxDetailH())
	g.helpY = m.h - 1
	g.buttonsY = m.h - 2
	g.detailTop = g.buttonsY - g.detailH
	g.paneTop = 1
	g.paneH = g.detailTop - g.paneTop
	if g.paneH < 3 {
		g.paneH = 3
	}
	g.rows = g.paneH - 2
	return g
}

const minDetailH, minPaneH = 4, 5

const settingDetailH = "tui.detail_height"

// maxDetailH は上の枠を minPaneH 行残せる詳細枠の最大高さ（タイトル行・ボタン行・ヘルプ行を除く）。
func (m *model) maxDetailH() int { return max(minDetailH, m.h-3-minPaneH) }

// setDetailTop は詳細枠の上辺を y に合わせて高さを変える。
func (m *model) setDetailTop(y int) {
	m.detailH = clamp(m.h-2-y, minDetailH, m.maxDetailH())
	g := m.geom()
	m.sOff = clamp(m.sOff, 0, max(0, len(m.sessions)-g.rows))
	m.tOff = clamp(m.tOff, 0, max(0, len(m.tasks)-g.rows))
}

func (m *model) ensureVisible() {
	g := m.geom()
	adj := func(idx, off, n int) int {
		if idx < off {
			off = idx
		}
		if idx >= off+g.rows {
			off = idx - g.rows + 1
		}
		return clamp(off, 0, max(0, n-g.rows))
	}
	m.sOff = adj(m.sIdx, m.sOff, len(m.sessions))
	m.tOff = adj(m.tIdx, m.tOff, len(m.tasks))
}

// ---------- text helpers ----------

// fit は表示幅 w に切り詰め・右パディングする（全角文字対応）。
func fit(s string, w int) string {
	if w <= 0 {
		return ""
	}
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "\t", " ")
	if runewidth.StringWidth(s) > w {
		s = runewidth.Truncate(s, w, "…")
	}
	return runewidth.FillRight(s, w)
}

func wrap(s string, w int) []string {
	if w <= 0 {
		return nil
	}
	out := []string{}
	for _, para := range strings.Split(strings.TrimRight(s, "\n"), "\n") {
		para = strings.ReplaceAll(para, "\t", "  ")
		if para == "" {
			out = append(out, "")
			continue
		}
		for runewidth.StringWidth(para) > w {
			cut := runewidth.Truncate(para, w, "")
			if cut == "" {
				break
			}
			out = append(out, cut)
			para = para[len(cut):]
		}
		out = append(out, para)
	}
	return out
}

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

// boxTop は "┌ title ───┐" を幅 w で作る。
func boxTop(title string, w int, on bool) string {
	bs := stBorder
	hs := stDim
	if on {
		bs, hs = stBorderOn, stHeadOn
	}
	inner := w - 2
	t := ""
	if title != "" {
		t = " " + runewidth.Truncate(title, max(0, inner-3), "…") + " "
	}
	rest := inner - 1 - runewidth.StringWidth(t)
	if rest < 0 {
		rest = 0
	}
	return bs.Render("┌─") + hs.Render(t) + bs.Render(strings.Repeat("─", rest)+"┐")
}

func boxBottom(w int, on bool, right string) string {
	bs := stBorder
	if on {
		bs = stBorderOn
	}
	inner := w - 2
	r := ""
	if right != "" {
		r = " " + right + " "
	}
	rest := inner - runewidth.StringWidth(r) - 1
	if rest < 0 {
		rest, r = inner, ""
		return bs.Render("└" + strings.Repeat("─", rest) + "┘")
	}
	return bs.Render("└"+strings.Repeat("─", rest)) + stDim.Render(r) + bs.Render("─┘")
}

func side(on bool) string {
	if on {
		return stBorderOn.Render("│")
	}
	return stBorder.Render("│")
}

// ---------- render ----------

type button struct{ label, action string }

func (m *model) buttons() []button {
	if m.form != nil {
		return nil
	}
	if m.view != nil {
		return []button{{"+Comment", "comment"}, {"✓Done", "toggle-done"}, {"▶Start", "toggle-doing"}, {"+Sub", "new-subtask"}, {"Edit", "edit"}, {"Close", "close-view"}}
	}
	var bs []button
	if m.focus == focusTasks && m.curTask() != nil {
		bs = []button{{"+Task", "new-task"}, {"+Sub", "new-subtask"}, {"✓Done", "toggle-done"}, {"▶Start", "toggle-doing"},
			{"!Block", "toggle-blocked"}, {"*Go", "toggle-go"}, {"Comment", "comment"}, {"View", "view"}, {"Edit", "edit"},
			{"↑", "move-up"}, {"↓", "move-down"}, {"Delete", "delete"}}
	} else {
		bs = []button{{"+Session", "new-session"}, {"+Task", "new-task"}, {"Edit", "edit"}}
		if s := m.curSession(); s != nil {
			bs = append(bs, button{"Dir", "workdir"}, button{"Delete", "delete"})
			if s.Status == store.SessionArchived {
				bs = append(bs, button{"Unarchive", "archive"})
			} else {
				bs = append(bs, button{"Archive", "archive"})
			}
		}
	}
	hd := "HideDone"
	if m.hideDone {
		hd = "ShowDone"
	}
	return append(bs, button{hd, "hide-done"}, button{"Quit", "quit"})
}

const minW, minH = 40, 12

func (m *model) tooSmall() bool { return m.w < minW || m.h < minH }

func (m *model) render() ([]string, []region) {
	if m.tooSmall() {
		lines := make([]string, max(1, m.h))
		lines[0] = fit(fmt.Sprintf("aitodo: terminal too small (%dx%d, need %dx%d)", m.w, m.h, minW, minH), max(1, m.w))
		return lines, nil
	}
	g := m.geom()
	lines := make([]string, m.h)
	var regs []region

	// title bar
	title := stTitle.Render(" aitodo ")
	info := fmt.Sprintf(" %d sessions  db: %s", len(m.sessions), tildify(m.st.Path))
	if m.showArchived {
		info += "  [archived shown]"
	}
	if m.hideDone {
		info += "  [done hidden]"
	}
	lines[0] = title + stDim.Render(fit(info, m.w-runewidth.StringWidth(" aitodo ")))

	if m.form != nil {
		fl, fr := m.renderForm(g)
		for i, l := range fl {
			if 1+i < g.buttonsY {
				lines[1+i] = l
			}
		}
		for _, r := range fr {
			r.y += 1
			regs = append(regs, r)
		}
	} else if m.view != nil {
		m.renderView(g, lines, &regs)
	} else {
		m.renderPanes(g, lines, &regs)
		m.renderDetail(g, lines)
	}

	// buttons
	x := 0
	var bl strings.Builder
	for _, b := range m.buttons() {
		lbl := " " + b.label + " "
		w := runewidth.StringWidth(lbl)
		if x+w > m.w {
			break
		}
		bl.WriteString(stButton.Render(lbl))
		regs = append(regs, region{y: g.buttonsY, x0: x, x1: x + w, kind: rButton, action: b.action})
		x += w
		if x < m.w {
			bl.WriteString(" ")
			x++
		}
	}
	lines[g.buttonsY] = bl.String()

	// help / message line
	var help string
	switch {
	case m.msg != "" && m.msgErr:
		help = stErr.Render(fit(" ✗ "+m.msg, m.w))
	case m.msg != "":
		help = stOK.Render(fit(" "+m.msg, m.w))
	case m.form != nil && m.form.onButton():
		help = stDim.Render(fit(" enter/space: 押す  ←→: ボタン切替  tab/shift+tab: 項目移動  ctrl+enter/ctrl+s: 保存  esc: キャンセル", m.w))
	case m.form != nil && len(m.form.fields) > 0 && m.form.fields[m.form.focus].dir:
		help = stDim.Render(fit(" tab: フォルダ補完（候補はクリックでも選択）  ↑↓/shift+tab: 項目移動  enter: 次/OK  ctrl+enter/ctrl+s: 保存  esc: キャンセル", m.w))
	case m.form != nil && len(m.form.fields) > 0 && m.form.fields[m.form.focus].multi:
		help = stDim.Render(fit(" enter: 改行  ctrl+enter/ctrl+s: 保存  tab/shift+tab: 項目移動  esc: キャンセル", m.w))
	case m.form != nil:
		help = stDim.Render(fit(" tab/↑↓: move field  enter: next/OK  ctrl+enter/ctrl+s: save  esc: cancel  (mouse: click field / buttons)", m.w))
	case m.view != nil:
		help = stDim.Render(fit(" ↑↓/jk/wheel scroll  c comment  space done  s start  A subtask  e edit  esc/v close  ? help", m.w))
	case m.focus == focusSessions:
		help = stDim.Render(fit(" ↑↓/jk move  tab/→ tasks  n new  e edit  w workdir  d delete  z archive  H show archived  ? help  q quit", m.w))
	default:
		help = stDim.Render(fit(" ? help  ↑↓/jk move  ←→ fold  [ ] fold all  space done  s start  b block  - skip  p go  a add  A sub  c comment  v view  e edit  J/K reorder  f hide done  q quit", m.w))
	}
	if m.menu != nil {
		help = stDim.Render(fit(" ↑↓/jk 選択  enter 実行  esc 閉じる  (右端のキーでも実行)", m.w))
	}
	if m.help {
		help = stDim.Render(fit(" ↑↓/jk/wheel スクロール  esc/?/q 閉じる", m.w))
	}
	lines[g.helpY] = help
	if m.menu != nil {
		m.renderMenu(lines, &regs)
	}
	if m.help {
		m.renderHelp(lines)
	}
	return lines, regs
}

func (m *model) renderPanes(g geom, lines []string, regs *[]region) {
	sOn, tOn := m.focus == focusSessions, m.focus == focusTasks
	sInner, tInner := g.sw-2, g.tw-2

	// sessions
	sTitle := "Sessions"
	lines[g.paneTop] = boxTop(sTitle, g.sw, sOn)
	for r := 0; r < g.rows; r++ {
		y := g.paneTop + 1 + r
		i := m.sOff + r
		cell := strings.Repeat(" ", max(0, sInner))
		if i < len(m.sessions) {
			s := m.sessions[i]
			cnt := fmt.Sprintf("%d/%d", s.Done, s.Total)
			name := s.Name
			if s.Status == store.SessionArchived {
				name = "▪ " + name
			}
			nameW := sInner - 2 - runewidth.StringWidth(cnt) - 1
			txt := " " + fit(name, nameW) + " " + cnt + " "
			txt = fit(txt, sInner)
			switch {
			case i == m.sIdx && sOn:
				cell = stSel.Render(txt)
			case i == m.sIdx:
				cell = stSelDim.Render(txt)
			case s.Status == store.SessionArchived:
				cell = stDim.Render(txt)
			case s.Total > 0 && s.Done == s.Total:
				cell = stOK.Render(txt)
			default:
				cell = txt
			}
			*regs = append(*regs, region{y: y, x0: g.sx + 1, x1: g.sx + 1 + sInner, kind: rSession, idx: i})
		} else if len(m.sessions) == 0 && r == 0 {
			cell = stDim.Render(fit(" (none) press n", sInner))
		}
		lines[y] = side(sOn) + cell + side(sOn)
	}
	sb := ""
	if len(m.sessions) > g.rows {
		sb = fmt.Sprintf("%d-%d/%d", m.sOff+1, min(len(m.sessions), m.sOff+g.rows), len(m.sessions))
	}
	lines[g.paneTop+g.paneH-1] = boxBottom(g.sw, sOn, sb)
	*regs = append(*regs, region{y: -1, x0: g.sx, x1: g.sx + g.sw, kind: rSessionPane})

	// tasks
	tTitle := "Tasks"
	if s := m.curSession(); s != nil {
		tTitle = fmt.Sprintf("%s  %d/%d", s.Name, s.Done, s.Total)
		if s.Workdir != "" {
			tTitle += "  " + tildify(s.Workdir)
		}
	}
	top := boxTop(tTitle, g.tw, tOn)
	lines[g.paneTop] += top
	for r := 0; r < g.rows; r++ {
		y := g.paneTop + 1 + r
		i := m.tOff + r
		cell := strings.Repeat(" ", max(0, tInner))
		if i < len(m.tasks) {
			t := m.tasks[i]
			mark := marks[t.Status]
			idS := fmt.Sprintf("#%d", t.ID)
			indW := min(2*t.Depth, tInner/2)
			ind := strings.Repeat(" ", indW)
			suffix := ""
			if t.SubtasksTotal > 0 {
				suffix += fmt.Sprintf("  %d/%d", t.SubtasksDone, t.SubtasksTotal)
			}
			if t.CommentCount > 0 {
				suffix += fmt.Sprintf("  ✎%d", t.CommentCount)
			}
			// 開閉マークはチェックボックス直前の空白に置く（子のない行は今までどおり）
			ti := m.tree[t.ID]
			fold := " "
			if ti.hasKids {
				fold = "▾"
				if m.collapsed[t.ID] {
					fold = "▸"
					if ti.doing > 0 {
						suffix += fmt.Sprintf("  >%d", ti.doing)
					}
					if ti.blocked > 0 {
						suffix += fmt.Sprintf("  !%d", ti.blocked)
					}
					if ti.goes > 0 {
						suffix += fmt.Sprintf("  *%d", ti.goes)
					}
				}
			}
			prefixW := 1 + indW + 3 + 1 + runewidth.StringWidth(idS) + 1
			rest := tInner - prefixW - runewidth.StringWidth(suffix)
			if rest < 4 { // 狭いときは付加情報を諦める
				suffix, rest = "", tInner-prefixW
			}
			title := fit(t.Title, rest)
			st := lipgloss.NewStyle()
			switch t.Status {
			case store.StatusDone:
				st = stDone
			case store.StatusDoing:
				st = stDoing
			case store.StatusBlocked:
				st = stBlocked
			case store.StatusSkipped:
				st = stSkipped
			case store.StatusGo:
				st = stGo
			}
			var txt string
			switch {
			case i == m.tIdx && tOn:
				txt = stSel.Render(ind + fold + mark + " " + idS + " " + title + suffix)
			case i == m.tIdx:
				txt = stSelDim.Render(ind + fold + mark + " " + idS + " " + title + suffix)
			default:
				txt = ind + fold + st.UnsetStrikethrough().Render(mark) + " " + stDim.Render(idS) + " " + st.Render(title) + stDim.Render(suffix)
			}
			cell = txt
			x0 := g.tx + 1
			cb := x0 + indW // " [x]" の開始位置（インデント分ずらす）
			*regs = append(*regs, region{y: y, x0: x0, x1: cb, kind: rTask, idx: i})
			if ti.hasKids {
				*regs = append(*regs,
					region{y: y, x0: cb, x1: cb + 1, kind: rFold, idx: i},
					region{y: y, x0: cb + 1, x1: cb + 4, kind: rCheckbox, idx: i})
			} else {
				*regs = append(*regs, region{y: y, x0: cb, x1: cb + 4, kind: rCheckbox, idx: i})
			}
			*regs = append(*regs, region{y: y, x0: cb + 4, x1: x0 + tInner, kind: rTask, idx: i})
		} else if r == 0 && len(m.tasks) == 0 {
			hint := " (no tasks) press a to add"
			if m.curSession() == nil {
				hint = " create a session first (n)"
			}
			cell = stDim.Render(fit(hint, tInner))
		}
		lines[y] += side(tOn) + cell + side(tOn)
	}
	tb := ""
	if len(m.tasks) > g.rows {
		tb = fmt.Sprintf("%d-%d/%d", m.tOff+1, min(len(m.tasks), m.tOff+g.rows), len(m.tasks))
	}
	lines[g.paneTop+g.paneH-1] += boxBottom(g.tw, tOn, tb)
	*regs = append(*regs, region{y: -1, x0: g.tx, x1: g.tx + g.tw, kind: rTaskPane})
}

func (m *model) renderDetail(g geom, lines []string) {
	inner := m.w - 2
	var title string
	var body []string
	if t := m.curTask(); m.focus == focusTasks && t != nil {
		title = fmt.Sprintf("Task #%d  %s", t.ID, t.Status)
		if t.SubtasksTotal > 0 {
			title += fmt.Sprintf("  subtasks %d/%d", t.SubtasksDone, t.SubtasksTotal)
		}
		if t.CommentCount > 0 {
			title += fmt.Sprintf("  ✎%d (v で全文)", t.CommentCount)
		}
		body = append(body, wrap(t.Title, inner-2)...)
		body = append(body, stDim.Render(TaskTimes(t, time.Now())))
		if t.Note != "" {
			for i, l := range wrap(t.Note, inner-4) {
				p := "  "
				if i == 0 {
					p = "→ "
				}
				body = append(body, stLabel.Render(p)+l)
			}
		}
		if t.CommentCount > 0 {
			// 最新のコメントを 1 行ずつ（最大 2 件）
			cs, _ := m.st.ListComments(t.ID)
			for _, c := range cs[max(0, len(cs)-2):] {
				first := strings.SplitN(strings.TrimSpace(c.Body), "\n", 2)[0]
				body = append(body, stLabel.Render("✎ "+c.Author+" "+shortTime(c.CreatedAt)+"  ")+first)
			}
		}
		if t.Body != "" {
			body = append(body, wrap(t.Body, inner-2)...)
		}
	} else if s := m.curSession(); s != nil {
		title = fmt.Sprintf("Session #%d  %s", s.ID, s.Status)
		wd := tildify(s.Workdir)
		if wd == "" {
			wd = "(未設定 — w で設定)"
		}
		body = append(body, stLabel.Render("name:    ")+s.Name, stLabel.Render("workdir: ")+wd)
		body = append(body, stLabel.Render("tasks:   ")+fmt.Sprintf("%d total, %d done, %d doing", s.Total, s.Done, s.Doing))
		body = append(body, stLabel.Render("time:    ")+"created "+shortTime(s.CreatedAt)+"   updated "+shortTime(s.UpdatedAt))
		if s.Description != "" {
			body = append(body, wrap(s.Description, inner-2)...)
		}
	} else {
		title = "Welcome"
		body = []string{"n: 新しいセッションを作成。AI からは `aitodo manual` を参照。"}
	}
	lines[g.detailTop] = boxTop(title, m.w, m.dragDetail)
	for r := 0; r < g.detailH-2; r++ {
		txt := ""
		if r < len(body) {
			txt = body[r]
		}
		if r == g.detailH-3 && len(body) > g.detailH-2 {
			txt = stDim.Render("…")
		}
		pad := inner - 1 - lipgloss.Width(txt)
		if pad < 0 {
			txt = fit(stripANSI(txt), inner-1)
			pad = 0
		}
		lines[g.detailTop+1+r] = side(false) + " " + txt + strings.Repeat(" ", pad) + side(false)
	}
	lines[g.detailTop+g.detailH-1] = boxBottom(m.w, false, "")
}

func stripANSI(s string) string {
	var b strings.Builder
	esc := false
	for _, r := range s {
		switch {
		case esc:
			if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') {
				esc = false
			}
		case r == 0x1b:
			esc = true
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

func shortTime(s string) string {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return s
	}
	return t.Local().Format("01-02 15:04")
}

// TaskTimes はタスクの作成・着手・完了日時と所要時間を 1 行にまとめる。
func TaskTimes(t *store.Task, now time.Time) string {
	s := "created " + shortTime(t.CreatedAt)
	if t.StartedAt != nil {
		s += "   started " + shortTime(*t.StartedAt)
	}
	if t.DoneAt != nil {
		s += "   " + t.Status + " " + shortTime(*t.DoneAt)
	}
	if d, ok := t.WorkTime(now); ok {
		if t.Status == store.StatusDoing {
			s += "   (elapsed " + FmtDuration(d) + ")"
		} else {
			s += "   (took " + FmtDuration(d) + ")"
		}
	}
	return s
}

// FmtDuration は 3d4h / 2h05m / 12m / <1m の形に丸める。
func FmtDuration(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "<1m"
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
	}
	return fmt.Sprintf("%dd%dh", int(d.Hours())/24, int(d.Hours())%24)
}

func (m *model) View() string {
	lines, _ := m.render()
	for i := range lines {
		if w := lipgloss.Width(lines[i]); w < m.w {
			lines[i] += strings.Repeat(" ", m.w-w)
		}
	}
	return strings.Join(lines, "\n")
}

// ---------- update ----------

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.w, m.h = msg.Width, msg.Height
		if m.form != nil {
			m.form.resize(m.w)
		}
		m.ensureVisible()
		return m, nil
	case tickMsg:
		m.reload()
		return m, tick()
	case tea.MouseMsg:
		cmd := m.mouse(msg)
		if !tea.MouseEvent(msg).IsWheel() {
			m.ensureVisible()
		}
		return m, cmd
	case tea.KeyMsg:
		if m.help {
			return m, m.helpKey(msg)
		}
		if m.menu != nil {
			cmd := m.menuKey(msg)
			m.ensureVisible()
			return m, cmd
		}
		if m.form != nil {
			return m, m.formKey(msg)
		}
		if m.view != nil {
			return m, m.viewKey(msg)
		}
		cmd := m.key(msg)
		m.ensureVisible()
		return m, cmd
	}
	if m.form != nil {
		if isCtrlEnterSeq(msg) {
			return m, m.formKey(tea.KeyMsg{Type: tea.KeyCtrlJ})
		}
		return m, m.form.updateFocused(msg)
	}
	return m, nil
}

func (m *model) key(k tea.KeyMsg) tea.Cmd {
	m.msg = ""
	switch k.String() {
	case "ctrl+c", "q":
		return tea.Quit
	case "tab", "shift+tab":
		m.focus = 1 - m.focus
	case "right", "l":
		if m.focus == focusTasks {
			m.foldRight()
		}
		m.focus = focusTasks
	case "left", "h":
		if m.focus == focusTasks {
			m.foldLeft()
		}
	case "[":
		return m.action("collapse-all")
	case "]":
		return m.action("expand-all")
	case "enter":
		if m.focus == focusSessions {
			m.focus = focusTasks
		} else {
			return m.action("edit")
		}
	case "up", "k":
		m.move(-1)
	case "down", "j":
		m.move(1)
	case "pgup", "ctrl+u":
		m.move(-m.geom().rows)
	case "pgdown", "ctrl+d":
		m.move(m.geom().rows)
	case "home", "g":
		m.move(-1 << 20)
	case "end", "G":
		m.move(1 << 20)
	case " ", "x":
		return m.action("toggle-done")
	case "s":
		return m.action("toggle-doing")
	case "b":
		return m.action("toggle-blocked")
	case "-":
		return m.action("toggle-skipped")
	case "p":
		return m.action("toggle-go")
	case "a":
		return m.action("new-task")
	case "A":
		return m.action("new-subtask")
	case "c":
		return m.action("comment")
	case "v", "o":
		return m.action("view")
	case "n", "N":
		return m.action("new-session")
	case "e":
		return m.action("edit")
	case "w":
		return m.action("workdir")
	case "d", "delete":
		return m.action("delete")
	case "z":
		return m.action("archive")
	case "H":
		return m.action("show-archived")
	case "f":
		return m.action("hide-done")
	case "K", "shift+up":
		return m.action("move-up")
	case "J", "shift+down":
		return m.action("move-down")
	case "r", "ctrl+r":
		m.reload()
		m.setMsg("reloaded")
	case "?":
		m.help, m.helpOff = true, 0
	}
	return nil
}

func (m *model) move(d int) {
	if m.focus == focusSessions {
		old := m.sIdx
		m.sIdx = clamp(m.sIdx+d, 0, len(m.sessions)-1)
		if old != m.sIdx {
			m.tIdx, m.tOff = 0, 0
			m.reload()
		}
	} else {
		m.tIdx = clamp(m.tIdx+d, 0, len(m.tasks)-1)
	}
	m.ensureVisible()
}

func (m *model) selectSession(i int) {
	if i != m.sIdx {
		m.sIdx = i
		m.tIdx, m.tOff = 0, 0
		m.reload()
	}
}

func (m *model) mouse(ev tea.MouseMsg) tea.Cmd {
	// ボタンを押していない移動はメニューのホバー以外では使わない（描画し直さずに済ませる）
	if ev.Action == tea.MouseActionMotion && !m.dragDetail && m.menu == nil {
		return nil
	}
	_, regs := m.render()
	g := m.geom()
	// 詳細枠の上辺ドラッグで高さを変更
	if m.dragDetail {
		switch ev.Action {
		case tea.MouseActionMotion:
			m.setDetailTop(ev.Y)
		case tea.MouseActionRelease:
			m.dragDetail = false
			if err := m.st.SetSetting(settingDetailH, strconv.Itoa(m.detailH)); err != nil {
				m.setErr(err)
			}
		}
		return nil
	}
	// メニュー表示中: マウスが乗った項目をハイライト
	if m.menu != nil && ev.Action == tea.MouseActionMotion {
		for _, r := range regs {
			if r.kind == rMenuItem && r.y == ev.Y && ev.X >= r.x0 && ev.X < r.x1 {
				m.menu.sel = r.idx
			}
		}
		return nil
	}
	if ev.Action != tea.MouseActionPress {
		return nil
	}
	// ヘルプ表示中: ホイールでスクロール、クリックで閉じる
	if m.help {
		switch ev.Button {
		case tea.MouseButtonWheelUp:
			m.helpOff = max(0, m.helpOff-3)
		case tea.MouseButtonWheelDown:
			m.helpOff += 3
		default:
			m.help = false
		}
		return nil
	}
	// メニュー表示中: 項目クリックで実行、それ以外のクリックで閉じる
	if m.menu != nil {
		if tea.MouseEvent(ev).IsWheel() {
			return nil
		}
		for _, r := range regs {
			if r.kind == rMenuItem && r.y == ev.Y && ev.X >= r.x0 && ev.X < r.x1 {
				m.menu.sel = r.idx
				return m.runMenu()
			}
		}
		m.menu = nil
		return nil
	}
	if ev.Button == tea.MouseButtonRight {
		m.rightClick(ev, regs, g)
		return nil
	}
	// ホイール: カーソル下の枠をスクロール
	if ev.Button == tea.MouseButtonWheelUp || ev.Button == tea.MouseButtonWheelDown {
		d := 3
		if ev.Button == tea.MouseButtonWheelUp {
			d = -3
		}
		if m.form != nil {
			return nil
		}
		if m.view != nil {
			m.view.off = max(0, m.view.off+d)
			return nil
		}
		for _, r := range regs {
			if r.y == -1 && ev.X >= r.x0 && ev.X < r.x1 && ev.Y >= g.paneTop && ev.Y < g.paneTop+g.paneH {
				if r.kind == rSessionPane {
					m.sOff = clamp(m.sOff+d, 0, max(0, len(m.sessions)-g.rows))
				} else {
					m.tOff = clamp(m.tOff+d, 0, max(0, len(m.tasks)-g.rows))
				}
			}
		}
		return nil
	}
	if ev.Button != tea.MouseButtonLeft {
		return nil
	}
	if m.form == nil && m.view == nil && !m.tooSmall() && ev.Y == g.detailTop {
		m.dragDetail = true
		return nil
	}
	double := time.Since(m.lastClickAt) < 400*time.Millisecond && m.lastClickY == ev.Y
	m.lastClickAt, m.lastClickY = time.Now(), ev.Y
	for _, r := range regs {
		if r.y != ev.Y || ev.X < r.x0 || ev.X >= r.x1 {
			continue
		}
		m.msg = ""
		switch r.kind {
		case rSession:
			m.focus = focusSessions
			m.selectSession(r.idx)
			if double {
				m.focus = focusTasks
			}
		case rCheckbox:
			m.focus = focusTasks
			m.tIdx = r.idx
			return m.action("toggle-done")
		case rFold:
			m.focus = focusTasks
			m.tIdx = r.idx
			return m.action("toggle-fold")
		case rTask:
			m.focus = focusTasks
			m.tIdx = r.idx
			if double {
				return m.action("edit")
			}
		case rButton:
			return m.action(r.action)
		case rFormField:
			m.form.setFocus(r.idx)
		case rFormCandidate:
			fl := m.form.fields[r.idx]
			m.form.setFocus(r.idx)
			fl.in.SetValue(fl.candBase + r.action + "/")
			fl.in.CursorEnd()
			fl.cands = nil
		case rFormButton:
			if r.action == "ok" {
				return m.submitForm()
			}
			m.form = nil
		}
		return nil
	}
	return nil
}

// ---------- actions ----------

// target は操作対象のタスク。詳細ビュー表示中はリストの選択ではなく表示中のタスク。
func (m *model) target() *store.Task {
	if m.view != nil {
		t, err := m.st.GetTask(m.view.taskID)
		if err != nil {
			return nil
		}
		return t
	}
	return m.curTask()
}

func (m *model) toggle(target string) {
	t := m.target()
	if t == nil || (m.focus != focusTasks && m.view == nil) {
		return
	}
	next := target
	if t.Status == target {
		next = store.StatusTodo
	}
	if _, err := m.st.SetStatus(t.ID, next, nil); err != nil {
		m.setErr(err)
		return
	}
	m.reload()
	m.setMsg(fmt.Sprintf("#%d → %s", t.ID, next))
}

func (m *model) action(a string) tea.Cmd {
	s, t := m.curSession(), m.target()
	switch a {
	case "quit":
		return tea.Quit
	case "toggle-done":
		m.toggle(store.StatusDone)
	case "toggle-doing":
		m.toggle(store.StatusDoing)
	case "toggle-blocked":
		m.toggle(store.StatusBlocked)
	case "toggle-skipped":
		m.toggle(store.StatusSkipped)
	case "toggle-go":
		m.toggle(store.StatusGo)
	case "new-session":
		cwd, _ := os.Getwd()
		m.openForm(&form{kind: "new-session", title: "新しいセッション", fields: []*field{
			newField("名前", "", "例: refactor-auth"),
			newDirField("作業フォルダ", tildify(cwd), "空欄可。tab で補完"),
			newMultiField("説明", "", "ゴールや背景（任意）"),
		}})
	case "new-subtask":
		if t == nil || (m.focus != focusTasks && m.view == nil) {
			m.setErr(fmt.Errorf("親にするタスクを選択してください"))
			return nil
		}
		m.openForm(&form{kind: "new-subtask", title: fmt.Sprintf("サブタスク追加 → #%d %s", t.ID, t.Title), id: t.ID, fields: []*field{
			newField("タイトル", "", "やること"),
			newMultiField("詳細", "", "任意"),
		}})
	case "comment":
		if t == nil || (m.focus != focusTasks && m.view == nil) {
			m.setErr(fmt.Errorf("コメントするタスクを選択してください"))
			return nil
		}
		m.openForm(&form{kind: "comment", title: fmt.Sprintf("コメント → #%d %s", t.ID, t.Title), id: t.ID, fields: []*field{
			newMultiField("コメント", "", "結果・指摘・質問など"),
		}})
	case "view":
		if t != nil && m.focus == focusTasks {
			m.view = &taskView{taskID: t.ID}
		}
	case "close-view":
		m.view = nil
	case "new-task":
		if s == nil {
			m.setErr(fmt.Errorf("先にセッションを作成してください (n)"))
			return nil
		}
		m.focus = focusTasks
		m.openForm(&form{kind: "new-task", title: "タスク追加 → " + s.Name, fields: []*field{
			newField("タイトル", "", "やること"),
			newMultiField("詳細", "", "任意"),
		}})
	case "edit":
		if (m.focus == focusTasks || m.view != nil) && t != nil {
			m.openForm(&form{kind: "edit-task", title: fmt.Sprintf("タスク #%d を編集", t.ID), id: t.ID, fields: []*field{
				newField("タイトル", t.Title, ""),
				newMultiField("詳細", t.Body, ""),
				newMultiField("メモ/結果", t.Note, ""),
			}})
		} else if s != nil {
			m.openForm(&form{kind: "edit-session", title: fmt.Sprintf("セッション #%d を編集", s.ID), id: s.ID, fields: []*field{
				newField("名前", s.Name, ""),
				newDirField("作業フォルダ", tildify(s.Workdir), "空欄で解除。tab で補完"),
				newMultiField("説明", s.Description, ""),
			}})
		}
	case "workdir":
		if s != nil {
			m.openForm(&form{kind: "workdir", title: "作業フォルダ → " + s.Name, id: s.ID, fields: []*field{
				newDirField("作業フォルダ", tildify(s.Workdir), "絶対パス / ~/... / 空欄で解除。tab で補完"),
			}})
		}
	case "delete":
		if m.focus == focusTasks && t != nil {
			extra := ""
			if t.SubtasksTotal > 0 {
				extra = fmt.Sprintf("（サブタスク %d 件も削除されます）", t.SubtasksTotal)
			}
			m.openForm(&form{kind: "delete-task", title: "削除の確認", id: t.ID,
				message: fmt.Sprintf("タスク #%d「%s」を削除しますか？%s (y/n)", t.ID, t.Title, extra)})
		} else if s != nil {
			m.openForm(&form{kind: "delete-session", title: "削除の確認", id: s.ID,
				message: fmt.Sprintf("セッション「%s」と %d 件のタスクを削除しますか？ (y/n)", s.Name, s.Total)})
		}
	case "archive":
		if s != nil {
			st := store.SessionArchived
			if s.Status == store.SessionArchived {
				st = store.SessionActive
			}
			if _, err := m.st.UpdateSession(s.ID, store.SessionPatch{Status: &st}); err != nil {
				m.setErr(err)
			} else {
				m.reload()
				m.setMsg(s.Name + " → " + st)
			}
		}
	case "show-archived":
		m.showArchived = !m.showArchived
		m.reload()
	case "hide-done":
		m.hideDone = !m.hideDone
		m.reload()
	case "toggle-fold":
		if t == nil || !m.tree[t.ID].hasKids {
			m.setErr(fmt.Errorf("サブタスクがありません"))
			return nil
		}
		m.collapsed[t.ID] = !m.collapsed[t.ID]
		m.reload()
	case "collapse-all":
		for _, p := range m.parent {
			m.collapsed[p] = true
		}
		m.reload()
	case "expand-all":
		clear(m.collapsed)
		m.reload()
	case "move-up", "move-down":
		if t == nil || m.hideDone {
			if m.hideDone {
				m.setErr(fmt.Errorf("完了を非表示中は並べ替えできません (f で解除)"))
			}
			return nil
		}
		idx, err := m.st.TaskIndex(t.ID)
		if err != nil {
			m.setErr(err)
			return nil
		}
		if a == "move-up" {
			idx--
		} else {
			idx++
		}
		if _, err := m.st.MoveTask(t.ID, idx); err != nil {
			m.setErr(err)
		}
		m.reload()
	}
	return nil
}

// ---------- forms ----------

type field struct {
	label string
	in    textinput.Model

	multi bool           // 複数行入力欄（enter で改行）
	ta    textarea.Model // multi のときに使う

	dir      bool     // フォルダ入力欄（tab で補完）
	cands    []string // 補完候補（曖昧なときに表示）
	candBase string   // 候補名の前に付く入力済み部分
}

// newDirField はフォルダ入力用の欄。tab で bash 風に補完する。
func newDirField(label, value, placeholder string) *field {
	f := newField(label, value, placeholder)
	f.dir = true
	return f
}

// complete はフォルダ欄の値を補完する。
func (f *field) complete() {
	cwd, _ := os.Getwd()
	v, base, cands := completeDir(f.in.Value(), cwd)
	f.in.SetValue(v)
	f.in.CursorEnd()
	f.cands, f.candBase = cands, base
}

func newField(label, value, placeholder string) *field {
	ti := textinput.New()
	ti.Prompt = "› "
	ti.Placeholder = placeholder
	ti.SetValue(value)
	ti.CharLimit = 0
	return &field{label: label, in: ti}
}

// multiFieldHeight は複数行入力欄の表示行数。
const multiFieldHeight = 3

// newMultiField は複数行入力用の欄。enter で改行する（ctrl+enter は保存）。
func newMultiField(label, value, placeholder string) *field {
	ta := textarea.New()
	ta.Prompt = "› "
	ta.Placeholder = placeholder
	ta.ShowLineNumbers = false
	ta.CharLimit = 0
	ta.FocusedStyle.CursorLine = lipgloss.NewStyle()
	ta.KeyMap.InsertNewline = key.NewBinding(key.WithKeys("enter", "ctrl+m"))
	ta.SetHeight(multiFieldHeight)
	ta.SetValue(value) // カーソルは末尾
	return &field{label: label, multi: true, ta: ta}
}

func (f *field) value() string {
	if f.multi {
		return f.ta.Value()
	}
	return f.in.Value()
}

func (f *field) view() string {
	if f.multi {
		return f.ta.View()
	}
	return f.in.View()
}

func (f *field) update(msg tea.Msg) tea.Cmd {
	var cmd tea.Cmd
	if f.multi {
		f.ta, cmd = f.ta.Update(msg)
	} else {
		f.in, cmd = f.in.Update(msg)
	}
	return cmd
}

// atTop / atBottom は複数行欄のカーソルが先頭行 / 最終行（折り返し含む）にあるか。
func (f *field) atTop() bool {
	return f.ta.Line() == 0 && f.ta.LineInfo().RowOffset == 0
}

func (f *field) atBottom() bool {
	li := f.ta.LineInfo()
	return f.ta.Line() == f.ta.LineCount()-1 && li.RowOffset >= li.Height-1
}

type form struct {
	kind    string
	title   string
	id      int64
	fields  []*field
	focus   int    // 0..len(fields)-1: 入力欄, len(fields): OK, len(fields)+1: Cancel
	message string // 確認ダイアログ用
}

// フォーカス位置としての OK / Cancel ボタン（入力欄の後ろに並ぶ）。
func (f *form) okIdx() int     { return len(f.fields) }
func (f *form) cancelIdx() int { return len(f.fields) + 1 }

// onButton はフォーカスが OK / Cancel ボタンにあるか。
func (f *form) onButton() bool { return f.focus >= len(f.fields) }

func (f *form) setFocus(i int) {
	n := len(f.fields) + 2
	f.focus = ((i % n) + n) % n
	for j, fl := range f.fields {
		switch {
		case fl.multi && j == f.focus:
			fl.ta.Focus()
		case fl.multi:
			fl.ta.Blur()
		case j == f.focus:
			fl.in.Focus()
			fl.in.CursorEnd()
		default:
			fl.in.Blur()
		}
	}
}

func (f *form) resize(w int) {
	for _, fl := range f.fields {
		if fl.multi {
			fl.ta.SetWidth(max(10, w-8))
		} else {
			fl.in.Width = max(10, w-10)
		}
	}
}

func (f *form) updateFocused(msg tea.Msg) tea.Cmd {
	if f.onButton() {
		return nil
	}
	return f.fields[f.focus].update(msg)
}

func (m *model) openForm(f *form) {
	m.form = f
	m.msg = ""
	f.resize(m.w)
	f.setFocus(0)
}

// ctrlEnter は ctrl+enter として扱うキー。bubbletea v1 は ctrl+enter を識別できないが、
// Windows Terminal などは ctrl+enter で LF を送るため ctrl+j として届く。
const ctrlEnter = "ctrl+j"

// isCtrlEnterSeq は modifyOtherKeys (CSI 27;5;13~) / CSI u (CSI 13;5u) 形式の ctrl+enter か。
// bubbletea v1 はこれらを非公開の unknownCSISequenceMsg として渡すので文字列表現で判定する。
func isCtrlEnterSeq(msg tea.Msg) bool {
	switch fmt.Sprint(msg) {
	case "?CSI[50 55 59 53 59 49 51 126]?", "?CSI[49 51 59 53 117]?":
		return true
	}
	return false
}

func (m *model) formKey(k tea.KeyMsg) tea.Cmd {
	f := m.form
	m.msg = ""
	key := k.String()
	if len(f.fields) == 0 { // 確認ダイアログ
		switch key {
		case "y", "Y":
			return m.submitForm()
		case "n", "N", "esc", "q", "ctrl+c":
			m.form = nil
		case "tab", "shift+tab", "left", "right", "h", "l":
			f.setFocus(f.focus + 1)
		case "enter", " ", ctrlEnter:
			return m.pressButton()
		}
		return nil
	}
	if f.onButton() {
		switch key {
		case "esc", "ctrl+c":
			m.form = nil
		case "tab", "down":
			f.setFocus(f.focus + 1)
		case "shift+tab", "up":
			f.setFocus(f.focus - 1)
		case "left", "right":
			f.setFocus(f.okIdx() + f.cancelIdx() - f.focus) // OK ⇔ Cancel
		case "ctrl+s", ctrlEnter:
			return m.submitForm()
		case "enter", " ":
			return m.pressButton()
		}
		return nil
	}
	fl := f.fields[f.focus]
	if fl.dir && key == "tab" {
		fl.complete()
		return nil
	}
	fl.cands = nil
	if fl.multi {
		switch {
		case key == "enter":
			return f.updateFocused(k) // 改行
		case key == "up" && !fl.atTop(), key == "down" && !fl.atBottom():
			return f.updateFocused(k) // 欄内でカーソル移動
		}
	}
	switch key {
	case "esc", "ctrl+c":
		m.form = nil
		return nil
	case "tab", "down":
		f.setFocus(f.focus + 1)
		return nil
	case "shift+tab", "up":
		f.setFocus(f.focus - 1)
		return nil
	case "ctrl+s", ctrlEnter:
		return m.submitForm()
	case "enter":
		if f.focus == len(f.fields)-1 {
			return m.submitForm()
		}
		f.setFocus(f.focus + 1)
		return nil
	}
	return f.updateFocused(k)
}

func (m *model) renderForm(g geom) ([]string, []region) {
	f := m.form
	w := m.w
	inner := w - 2
	var lines []string
	var regs []region
	lines = append(lines, boxTop(f.title, w, true))
	row := func(content string) {
		pad := inner - 1 - lipgloss.Width(content)
		if pad < 0 {
			content = fit(stripANSI(content), inner-1)
			pad = 0
		}
		lines = append(lines, side(true)+" "+content+strings.Repeat(" ", pad)+side(true))
	}
	row("")
	if f.message != "" {
		for _, l := range wrap(f.message, inner-2) {
			row(l)
		}
	}
	for i, fl := range f.fields {
		lbl := fl.label
		if i == f.focus {
			row(stHeadOn.Render(lbl))
		} else {
			row(stLabel.Render(lbl))
		}
		regs = append(regs, region{y: len(lines) - 1, x0: 0, x1: w, kind: rFormField, idx: i})
		for _, l := range strings.Split(fl.view(), "\n") {
			row(l)
			regs = append(regs, region{y: len(lines) - 1, x0: 0, x1: w, kind: rFormField, idx: i})
		}
		if len(fl.cands) > 0 {
			cl, cr := layoutCandidates(fl.cands, inner-2, 6)
			for _, l := range cl {
				row(l)
			}
			for _, r := range cr {
				r.y += len(lines) - len(cl)
				r.idx = i
				regs = append(regs, r)
			}
		}
		row("")
	}
	okLbl, cancelLbl := "  OK  ", " Cancel "
	if f.message != "" {
		okLbl, cancelLbl = " Yes (y) ", " No (n) "
	}
	btn := func(lbl string, idx int) string {
		if f.focus == idx {
			return stButtonOn.Render(lbl)
		}
		return stButton.Render(lbl)
	}
	row(btn(okLbl, f.okIdx()) + "  " + btn(cancelLbl, f.cancelIdx()))
	by := len(lines) - 1
	okW := runewidth.StringWidth(okLbl)
	regs = append(regs,
		region{y: by, x0: 2, x1: 2 + okW, kind: rFormButton, action: "ok"},
		region{y: by, x0: 2 + okW + 2, x1: 2 + okW + 2 + runewidth.StringWidth(cancelLbl), kind: rFormButton, action: "cancel"})
	lines = append(lines, boxBottom(w, true, ""))
	_ = g
	return lines, regs
}

// pressButton はフォーカス中のボタン（OK / Cancel）を押す。
func (m *model) pressButton() tea.Cmd {
	if m.form.focus == m.form.cancelIdx() {
		m.form = nil
		return nil
	}
	return m.submitForm()
}

func (m *model) submitForm() tea.Cmd {
	f := m.form
	val := func(i int) string { return strings.TrimSpace(f.fields[i].value()) }
	var err error
	switch f.kind {
	case "new-session":
		var s *store.Session
		s, err = m.st.CreateSession(val(0), val(2), val(1))
		if err == nil {
			m.wantSession = s.ID
			m.focus = focusTasks
			m.setMsg("created session " + s.Name)
		}
	case "edit-session":
		name, wd, desc := val(0), val(1), val(2)
		_, err = m.st.UpdateSession(f.id, store.SessionPatch{Name: &name, Workdir: &wd, Description: &desc})
	case "workdir":
		wd := val(0)
		_, err = m.st.UpdateSession(f.id, store.SessionPatch{Workdir: &wd})
		if err == nil {
			m.setMsg("workdir updated")
		}
	case "new-task":
		s := m.curSession()
		if s == nil {
			err = fmt.Errorf("no session selected")
			break
		}
		var t *store.Task
		t, err = m.st.AddTask(s.ID, val(0), val(1))
		if err == nil {
			m.reload()
			for i := range m.tasks {
				if m.tasks[i].ID == t.ID {
					m.tIdx = i
				}
			}
			m.setMsg(fmt.Sprintf("added #%d", t.ID))
		}
	case "new-subtask":
		var t *store.Task
		t, err = m.st.AddSubtask(f.id, val(0), val(1))
		if err == nil {
			m.form = nil
			m.reload()
			m.selectTask(t.ID)
			m.setMsg(fmt.Sprintf("added subtask #%d", t.ID))
			return nil
		}
	case "comment":
		_, err = m.st.AddComment(f.id, humanAuthor(), val(0))
		if err == nil {
			m.setMsg("comment added")
		}
	case "edit-task":
		title, body, note := val(0), val(1), val(2)
		_, err = m.st.UpdateTask(f.id, store.TaskPatch{Title: &title, Body: &body, Note: &note})
	case "delete-task":
		err = m.st.DeleteTask(f.id)
		if err == nil {
			m.setMsg(fmt.Sprintf("deleted #%d", f.id))
		}
	case "delete-session":
		err = m.st.DeleteSession(f.id)
		if err == nil {
			m.setMsg("session deleted")
		}
	}
	if err != nil {
		m.setErr(err)
		return nil // フォームは開いたまま
	}
	m.form = nil
	m.reload()
	return nil
}

// layoutCandidates は補完候補を横に並べて折り返す。返す region の y は先頭行からの相対値、
// x は枠内のコンテンツ開始位置（x=2）を基準にした絶対値。maxLines を超える分は件数だけ表示する。
func layoutCandidates(cands []string, width, maxLines int) ([]string, []region) {
	var lines []string
	var regs []region
	var cur strings.Builder
	x := 0
	for i, c := range cands {
		lbl := c + "/"
		if runewidth.StringWidth(lbl) > width {
			lbl = runewidth.Truncate(lbl, width, "…")
		}
		w := runewidth.StringWidth(lbl)
		if x > 0 && x+2+w > width {
			lines = append(lines, cur.String())
			cur.Reset()
			x = 0
			if len(lines) == maxLines-1 {
				lines = append(lines, stDim.Render(fmt.Sprintf("…他 %d 件（続けて入力すると絞り込めます）", len(cands)-i)))
				return lines, regs
			}
		}
		if x > 0 {
			cur.WriteString("  ")
			x += 2
		}
		cur.WriteString(stLabel.Render(lbl))
		regs = append(regs, region{y: len(lines), x0: 2 + x, x1: 2 + x + w, kind: rFormCandidate, action: c})
		x += w
	}
	if cur.Len() > 0 {
		lines = append(lines, cur.String())
	}
	return lines, regs
}

func (m *model) selectTask(id int64) {
	if m.reveal(id) {
		m.reload()
	}
	for i := range m.tasks {
		if m.tasks[i].ID == id {
			m.tIdx = i
		}
	}
	m.ensureVisible()
}

// humanAuthor は TUI から書くコメントの投稿者名。
func humanAuthor() string {
	if u := os.Getenv("USER"); u != "" {
		return u
	}
	return "human"
}

// ---------- task view（タスク詳細・コメント全文） ----------

type taskView struct {
	taskID int64
	off    int
}

func (m *model) viewLines(width int) (string, []string) {
	d, err := m.st.GetTaskDetail(m.view.taskID)
	if err != nil {
		return "Task", []string{stErr.Render(err.Error())}
	}
	t := &d.Task
	title := fmt.Sprintf("Task #%d  %s", t.ID, t.Status)
	var out []string
	add := func(ls ...string) { out = append(out, ls...) }
	add(stHeadOn.Render(fit(t.Title, width)))
	meta := TaskTimes(t, time.Now())
	if t.ParentID != nil {
		meta += fmt.Sprintf("   parent #%d", *t.ParentID)
	}
	add(stDim.Render(meta), "")
	if t.Body != "" {
		add(stLabel.Render("詳細"))
		add(wrap(t.Body, width)...)
		add("")
	}
	if t.Note != "" {
		add(stLabel.Render("結果メモ"))
		add(wrap(t.Note, width)...)
		add("")
	}
	if len(d.Subtasks) > 0 {
		add(stLabel.Render(fmt.Sprintf("サブタスク %d/%d", t.SubtasksDone, t.SubtasksTotal)))
		for _, c := range d.Subtasks {
			add(fit(fmt.Sprintf("  %s #%d %s", marks[c.Status], c.ID, c.Title), width))
		}
		add("")
	}
	add(stLabel.Render(fmt.Sprintf("コメント (%d)", len(d.Comments))))
	if len(d.Comments) == 0 {
		add(stDim.Render("  (なし) c で追加"))
	}
	for _, c := range d.Comments {
		add(stDoing.Render("✎ "+c.Author) + stDim.Render("  "+shortTime(c.CreatedAt)+fmt.Sprintf("  c%d", c.ID)))
		for _, l := range wrap(c.Body, width-2) {
			add("  " + l)
		}
		add("")
	}
	return title, out
}

func (m *model) renderView(g geom, lines []string, regs *[]region) {
	inner := m.w - 2
	top, bottom := 1, g.buttonsY-1 // 枠の上辺・下辺の y
	rows := bottom - top - 1
	title, body := m.viewLines(inner - 2)
	m.view.off = clamp(m.view.off, 0, max(0, len(body)-rows))
	lines[top] = boxTop(title, m.w, true)
	for r := 0; r < rows; r++ {
		txt := ""
		if i := m.view.off + r; i < len(body) {
			txt = body[i]
		}
		pad := inner - 1 - lipgloss.Width(txt)
		if pad < 0 {
			txt = fit(stripANSI(txt), inner-1)
			pad = 0
		}
		lines[top+1+r] = side(true) + " " + txt + strings.Repeat(" ", pad) + side(true)
	}
	pos := ""
	if len(body) > rows {
		pos = fmt.Sprintf("%d-%d/%d", m.view.off+1, min(len(body), m.view.off+rows), len(body))
	}
	lines[bottom] = boxBottom(m.w, true, pos)
}

func (m *model) viewKey(k tea.KeyMsg) tea.Cmd {
	m.msg = ""
	rows := max(1, m.geom().buttonsY-3)
	switch k.String() {
	case "?":
		m.help, m.helpOff = true, 0
	case "esc", "q", "v", "o", "enter":
		m.view = nil
	case "ctrl+c":
		return tea.Quit
	case "up", "k":
		m.view.off = max(0, m.view.off-1)
	case "down", "j":
		m.view.off++
	case "pgup", "ctrl+u":
		m.view.off = max(0, m.view.off-rows)
	case "pgdown", "ctrl+d":
		m.view.off += rows
	case " ", "x":
		return m.action("toggle-done")
	case "home", "g":
		m.view.off = 0
	case "end", "G":
		m.view.off = 1 << 20
	case "c":
		return m.action("comment")
	case "s":
		return m.action("toggle-doing")
	case "A":
		return m.action("new-subtask")
	case "e":
		return m.action("edit")
	}
	return nil
}

// ---------- context menu ----------

type menuItem struct{ label, key, action string }

type ctxMenu struct {
	x, y  int // クリック位置（描画時に画面内へ収める）
	items []menuItem
	sel   int
}

// rightClick はクリック位置の行を選択し、その対象用のメニューを開く。
func (m *model) rightClick(ev tea.MouseMsg, regs []region, g geom) {
	if m.form != nil || m.view != nil || m.tooSmall() {
		return
	}
	m.msg = ""
	var items []menuItem
	for _, r := range regs {
		if r.y != ev.Y || ev.X < r.x0 || ev.X >= r.x1 {
			continue
		}
		switch r.kind {
		case rSession:
			m.focus = focusSessions
			m.selectSession(r.idx)
			items = m.sessionMenu()
		case rTask, rCheckbox, rFold:
			m.focus = focusTasks
			m.tIdx = r.idx
			items = taskMenu
		}
	}
	if items == nil { // 行のない枠内の余白
		for _, r := range regs {
			if r.y == -1 && ev.X >= r.x0 && ev.X < r.x1 && ev.Y >= g.paneTop && ev.Y < g.paneTop+g.paneH {
				if r.kind == rSessionPane {
					m.focus = focusSessions
					items = []menuItem{{"+ セッション", "n", "new-session"}, {"アーカイブ表示切替", "H", "show-archived"}}
				} else {
					m.focus = focusTasks
					items = []menuItem{{"+ タスク", "a", "new-task"}, {"完了の表示切替", "f", "hide-done"},
						{"すべてたたむ", "[", "collapse-all"}, {"すべて開く", "]", "expand-all"}}
				}
			}
		}
	}
	if items != nil {
		m.menu = &ctxMenu{x: ev.X, y: ev.Y, items: items}
	}
}

var taskMenu = []menuItem{
	{"✓ 完了 / 戻す", "space", "toggle-done"},
	{"▶ 着手 / 戻す", "s", "toggle-doing"},
	{"! ブロック", "b", "toggle-blocked"},
	{"- スキップ", "-", "toggle-skipped"},
	{"* go（Claude に着手させる）", "p", "toggle-go"},
	{"+ サブタスク", "A", "new-subtask"},
	{"たたむ / 開く", "←→", "toggle-fold"},
	{"コメント", "c", "comment"},
	{"詳細を見る", "v", "view"},
	{"編集", "e", "edit"},
	{"上へ移動", "K", "move-up"},
	{"下へ移動", "J", "move-down"},
	{"削除", "d", "delete"},
}

func (m *model) sessionMenu() []menuItem {
	arch := "アーカイブ"
	if s := m.curSession(); s != nil && s.Status == store.SessionArchived {
		arch = "アーカイブ解除"
	}
	return []menuItem{
		{"+ タスク", "a", "new-task"},
		{"編集", "e", "edit"},
		{"作業フォルダ", "w", "workdir"},
		{arch, "z", "archive"},
		{"削除", "d", "delete"},
		{"+ セッション", "n", "new-session"},
	}
}

func (m *model) runMenu() tea.Cmd {
	a := m.menu.items[m.menu.sel].action
	m.menu = nil
	return m.action(a)
}

func (m *model) menuKey(k tea.KeyMsg) tea.Cmd {
	mn := m.menu
	switch ks := k.String(); ks {
	case "esc", "q":
		m.menu = nil
	case "ctrl+c":
		return tea.Quit
	case "up", "k":
		mn.sel = (mn.sel + len(mn.items) - 1) % len(mn.items)
	case "down", "j":
		mn.sel = (mn.sel + 1) % len(mn.items)
	case "enter":
		return m.runMenu()
	default:
		if ks == " " {
			ks = "space"
		}
		for i, it := range mn.items {
			if it.key == ks {
				mn.sel = i
				return m.runMenu()
			}
		}
	}
	return nil
}

// renderMenu はメニューを lines の上に重ねて描き、項目の領域を regs に足す。
func (m *model) renderMenu(lines []string, regs *[]region) {
	mn := m.menu
	lw, kw := 0, 0
	for _, it := range mn.items {
		lw = max(lw, runewidth.StringWidth(it.label))
		kw = max(kw, runewidth.StringWidth(it.key))
	}
	inner := 1 + lw + 3 + kw + 1
	w, h := inner+2, len(mn.items)+2
	x := clamp(mn.x, 0, max(0, m.w-w))
	y := clamp(mn.y, 1, max(1, len(lines)-h))
	put := func(row int, s string) {
		if row >= 0 && row < len(lines) {
			lines[row] = overlay(lines[row], x, s, w)
		}
	}
	bs := stBorderOn
	put(y, bs.Render("┌"+strings.Repeat("─", inner)+"┐"))
	for i, it := range mn.items {
		txt := " " + runewidth.FillRight(it.label, lw) + "   " + fmt.Sprintf("%*s", kw, it.key) + " "
		if i == mn.sel {
			txt = stSel.Render(txt)
		} else {
			txt = stMenu.Render(txt)
		}
		put(y+1+i, bs.Render("│")+txt+bs.Render("│"))
		*regs = append(*regs, region{y: y + 1 + i, x0: x + 1, x1: x + 1 + inner, kind: rMenuItem, idx: i})
	}
	put(y+h-1, bs.Render("└"+strings.Repeat("─", inner)+"┘"))
}

// overlay は装飾付きの行 line の表示位置 x から幅 w を s で置き換える（全角の途中で切れる場合は空白で埋める）。
func overlay(line string, x int, s string, w int) string {
	lw := ansi.StringWidth(line)
	if lw < x+w {
		line += strings.Repeat(" ", x+w-lw)
		lw = x + w
	}
	left := ansi.Truncate(line, x, "")
	left += strings.Repeat(" ", x-ansi.StringWidth(left))
	want := lw - (x + w)
	right := ansi.TruncateLeft(line, x+w, "")
	if ansi.StringWidth(right) > want { // 境界をまたぐ全角文字を落とす
		right = ansi.TruncateLeft(line, x+w+1, " ")
	}
	return left + "\x1b[0m" + s + "\x1b[0m" + right
}

// ---------- help ----------

type helpSec struct {
	title string
	rows  [][2]string // {キー, 説明}
}

// ヘルプの内容。2 列で表示できるときは左列 helpLeft、右列 helpRight。
var (
	helpLeft = []helpSec{
		{"全体", [][2]string{
			{"tab", "セッション / タスク枠を切替"},
			{"r", "再読み込み"},
			{"?", "このヘルプ"},
			{"q  ctrl+c", "終了"},
		}},
		{"セッション枠", [][2]string{
			{"↑↓  jk", "移動"},
			{"g G  pgup pgdn", "先頭 / 末尾 / ページ移動"},
			{"→ l  enter", "タスク枠へ"},
			{"n", "新しいセッション"},
			{"a", "タスク追加"},
			{"e", "編集"},
			{"w", "作業フォルダ"},
			{"z", "アーカイブ / 解除"},
			{"H", "アーカイブの表示切替"},
			{"d", "削除"},
		}},
		{"詳細ビュー (v)", [][2]string{
			{"↑↓  jk  pgup pgdn", "スクロール"},
			{"space  s", "完了 / 着手"},
			{"c  A  e", "コメント / サブタスク / 編集"},
			{"esc  q  v", "閉じる"},
		}},
		{"フォーム", [][2]string{
			{"tab  shift+tab", "項目移動"},
			{"ctrl+enter  ctrl+s", "保存"},
			{"esc", "キャンセル"},
		}},
	}
	helpRight = []helpSec{
		{"タスク枠", [][2]string{
			{"↑↓  jk", "移動"},
			{"g G  pgup pgdn", "先頭 / 末尾 / ページ移動"},
			{"← h", "畳む → 親へ → セッション枠へ"},
			{"→ l", "開く → 最初の子へ"},
			{"[  ]", "すべて畳む / すべて開く"},
			{"space  x", "完了 / 戻す"},
			{"s", "着手 / 戻す"},
			{"b", "ブロック"},
			{"-", "スキップ"},
			{"p", "go / 戻す（そのフォルダの Claude に着手させる）"},
			{"a", "タスク追加"},
			{"A", "サブタスク追加"},
			{"c", "コメント"},
			{"v  o", "詳細ビュー"},
			{"e  enter", "編集"},
			{"K J  shift+↑↓", "並べ替え"},
			{"d", "削除"},
			{"f", "完了の表示切替"},
		}},
		{"マウス", [][2]string{
			{"クリック", "選択 / [ ] で完了 / ▸▾ で開閉"},
			{"ダブルクリック", "編集（セッションはタスク枠へ）"},
			{"右クリック", "メニュー"},
			{"ホイール", "スクロール"},
			{"詳細枠の上辺ドラッグ", "高さを変える"},
		}},
	}
)

// helpColumn はセクションを幅 w の行に並べる。kw はキー欄の幅。
func helpColumn(secs []helpSec, kw, w int) []string {
	var ls []string
	for i, sec := range secs {
		if i > 0 {
			ls = append(ls, strings.Repeat(" ", w))
		}
		ls = append(ls, stHeadOn.Render(fit(sec.title, w)))
		for _, r := range sec.rows {
			ls = append(ls, stLabel.Render(fit(" "+r[0], kw+1))+"  "+fit(r[1], w-kw-3))
		}
	}
	return ls
}

// helpBody はヘルプの本文行（幅は 2 列 / 1 列のどちらか）を返す。
func (m *model) helpBody() ([]string, int) {
	kw, dw := 0, 0
	for _, sec := range append(append([]helpSec{}, helpLeft...), helpRight...) {
		for _, r := range sec.rows {
			kw = max(kw, runewidth.StringWidth(r[0]))
			dw = max(dw, runewidth.StringWidth(r[1]))
		}
	}
	colW := 1 + kw + 2 + dw
	const gap = 4
	if 2*colW+gap+4 <= m.w {
		l, r := helpColumn(helpLeft, kw, colW), helpColumn(helpRight, kw, colW)
		body := make([]string, max(len(l), len(r)))
		for i := range body {
			a, b := strings.Repeat(" ", colW), ""
			if i < len(l) {
				a = l[i]
			}
			if i < len(r) {
				b = r[i]
			}
			body[i] = a + strings.Repeat(" ", gap) + b
		}
		return body, 2*colW + gap
	}
	colW = min(colW, m.w-4)
	return helpColumn(append(append([]helpSec{}, helpLeft...), helpRight...), min(kw, colW/2), colW), colW
}

// renderHelp はヘルプを画面中央に重ねて描く。
func (m *model) renderHelp(lines []string) {
	body, bw := m.helpBody()
	inner := bw + 2
	w := inner + 2
	rows := min(len(body), len(lines)-4)
	m.helpOff = clamp(m.helpOff, 0, max(0, len(body)-rows))
	x := max(0, (m.w-w)/2)
	y := max(1, (len(lines)-rows-2)/2)
	put := func(row int, s string) {
		if row >= 0 && row < len(lines) {
			lines[row] = overlay(lines[row], x, s, w)
		}
	}
	put(y, boxTop("Help", w, true))
	for i := 0; i < rows; i++ {
		l := body[m.helpOff+i]
		pad := max(0, bw-lipgloss.Width(l))
		put(y+1+i, side(true)+stMenu.Render(" ")+l+stMenu.Render(strings.Repeat(" ", pad)+" ")+side(true))
	}
	sb := ""
	if len(body) > rows {
		sb = fmt.Sprintf("%d-%d/%d", m.helpOff+1, m.helpOff+rows, len(body))
	}
	put(y+rows+1, boxBottom(w, true, sb))
}

func (m *model) helpKey(k tea.KeyMsg) tea.Cmd {
	switch k.String() {
	case "esc", "q", "?", "enter":
		m.help = false
	case "ctrl+c":
		return tea.Quit
	case "up", "k":
		m.helpOff = max(0, m.helpOff-1)
	case "down", "j":
		m.helpOff++
	case "pgup", "ctrl+u":
		m.helpOff = max(0, m.helpOff-10)
	case "pgdown", "ctrl+d", " ":
		m.helpOff += 10
	case "home", "g":
		m.helpOff = 0
	case "end", "G":
		m.helpOff = 1 << 20
	}
	return nil
}
