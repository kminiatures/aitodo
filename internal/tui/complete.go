package tui

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// completeDir は bash 風のディレクトリ補完を行う。
//   - 候補が 1 つ: その名前 + "/" で確定
//   - 候補が複数: 共通接頭辞まで伸ばす。伸ばせなければ候補一覧を返す
//   - 候補なし: 値はそのまま、候補も空
//
// base は候補名の前に付ける入力済み部分（"~/work/" など、ユーザーの書き方のまま）。
// 大文字小文字が一致する候補が無ければ、大小を無視して探す（macOS の FS に合わせる）。
func completeDir(value, cwd string) (newValue, base string, cands []string) {
	if value == "~" {
		return "~/", "~/", nil
	}
	i := strings.LastIndex(value, "/")
	base, prefix := value[:i+1], value[i+1:]

	dir := expandPath(base, cwd)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return value, base, nil
	}
	var exact, fold []string
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, ".") && !strings.HasPrefix(prefix, ".") {
			continue
		}
		if !isDirEntry(dir, e) {
			continue
		}
		if strings.HasPrefix(name, prefix) {
			exact = append(exact, name)
		} else if strings.HasPrefix(strings.ToLower(name), strings.ToLower(prefix)) {
			fold = append(fold, name)
		}
	}
	matches := exact
	if len(matches) == 0 {
		matches = fold
	}
	sort.Strings(matches)
	switch len(matches) {
	case 0:
		return value, base, nil
	case 1:
		return base + matches[0] + "/", base, nil
	}
	lcp := commonPrefixFold(matches)
	if lcp != prefix && len([]rune(lcp)) >= len([]rune(prefix)) {
		return base + lcp, base, nil
	}
	return value, base, matches
}

// expandPath は "~" を展開し、相対パスを cwd 基準にする。空なら cwd。
func expandPath(p, cwd string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			p = filepath.Join(home, p[1:])
		}
	}
	if p == "" {
		return cwd
	}
	if !filepath.IsAbs(p) {
		p = filepath.Join(cwd, p)
	}
	return p
}

func isDirEntry(dir string, e os.DirEntry) bool {
	if e.IsDir() {
		return true
	}
	if e.Type()&os.ModeSymlink != 0 { // シンボリックリンクはリンク先で判定
		fi, err := os.Stat(filepath.Join(dir, e.Name()))
		return err == nil && fi.IsDir()
	}
	return false
}

// commonPrefixFold は大文字小文字を無視した共通接頭辞を、先頭候補の表記で返す。
func commonPrefixFold(names []string) string {
	first := []rune(names[0])
	n := len(first)
	for _, s := range names[1:] {
		r := []rune(s)
		k := 0
		for k < n && k < len(r) && strings.EqualFold(string(first[k]), string(r[k])) {
			k++
		}
		n = k
	}
	return string(first[:n])
}
