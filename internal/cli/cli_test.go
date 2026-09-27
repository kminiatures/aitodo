package cli

import "testing"

func TestParseImportText(t *testing.T) {
	got, err := ParseImport([]byte("# comment\n- [ ] one\n  body1\n\tbody2\n\n2. two\n* three\n"))
	if err != nil || len(got) != 3 {
		t.Fatalf("%v %v", got, err)
	}
	if got[0].Title != "one" || got[0].Body != "body1\nbody2" || got[1].Title != "two" || got[2].Title != "three" {
		t.Fatalf("%+v", got)
	}
}

func TestParseImportJSON(t *testing.T) {
	got, err := ParseImport([]byte(`["a", {"title":"b","body":"x"}]`))
	if err != nil || len(got) != 2 || got[1].Body != "x" {
		t.Fatalf("%+v %v", got, err)
	}
}

func TestParseArgsInterspersed(t *testing.T) {
	p, err := parseArgs([]string{"hello", "-s", "sess", "world", "--note=n", "--claim"},
		flagSpec{value: []string{"s=session", "note"}, bools: []string{"claim"}})
	if err != nil || p.get("session") != "sess" || p.get("note") != "n" || !p.bools["claim"] || len(p.pos) != 2 {
		t.Fatalf("%+v %v", p, err)
	}
}

func TestParseImportNested(t *testing.T) {
	got, err := ParseImport([]byte("- parent\n  body of parent\n  - child1\n    child body\n  - child2\n      - grandchild\n- next\n"))
	if err != nil || len(got) != 2 {
		t.Fatalf("%+v %v", got, err)
	}
	p := got[0]
	if p.Body != "body of parent" || len(p.Subtasks) != 2 || p.Subtasks[0].Body != "child body" ||
		len(p.Subtasks[1].Subtasks) != 1 || p.Subtasks[1].Subtasks[0].Title != "grandchild" || got[1].Title != "next" {
		t.Fatalf("%+v", got)
	}
}

func TestParseImportJSONNested(t *testing.T) {
	got, err := ParseImport([]byte(`[{"title":"a","subtasks":[{"title":"b"},"c"]}]`))
	if err != nil || len(got[0].Subtasks) != 2 || got[0].Subtasks[1].Title != "c" {
		t.Fatalf("%+v %v", got, err)
	}
}

func TestParseArgsDashText(t *testing.T) {
	p, err := parseArgs([]string{"12", "- fixed X\n- added test", "--author", "claude"}, flagSpec{value: []string{"author"}})
	if err != nil || len(p.pos) != 2 || p.pos[1] != "- fixed X\n- added test" || p.get("author") != "claude" {
		t.Fatalf("%+v %v", p, err)
	}
	if _, err := parseArgs([]string{"--bogus"}, flagSpec{}); err == nil {
		t.Fatal("unknown flags must still be rejected")
	}
}
