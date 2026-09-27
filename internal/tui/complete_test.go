package tui

import (
	"os"
	"path/filepath"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func mkdirs(t *testing.T, root string, names ...string) {
	for _, n := range names {
		if err := os.MkdirAll(filepath.Join(root, n), 0o755); err != nil {
			t.Fatal(err)
		}
	}
}

func TestCompleteDir(t *testing.T) {
	root := t.TempDir()
	mkdirs(t, root, "apps", "apple", "banana", ".hidden", "作業フォルダ")
	os.WriteFile(filepath.Join(root, "applefile"), nil, 0o644)
	os.Symlink(filepath.Join(root, "banana"), filepath.Join(root, "bananalink"))

	cases := []struct {
		in, want string
		ncands   int
	}{
		{root + "/ban", root + "/banana", 0},    // banana と bananalink の共通接頭辞
		{root + "/banana", root + "/banana", 2}, // もう伸ばせない → 候補表示
		{root + "/app", root + "/app", 2},       // apps / apple（ファイルは除外）
		{root + "/apps", root + "/apps/", 0},    // 一意に確定
		{root + "/作", root + "/作業フォルダ/", 0},     // 全角
		{root + "/.h", root + "/.hidden/", 0},   // ドット始まりは明示時のみ
		{root + "/", root + "/", 5},             // 隠しフォルダ以外すべて
		{root + "/BAN", root + "/banana", 0},    // 大小無視のフォールバック
		{root + "/zzz", root + "/zzz", 0},       // 候補なし
		{"~", "~/", 0},
	}
	for _, c := range cases {
		got, _, cands := completeDir(c.in, root)
		if got != c.want || len(cands) != c.ncands {
			t.Errorf("completeDir(%q) = %q, %v; want %q, %d cands", c.in, got, cands, c.want, c.ncands)
		}
	}
	// 相対パスは cwd 基準
	if got, _, _ := completeDir("ba", root); got != "banana" {
		t.Errorf("relative: got %q", got)
	}
}

func TestFormTabCompletesDirAndClickCandidate(t *testing.T) {
	m, _ := setup(t)
	root := t.TempDir()
	mkdirs(t, root, "alpha", "alps", "beta")
	m.action("workdir")
	fl := m.form.fields[0]
	fl.in.SetValue(root + "/al")
	m.Update(tea.KeyMsg{Type: tea.KeyTab})
	if fl.in.Value() != root+"/alp" {
		t.Fatalf("after tab: %q", fl.in.Value())
	}
	m.Update(tea.KeyMsg{Type: tea.KeyTab})
	if len(fl.cands) != 2 {
		t.Fatalf("cands = %v", fl.cands)
	}
	// 候補 "alps" をクリック
	_, regs := m.render()
	clicked := false
	for _, r := range regs {
		if r.kind == rFormCandidate && r.action == "alps" {
			m.lastClickY = -1
			click(m, r.x0, r.y)
			clicked = true
		}
	}
	if !clicked || fl.in.Value() != root+"/alps/" || fl.cands != nil {
		t.Fatalf("clicked=%v value=%q cands=%v", clicked, fl.in.Value(), fl.cands)
	}
	// 他のキーを押すと候補は消える / フォーカスは移動しない（tab は補完専用）
	m.Update(tea.KeyMsg{Type: tea.KeyTab})
	if m.form.focus != 0 {
		t.Fatal("tab in dir field must not move focus")
	}
}

func TestCandidatesOverflow(t *testing.T) {
	names := []string{}
	for i := 0; i < 200; i++ {
		names = append(names, "directory-name")
	}
	lines, regs := layoutCandidates(names, 60, 6)
	if len(lines) != 6 {
		t.Fatalf("lines = %d", len(lines))
	}
	for _, r := range regs {
		if r.y >= 5 || r.x1 > 62 {
			t.Fatalf("region out of range: %+v", r)
		}
	}
}
