package tui

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/kminiatures/aitodo/internal/store"
)

func setup(t *testing.T) (*model, *store.Store) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	s1, _ := st.CreateSession("日本語セッション名がとても長い場合のテスト", "", t.TempDir())
	st.CreateSession("second", "", "")
	st.AddTasks(s1.ID, []store.NewTask{{Title: "最初のタスク"}, {Title: "二番目のタスク（全角）"}, {Title: "third"}})
	m := newModel(st, s1.ID)
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	return m, st
}

func click(m *model, x, y int) {
	m.Update(tea.MouseMsg{X: x, Y: y, Action: tea.MouseActionPress, Button: tea.MouseButtonLeft})
}

// 描画行の表示幅がすべて端末幅に収まる（全角でも崩れない）。
func TestRenderWidth(t *testing.T) {
	m, _ := setup(t)
	lines := strings.Split(m.View(), "\n")
	if len(lines) != 24 {
		t.Fatalf("got %d lines, want 24", len(lines))
	}
	for i, l := range lines {
		if w := lipgloss.Width(l); w != 80 {
			t.Errorf("line %d width %d: %q", i, w, l)
		}
	}
}

func TestClickCheckboxTogglesDone(t *testing.T) {
	m, st := setup(t)
	g := m.geom()
	// 2 行目タスクのチェックボックス: タスク枠左辺 + 2 (" [" の "[" 位置)
	y := g.paneTop + 1 + 1
	click(m, g.tx+2, y)
	tk, _ := st.GetTask(m.tasks[1].ID)
	if tk.Status != store.StatusDone {
		t.Fatalf("status = %s, want done", tk.Status)
	}
	if m.tIdx != 1 {
		t.Fatalf("tIdx = %d", m.tIdx)
	}
	m.lastClickY = -1
	click(m, g.tx+2, y)
	tk, _ = st.GetTask(tk.ID)
	if tk.Status != store.StatusTodo {
		t.Fatalf("status = %s, want todo after 2nd click", tk.Status)
	}
}

func TestClickTaskSelectsAndDoubleClickEdits(t *testing.T) {
	m, _ := setup(t)
	g := m.geom()
	y := g.paneTop + 1 + 2
	click(m, g.tx+15, y)
	if m.tIdx != 2 || m.focus != focusTasks {
		t.Fatalf("tIdx=%d focus=%d", m.tIdx, m.focus)
	}
	click(m, g.tx+15, y)
	if m.form == nil || m.form.kind != "edit-task" {
		t.Fatalf("double click should open edit form, got %+v", m.form)
	}
}

func TestClickSessionSwitches(t *testing.T) {
	m, _ := setup(t)
	g := m.geom()
	// セッション一覧は更新日時降順。"second" を探してクリック
	idx := -1
	for i, s := range m.sessions {
		if s.Name == "second" {
			idx = i
		}
	}
	click(m, g.sx+3, g.paneTop+1+idx)
	if m.curSession().Name != "second" || m.focus != focusSessions {
		t.Fatalf("selected %q focus %d", m.curSession().Name, m.focus)
	}
	if len(m.tasks) != 0 {
		t.Fatalf("tasks should be reloaded for session, got %d", len(m.tasks))
	}
}

func TestButtonsAndFormMouse(t *testing.T) {
	m, st := setup(t)
	_, regs := m.render()
	var add *region
	for i := range regs {
		if regs[i].kind == rButton && regs[i].action == "new-task" {
			add = &regs[i]
		}
	}
	if add == nil {
		t.Fatal("no +Task button")
	}
	click(m, add.x0+1, add.y)
	if m.form == nil || m.form.kind != "new-task" {
		t.Fatal("form not opened")
	}
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("マウスで追加")})
	_, regs = m.render()
	for _, r := range regs {
		if r.kind == rFormButton && r.action == "ok" {
			m.lastClickY = -1
			click(m, r.x0, r.y)
		}
	}
	if m.form != nil {
		t.Fatalf("form should be closed; msg=%q", m.msg)
	}
	ts, _ := st.ListTasks(m.curSession().ID, nil)
	if ts[len(ts)-1].Title != "マウスで追加" {
		t.Fatalf("last task = %q", ts[len(ts)-1].Title)
	}
}

func TestWheelScroll(t *testing.T) {
	m, st := setup(t)
	items := []store.NewTask{}
	for i := 0; i < 40; i++ {
		items = append(items, store.NewTask{Title: "bulk"})
	}
	st.AddTasks(m.curSession().ID, items)
	m.reload()
	g := m.geom()
	m.Update(tea.MouseMsg{X: g.tx + 5, Y: g.paneTop + 2, Action: tea.MouseActionPress, Button: tea.MouseButtonWheelDown})
	if m.tOff != 3 {
		t.Fatalf("tOff = %d, want 3", m.tOff)
	}
	// スクロール後のクリックはオフセットを考慮する
	click(m, g.tx+15, g.paneTop+1)
	if m.tIdx != 3 {
		t.Fatalf("tIdx = %d, want 3", m.tIdx)
	}
}

func TestKeysToggleAndReorder(t *testing.T) {
	m, st := setup(t)
	m.focus = focusTasks
	m.Update(tea.KeyMsg{Type: tea.KeySpace, Runes: []rune(" ")})
	tk, _ := st.GetTask(m.tasks[0].ID)
	if tk.Status != store.StatusDone {
		t.Fatalf("space: status %s", tk.Status)
	}
	id := m.tasks[0].ID
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("J")})
	if m.tasks[1].ID != id || m.tIdx != 1 {
		t.Fatalf("J: order wrong, tIdx=%d", m.tIdx)
	}
}

func TestTinyTerminalNoPanic(t *testing.T) {
	m, _ := setup(t)
	for _, sz := range [][2]int{{40, 6}, {10, 3}, {0, 0}, {80, 12}} {
		m.Update(tea.WindowSizeMsg{Width: sz[0], Height: sz[1]})
		_ = m.View()
		click(m, 1, 1)
	}
}

func TestTickKeepsWheelScroll(t *testing.T) {
	m, st := setup(t)
	items := []store.NewTask{}
	for i := 0; i < 40; i++ {
		items = append(items, store.NewTask{Title: "bulk"})
	}
	st.AddTasks(m.curSession().ID, items)
	m.reload()
	g := m.geom()
	m.Update(tea.MouseMsg{X: g.tx + 5, Y: g.paneTop + 2, Action: tea.MouseActionPress, Button: tea.MouseButtonWheelDown})
	m.Update(tickMsg{})
	if m.tOff != 3 {
		t.Fatalf("tOff = %d after tick, want 3", m.tOff)
	}
}

func TestToggleIgnoredWhenSessionsFocused(t *testing.T) {
	m, st := setup(t)
	m.focus = focusSessions
	m.Update(tea.KeyMsg{Type: tea.KeySpace, Runes: []rune(" ")})
	tk, _ := st.GetTask(m.tasks[0].ID)
	if tk.Status != store.StatusTodo {
		t.Fatalf("status changed to %s while sessions focused", tk.Status)
	}
}

func TestSubtaskIndentCheckboxAndComment(t *testing.T) {
	m, st := setup(t)
	parent := m.tasks[0]
	sub, _ := st.AddSubtask(parent.ID, "子タスク", "")
	m.reload()
	if m.tasks[1].ID != sub.ID || m.tasks[1].Depth != 1 {
		t.Fatalf("subtask should follow parent: %+v", m.tasks[1])
	}
	g := m.geom()
	y := g.paneTop + 1 + 1
	// インデント分（2 桁）ずれたチェックボックスをクリック
	click(m, g.tx+1+2+1, y)
	if tk, _ := st.GetTask(sub.ID); tk.Status != store.StatusDone {
		t.Fatalf("indented checkbox click: status %s", tk.Status)
	}
	// 幅が崩れない
	for i, l := range strings.Split(m.View(), "\n") {
		if w := lipgloss.Width(l); w != 80 {
			t.Fatalf("line %d width %d", i, w)
		}
	}
	// c → コメント入力 → enter
	m.focus = focusTasks
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("c")})
	if m.form == nil || m.form.kind != "comment" {
		t.Fatal("comment form not opened")
	}
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("レビューOK")})
	m.Update(tea.KeyMsg{Type: tea.KeyEnter}) // 複数行欄では改行
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("2行目")})
	m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("3行目")})
	if m.form == nil {
		t.Fatal("enter in multi-line field should not submit")
	}
	for i, l := range strings.Split(m.View(), "\n") {
		if w := lipgloss.Width(l); w != 80 {
			t.Fatalf("form line %d width %d", i, w)
		}
	}
	m.Update(tea.KeyMsg{Type: tea.KeyCtrlS})
	cs, _ := st.ListComments(m.curTask().ID)
	if len(cs) != 1 || cs[0].Body != "レビューOK\n2行目\n3行目" {
		t.Fatalf("comments %+v (msg %q)", cs, m.msg)
	}
	// v → 詳細ビューにコメント全文が出る
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("v")})
	if m.view == nil || !strings.Contains(m.View(), "レビューOK") {
		t.Fatal("view should show comment")
	}
	m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if m.view != nil {
		t.Fatal("esc should close view")
	}
	// A → サブタスク追加
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("A")})
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("孫")})
	m.Update(tea.KeyMsg{Type: tea.KeyEnter}) // タイトル → 詳細
	m.Update(tea.KeyMsg{Type: tea.KeyCtrlS})
	if c := m.curTask(); c == nil || c.Title != "孫" || c.ParentID == nil {
		t.Fatalf("subtask not added/selected: %+v msg=%q", c, m.msg)
	}
}

// 詳細ビューの操作は、リストの選択が変わっても表示中のタスクに効く。
func TestViewActsOnViewedTask(t *testing.T) {
	m, st := setup(t)
	m.focus = focusTasks
	m.hideDone = true
	m.reload()
	viewed := m.tasks[0].ID
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("v")})
	m.Update(tea.KeyMsg{Type: tea.KeySpace, Runes: []rune(" ")}) // done → hideDone でリストから消える
	m.Update(tickMsg{})
	m.Update(tea.KeyMsg{Type: tea.KeySpace, Runes: []rune(" ")}) // もう一度 → 同じタスクが todo に戻るべき
	if tk, _ := st.GetTask(viewed); tk.Status != store.StatusTodo {
		t.Fatalf("viewed task status = %s", tk.Status)
	}
	for _, x := range m.tasks {
		if x.ID != viewed && x.Status != store.StatusTodo {
			t.Fatalf("other task #%d was toggled to %s", x.ID, x.Status)
		}
	}
}

func TestDragDetailBorderResizes(t *testing.T) {
	m, _ := setup(t)
	g := m.geom()
	top0 := g.detailTop
	click(m, 5, top0)
	if !m.dragDetail {
		t.Fatal("press on detail top border should start drag")
	}
	m.Update(tea.MouseMsg{X: 5, Y: top0 - 4, Action: tea.MouseActionMotion, Button: tea.MouseButtonLeft})
	m.Update(tea.MouseMsg{X: 5, Y: top0 - 4, Action: tea.MouseActionRelease, Button: tea.MouseButtonLeft})
	if m.dragDetail {
		t.Fatal("release should end drag")
	}
	if g2 := m.geom(); g2.detailTop != top0-4 || g2.detailH != g.detailH+4 {
		t.Fatalf("detail not resized: top %d->%d h %d->%d", top0, g2.detailTop, g.detailH, g2.detailH)
	}
	// 高さは保存され、次回起動時に復元される
	if m2 := newModel(m.st, 0); m2.detailH != m.detailH {
		t.Fatalf("restored detailH=%d, want %d", m2.detailH, m.detailH)
	}
	// 上限・下限でクランプされ、上の枠が潰れない
	click(m, 5, m.geom().detailTop)
	m.Update(tea.MouseMsg{X: 5, Y: 0, Action: tea.MouseActionMotion, Button: tea.MouseButtonLeft})
	if g3 := m.geom(); g3.paneH < minPaneH {
		t.Fatalf("pane collapsed: paneH=%d", g3.paneH)
	}
	m.Update(tea.MouseMsg{X: 5, Y: m.h, Action: tea.MouseActionMotion, Button: tea.MouseButtonLeft})
	if g4 := m.geom(); g4.detailH != minDetailH {
		t.Fatalf("detailH=%d, want min %d", g4.detailH, minDetailH)
	}
	for _, l := range strings.Split(m.View(), "\n") {
		if w := lipgloss.Width(l); w != m.w {
			t.Fatalf("line width %d != %d", w, m.w)
		}
	}
}

func rightClick(m *model, x, y int) {
	m.Update(tea.MouseMsg{X: x, Y: y, Action: tea.MouseActionPress, Button: tea.MouseButtonRight})
}

// 右クリックで行を選択してメニューを開き、項目クリック・キーで実行できる。
func TestContextMenu(t *testing.T) {
	m, st := setup(t)
	g := m.geom()
	y := g.paneTop + 1 + 2
	rightClick(m, g.tx+10, y)
	if m.menu == nil || m.focus != focusTasks || m.tIdx != 2 {
		t.Fatalf("menu=%v focus=%d tIdx=%d", m.menu, m.focus, m.tIdx)
	}
	// 描画幅が崩れない
	for i, l := range strings.Split(m.View(), "\n") {
		if w := lipgloss.Width(l); w != 80 {
			t.Errorf("line %d width %d: %q", i, w, l)
		}
	}
	// 先頭項目（完了）をクリック
	_, regs := m.render()
	var hit *region
	for i := range regs {
		if regs[i].kind == rMenuItem && regs[i].idx == 0 {
			hit = &regs[i]
		}
	}
	if hit == nil {
		t.Fatal("no menu item region")
	}
	click(m, hit.x0+1, hit.y)
	if m.menu != nil {
		t.Fatal("menu should close")
	}
	if tk, _ := st.GetTask(m.tasks[2].ID); tk.Status != store.StatusDone {
		t.Fatalf("status = %s, want done", tk.Status)
	}

	// ホバーで項目がハイライトされ、外れても最後の項目のまま
	rightClick(m, g.tx+10, g.paneTop+1)
	_, regs = m.render()
	for _, r := range regs {
		if r.kind == rMenuItem && r.idx == 3 {
			m.Update(tea.MouseMsg{X: r.x0 + 1, Y: r.y, Action: tea.MouseActionMotion, Button: tea.MouseButtonNone})
		}
	}
	if m.menu == nil || m.menu.sel != 3 {
		t.Fatalf("hover: menu=%+v", m.menu)
	}
	m.Update(tea.MouseMsg{X: 0, Y: 0, Action: tea.MouseActionMotion, Button: tea.MouseButtonNone})
	if m.menu == nil || m.menu.sel != 3 {
		t.Fatalf("hover outside: menu=%+v", m.menu)
	}
	m.Update(tea.KeyMsg{Type: tea.KeyEsc})

	// キー操作: ↓ enter で 2 番目（着手）
	rightClick(m, g.tx+10, g.paneTop+1)
	m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if tk, _ := st.GetTask(m.tasks[0].ID); tk.Status != store.StatusDoing {
		t.Fatalf("status = %s, want doing", tk.Status)
	}

	// 外側クリック・esc で閉じるだけ
	rightClick(m, g.tx+10, g.paneTop+1)
	click(m, 0, 0)
	if m.menu != nil {
		t.Fatal("outside click should close menu")
	}
	rightClick(m, g.tx+10, g.paneTop+1)
	m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if m.menu != nil {
		t.Fatal("esc should close menu")
	}

	// セッション行: 右端近くでも画面内に収まり、編集フォームが開く
	rightClick(m, g.sx+2, g.paneTop+1+1)
	if m.menu == nil || m.focus != focusSessions || m.sIdx != 1 {
		t.Fatalf("session menu: menu=%v focus=%d sIdx=%d", m.menu, m.focus, m.sIdx)
	}
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("e")})
	if m.form == nil || m.form.kind != "edit-session" {
		t.Fatalf("form = %+v", m.form)
	}
}

func TestOverlayWide(t *testing.T) {
	line := "あいうえお" // 幅 10
	got := overlay(line, 3, "XX", 2)
	if w := ansi.StringWidth(got); w != 10 {
		t.Fatalf("width %d: %q", w, got)
	}
	if s := ansi.Strip(got); s != "あ XX えお" {
		t.Fatalf("got %q", s)
	}
}

// tab で入力欄 → OK → Cancel とフォーカスが移り、ボタン上の enter で押せる。
func TestFormTabFocusesButtons(t *testing.T) {
	m, st := setup(t)
	m.focus = focusTasks
	m.action("new-task")
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("tabで追加")})
	m.Update(tea.KeyMsg{Type: tea.KeyTab}) // → 詳細
	m.Update(tea.KeyMsg{Type: tea.KeyTab}) // → OK
	if m.form.focus != m.form.okIdx() {
		t.Fatalf("focus = %d, want OK", m.form.focus)
	}
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("z")}) // ボタン上の文字入力は無視
	m.Update(tea.KeyMsg{Type: tea.KeyTab})                       // → Cancel
	if m.form.focus != m.form.cancelIdx() {
		t.Fatalf("focus = %d, want Cancel", m.form.focus)
	}
	m.Update(tea.KeyMsg{Type: tea.KeyTab}) // → 先頭の欄へ戻る
	if m.form.focus != 0 {
		t.Fatalf("focus = %d, want 0", m.form.focus)
	}
	m.Update(tea.KeyMsg{Type: tea.KeyShiftTab}) // → Cancel
	m.Update(tea.KeyMsg{Type: tea.KeyLeft})     // → OK
	if m.form.focus != m.form.okIdx() {
		t.Fatalf("focus = %d, want OK", m.form.focus)
	}
	m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if m.form != nil {
		t.Fatal("form should be closed")
	}
	ts, _ := st.ListTasks(m.curSession().ID, nil)
	if got := ts[len(ts)-1]; got.Title != "tabで追加" || got.Body != "" {
		t.Fatalf("last task = %q / %q", got.Title, got.Body)
	}

	// Cancel 上の enter は保存せずに閉じる。
	n := len(ts)
	m.action("new-task")
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("保存しない")})
	m.Update(tea.KeyMsg{Type: tea.KeyShiftTab}) // → Cancel
	m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if m.form != nil {
		t.Fatal("form should be closed")
	}
	if ts, _ := st.ListTasks(m.curSession().ID, nil); len(ts) != n {
		t.Fatalf("tasks = %d, want %d", len(ts), n)
	}
}

// 確認ダイアログでも tab で Yes / No を切り替え、enter で押せる。
func TestConfirmDialogTab(t *testing.T) {
	m, st := setup(t)
	m.focus = focusTasks
	n := len(m.tasks)
	m.action("delete")
	m.Update(tea.KeyMsg{Type: tea.KeyTab}) // → No
	m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if m.form != nil {
		t.Fatal("dialog should be closed")
	}
	if ts, _ := st.ListTasks(m.curSession().ID, nil); len(ts) != n {
		t.Fatalf("deleted on No: %d tasks", len(ts))
	}
	m.action("delete")
	m.Update(tea.KeyMsg{Type: tea.KeyEnter}) // 既定は Yes
	if ts, _ := st.ListTasks(m.curSession().ID, nil); len(ts) != n-1 {
		t.Fatalf("not deleted on Yes: %d tasks", len(ts))
	}
}

// ctrl+enter（端末からは ctrl+j / CSI 列として届く）で、複数行欄やボタン上からでも保存する。
func TestCtrlEnterSubmits(t *testing.T) {
	m, st := setup(t)
	m.focus = focusTasks
	id := m.curTask().ID
	for _, send := range []func(){
		func() { m.Update(tea.KeyMsg{Type: tea.KeyCtrlJ}) },
		func() { m.Update(fakeCSI("\x1b[27;5;13~")) },
		func() { m.Update(fakeCSI("\x1b[13;5u")) },
	} {
		m.action("comment")
		m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a")})
		m.Update(tea.KeyMsg{Type: tea.KeyEnter})
		m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("b")})
		send()
		if m.form != nil {
			t.Fatal("ctrl+enter should submit")
		}
	}
	cs, _ := st.ListComments(id)
	if len(cs) != 3 || cs[0].Body != "a\nb" {
		t.Fatalf("comments %+v", cs)
	}

	// ボタン（Cancel）上でも ctrl+enter は保存。
	m.action("new-task")
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("ボタン上から")})
	m.Update(tea.KeyMsg{Type: tea.KeyShiftTab})
	m.Update(tea.KeyMsg{Type: tea.KeyCtrlJ})
	ts, _ := st.ListTasks(m.curSession().ID, nil)
	if m.form != nil || ts[len(ts)-1].Title != "ボタン上から" {
		t.Fatalf("form=%v last=%q", m.form != nil, ts[len(ts)-1].Title)
	}
}

// fakeCSI は bubbletea の unknownCSISequenceMsg と同じ文字列表現を持つメッセージ。
type fakeCSI string

func (f fakeCSI) String() string { return fmt.Sprintf("?CSI%+v?", []byte(f)[2:]) }

func TestTaskTimes(t *testing.T) {
	for d, want := range map[time.Duration]string{
		30 * time.Second:             "<1m",
		12 * time.Minute:             "12m",
		2*time.Hour + 5*time.Minute:  "2h05m",
		3*24*time.Hour + 4*time.Hour: "3d4h",
	} {
		if got := FmtDuration(d); got != want {
			t.Errorf("FmtDuration(%v) = %q, want %q", d, got, want)
		}
	}
	m, st := setup(t)
	m.focus = focusTasks
	id := m.curTask().ID
	st.SetStatus(id, store.StatusDoing, nil)
	st.SetStatus(id, store.StatusDone, nil)
	m.reload()
	v := m.View()
	for _, s := range []string{"created ", "started ", "done ", "(took "} {
		if !strings.Contains(v, s) {
			t.Errorf("detail pane lacks %q", s)
		}
	}
}

// ←→ でサブタスクを畳む・開く。畳んだ行は ▸ と隠れた着手中の数を出す。
func TestFoldSubtasks(t *testing.T) {
	m, st := setup(t)
	parent := m.tasks[0]
	c1, _ := st.AddSubtask(parent.ID, "子1", "")
	c2, _ := st.AddSubtask(parent.ID, "子2", "")
	st.AddSubtask(c2.ID, "孫", "")
	st.SetStatus(c1.ID, store.StatusDoing, nil)
	m.reload()
	key := func(k tea.KeyType) { m.Update(tea.KeyMsg{Type: k}) }
	if len(m.tasks) != 6 || !strings.Contains(m.View(), "▾[ ] #1") {
		t.Fatalf("expanded: %d tasks\n%s", len(m.tasks), m.View())
	}

	// 子の上で ← → 親へ、親の上で ← → 畳む
	m.selectTask(c1.ID)
	key(tea.KeyLeft)
	if m.curTask().ID != parent.ID || m.focus != focusTasks {
		t.Fatalf("left on child should select parent: %+v", m.curTask())
	}
	key(tea.KeyLeft)
	if len(m.tasks) != 3 || !strings.Contains(m.View(), "▸[ ] #1") || !strings.Contains(m.View(), ">1") {
		t.Fatalf("collapsed: %d tasks\n%s", len(m.tasks), m.View())
	}
	// トップレベルで ← → セッション枠へ
	key(tea.KeyLeft)
	if m.focus != focusSessions {
		t.Fatal("left on collapsed top-level should focus sessions")
	}
	// → でタスク枠へ戻り、もう一度 → で開く、さらに → で最初の子へ
	key(tea.KeyRight)
	key(tea.KeyRight)
	if len(m.tasks) != 6 {
		t.Fatalf("right should expand: %d", len(m.tasks))
	}
	key(tea.KeyRight)
	if m.curTask().ID != c1.ID {
		t.Fatalf("right on expanded should go to first child: %+v", m.curTask())
	}

	// [ ですべて畳むと、選択は見えている祖先へ移る
	m.selectTask(c2.ID)
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("[")})
	if len(m.tasks) != 3 || m.curTask().ID != parent.ID {
		t.Fatalf("collapse-all: %d tasks, cur %+v", len(m.tasks), m.curTask())
	}
	// 畳んだ親にサブタスクを足すと開いて選択される
	sub, _ := st.AddSubtask(c2.ID, "追加", "")
	m.reload()
	m.selectTask(sub.ID)
	if m.curTask().ID != sub.ID {
		t.Fatalf("selectTask should reveal: %+v", m.curTask())
	}
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("]")})
	if len(m.tasks) != 7 {
		t.Fatalf("expand-all: %d", len(m.tasks))
	}

	// ▾ をクリックで畳む（チェックボックスは状態を変えない）
	g := m.geom()
	click(m, g.tx+1, g.paneTop+1)
	if len(m.tasks) != 3 {
		t.Fatalf("click fold: %d", len(m.tasks))
	}
	if tk, _ := st.GetTask(parent.ID); tk.Status != store.StatusTodo {
		t.Fatalf("fold click changed status: %s", tk.Status)
	}
	for i, l := range strings.Split(m.View(), "\n") {
		if w := lipgloss.Width(l); w != 80 {
			t.Fatalf("line %d width %d", i, w)
		}
	}
}

// ? でヘルプを開き、スクロールして esc / クリックで閉じる。幅は崩れない。
func TestHelpModal(t *testing.T) {
	m, _ := setup(t)
	press := func(s string) { m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}) }
	checkWidth := func(w int) {
		t.Helper()
		for i, l := range strings.Split(m.View(), "\n") {
			if lw := lipgloss.Width(l); lw != w {
				t.Fatalf("line %d width %d: %q", i, lw, l)
			}
		}
	}
	press("?")
	if !m.help || !strings.Contains(m.View(), "Help") || !strings.Contains(m.View(), "セッション / タスク枠を切替") {
		t.Fatalf("help not shown:\n%s", m.View())
	}
	checkWidth(80)
	// ヘルプ中のキーは下の画面に効かない
	idx := m.tIdx
	press("j")
	press("j")
	if m.tIdx != idx || m.helpOff != 2 {
		t.Fatalf("tIdx=%d helpOff=%d", m.tIdx, m.helpOff)
	}
	press("G")
	m.View()
	if m.helpOff == 0 || m.helpOff > 100 {
		t.Fatalf("helpOff after G = %d", m.helpOff)
	}
	m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if m.help {
		t.Fatal("esc should close help")
	}
	// 広い画面では 2 列、狭い画面では 1 列でも幅が崩れない
	for _, w := range []int{140, 50} {
		m.Update(tea.WindowSizeMsg{Width: w, Height: 30})
		press("?")
		checkWidth(w)
		click(m, 0, 0)
		if m.help {
			t.Fatal("click should close help")
		}
	}
	// 詳細ビューからも開ける
	press("v")
	press("?")
	if !m.help || m.view == nil {
		t.Fatal("? in view should open help over view")
	}
}
