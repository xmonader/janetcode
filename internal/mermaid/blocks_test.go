package mermaid

import (
	"strings"
	"testing"
)

func TestBlockDiagrams(t *testing.T) {
	cases := []struct {
		name    string
		src     string
		width   int
		want    []string // substrings, each on some line
		refused bool
	}{
		{
			name:  "pie with data",
			src:   "pie showData title Pets\n  \"Dogs\" : 75\n  \"Cats\" : 25\n",
			width: 60,
			want:  []string{"Pets", "Dogs ", "75 (75.0%)", "25 (25.0%)", "████"},
		},
		{
			name:  "pie title on its own line, tiny slice still visible",
			src:   "pie\n  title Mix\n  \"Big\" : 999\n  \"Tiny\" : 1\n",
			width: 60,
			want:  []string{"Mix", "99.9%", "0.1%"},
		},
		{name: "pie zero total", src: "pie\n  \"A\" : 0\n", width: 60, refused: true},
		{name: "pie unquoted label", src: "pie\n  A : 3\n", width: 60, refused: true},
		{name: "pie too narrow for a bar", src: "pie\n  \"A very long label indeed\" : 3\n", width: 40, refused: true},
		{
			name: "er entities and relationships",
			src: "erDiagram\n  CUSTOMER ||--o{ ORDER : places\n  ORDER ||..|{ LINE : contains\n" +
				"  CUSTOMER {\n    string name PK\n    int age \"years\"\n  }\n",
			width: 72,
			want: []string{"CUSTOMER", "string name PK", `int age "years"`, "ORDER", "LINE",
				"CUSTOMER 1 ──── 0..* ORDER : places", "ORDER 1 ╌╌╌╌ 1..* LINE : contains"},
		},
		{name: "er alias is refused", src: "erDiagram\n  p[Person] ||--o{ O : x\n", width: 72, refused: true},
		{name: "er unclosed block", src: "erDiagram\n  A {\n    int x\n", width: 72, refused: true},
		{
			name: "class with members, stereotype and relations",
			src: "classDiagram\n  class Animal {\n    <<abstract>>\n    +String name\n    +eat() void\n  }\n" +
				"  Animal <|-- Dog\n  Animal \"1\" *-- \"*\" Leg : has\n  Dog : +bark()\n  Dog ..> Food\n  Dog --|> Pet\n",
			width: 72,
			want: []string{"<<abstract>>", "+String name", "+eat() void", "+bark()",
				"Animal ◁─── Dog", `Animal "1" ◆─── "*" Leg : has`, "Dog ╌╌╌▶ Food", "Dog ───▷ Pet"},
		},
		{name: "class generics are refused", src: "classDiagram\n  class A {\n    List~int~ xs\n  }\n", width: 72, refused: true},
		{name: "class note is refused", src: "classDiagram\n  note for A \"x\"\n", width: 72, refused: true},
		{name: "gantt stays refused", src: "gantt\n  title A\n  section S\n  T :a1, 2024-01-01, 3d\n", width: 72, refused: true},
		{name: "wide box does not fit", src: "classDiagram\n  class A {\n    +" + strings.Repeat("x", 38) + "\n  }\n", width: 40, refused: true},
		{name: "wide characters are refused", src: "pie\n  \"日本\" : 3\n", width: 60, refused: true},
	}
	for _, c := range cases {
		lines, ok := Render(c.src, c.width)
		if c.refused {
			if ok {
				t.Errorf("%s: should fall back, drew:\n%s", c.name, strings.Join(lines, "\n"))
			}
			continue
		}
		if !ok {
			t.Errorf("%s: fell back", c.name)
			continue
		}
		all := strings.Join(lines, "\n")
		for _, w := range c.want {
			if !strings.Contains(all, w) {
				t.Errorf("%s: missing %q in:\n%s", c.name, w, all)
			}
		}
		for _, l := range lines {
			if runeLen(l) > c.width {
				t.Errorf("%s: line wider than %d: %q", c.name, c.width, l)
			}
		}
		again, _ := Render(c.src, c.width)
		if strings.Join(again, "\n") != all {
			t.Errorf("%s: not deterministic", c.name)
		}
	}
}

func TestBoxesWrapIntoRows(t *testing.T) {
	var sb strings.Builder
	sb.WriteString("erDiagram\n")
	for _, n := range []string{"ALPHA", "BRAVO", "CHARLIE", "DELTA", "ECHO", "FOXTROT"} {
		sb.WriteString("  " + n + " {\n    int id\n  }\n")
	}
	lines, ok := Render(sb.String(), 40)
	if !ok {
		t.Fatal("fell back")
	}
	tops := 0
	for _, l := range lines {
		if strings.HasPrefix(l, "┌") {
			tops++
		}
		if runeLen(l) > 40 {
			t.Errorf("too wide: %q", l)
		}
	}
	if tops < 2 {
		t.Errorf("expected several rows of boxes, got %d:\n%s", tops, strings.Join(lines, "\n"))
	}
}
