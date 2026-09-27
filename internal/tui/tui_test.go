package tui

import (
	"path/filepath"
	"strings"
	"testing"

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
	m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	cs, _ := st.ListComments(m.curTask().ID)
	if len(cs) != 1 || cs[0].Body != "レビューOK" {
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
	m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m.Update(tea.KeyMsg{Type: tea.KeyEnter})
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
