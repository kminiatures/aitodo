package cli

import (
	"fmt"
	"strings"
)

// flagSpec はコマンドごとの受け付けフラグ定義。value は値を取るフラグ、bools は真偽フラグ。
// エイリアスは "s=session" のように書く（短縮名=正式名）。
type flagSpec struct {
	value []string
	bools []string
}

type parsed struct {
	pos   []string
	vals  map[string]string
	bools map[string]bool
}

func (p *parsed) has(name string) bool   { _, ok := p.vals[name]; return ok }
func (p *parsed) get(name string) string { return p.vals[name] }
func (p *parsed) ptr(name string) *string {
	if v, ok := p.vals[name]; ok {
		return &v
	}
	return nil
}

func splitAlias(def string) (short, long string) {
	if i := strings.IndexByte(def, '='); i >= 0 {
		return def[:i], def[i+1:]
	}
	return "", def
}

// parseArgs はフラグと位置引数が混在していても解釈する。"--" 以降は全て位置引数。
func parseArgs(args []string, spec flagSpec) (*parsed, error) {
	valueNames := map[string]string{}
	boolNames := map[string]string{}
	for _, d := range spec.value {
		s, l := splitAlias(d)
		valueNames[l] = l
		if s != "" {
			valueNames[s] = l
		}
	}
	for _, d := range spec.bools {
		s, l := splitAlias(d)
		boolNames[l] = l
		if s != "" {
			boolNames[s] = l
		}
	}
	p := &parsed{vals: map[string]string{}, bools: map[string]bool{}}
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			p.pos = append(p.pos, args[i+1:]...)
			break
		}
		// "-" 単体・負数・"- 箇条書き" のように空白を含むものはフラグではなく位置引数
		if len(a) < 2 || a[0] != '-' || isNumber(a) || strings.ContainsAny(strings.SplitN(a, "=", 2)[0], " \t\n") {
			p.pos = append(p.pos, a)
			continue
		}
		name := strings.TrimLeft(a, "-")
		val, hasVal := "", false
		if j := strings.IndexByte(name, '='); j >= 0 {
			name, val, hasVal = name[:j], name[j+1:], true
		}
		if l, ok := valueNames[name]; ok {
			if !hasVal {
				if i+1 >= len(args) {
					return nil, fmt.Errorf("flag --%s requires a value", l)
				}
				i++
				val = args[i]
			}
			p.vals[l] = val
			continue
		}
		if l, ok := boolNames[name]; ok {
			p.bools[l] = !hasVal || (val != "false" && val != "0")
			continue
		}
		return nil, fmt.Errorf("unknown flag %s", a)
	}
	return p, nil
}

func isNumber(s string) bool {
	if len(s) < 2 || s[0] != '-' {
		return false
	}
	for _, c := range s[1:] {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}
