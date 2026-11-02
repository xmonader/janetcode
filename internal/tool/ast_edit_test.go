package tool

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const astGoSrc = `package x

import "os"

// A is documented.
func A() int { return 1 }

type T struct{ n int }

func (t *T) Get() int { return t.n }

func (t *T) Set(n int) { t.n = n }

func Get() int { return 0 }

var (
	v1 = 1
	v2 = 2
)
`

func runAst(t *testing.T, name, src string, in map[string]any) (string, string, error) {
	t.Helper()
	root := t.TempDir()
	p := filepath.Join(root, name)
	if err := os.WriteFile(p, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	in["path"] = name
	b, _ := json.Marshal(in)
	out, err := AstEdit{Root: root}.Run(context.Background(), b)
	got, _ := os.ReadFile(p)
	return out, string(got), err
}

func TestAstEditGo(t *testing.T) {
	cases := []struct {
		name    string
		in      map[string]any
		wantErr string
		want    []string // substrings of the file after
		notWant []string
	}{
		{"replace func keeps doc comment", map[string]any{"symbol": "A", "new_source": "func A() int {\n\treturn 2\n}\n"},
			"", []string{"// A is documented.\nfunc A() int {\n\treturn 2\n}\n\ntype T"}, []string{"return 1"}},
		{"method by qualified name", map[string]any{"symbol": "T.Get", "new_source": "func (t *T) Get() int { return -t.n }"},
			"", []string{"return -t.n", "func Get() int { return 0 }"}, nil},
		{"ambiguous name refused", map[string]any{"symbol": "Get", "new_source": "func Get() {}"}, "2 definitions named", nil, nil},
		{"missing name", map[string]any{"symbol": "Nope", "new_source": "func Nope() {}"}, "no definition named", nil, nil},
		{"syntax error rolled back", map[string]any{"symbol": "A", "new_source": "func A() int { return"}, "rolled back", nil, nil},
		{"group spec only", map[string]any{"symbol": "v2", "new_source": "v2 = 22"}, "", []string{"v1 = 1\n\tv2 = 22\n)"}, nil},
		{"type replaced", map[string]any{"symbol": "T", "new_source": "type T struct{ n, m int }"}, "", []string{"type T struct{ n, m int }\n\nfunc (t *T) Get"}, nil},
		{"import added to existing single", map[string]any{"add_import": "\"fmt\""}, "", []string{"import (\n\t\"os\"\n\t\"fmt\"\n)"}, nil},
		{"import present is a no-op", map[string]any{"add_import": "import \"os\""}, "no change", nil, nil},
		{"needs new_source", map[string]any{"symbol": "A"}, "needs new_source", nil, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out, got, err := runAst(t, "f.go", astGoSrc, c.in)
			if c.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), c.wantErr) {
					t.Fatalf("err = %v, want %q", err, c.wantErr)
				}
				if got != astGoSrc {
					t.Fatalf("file changed on a refused edit:\n%s", got)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !strings.HasPrefix(out, "Edited f.go by syntax node") {
				t.Fatalf("out = %q", out)
			}
			for _, w := range c.want {
				if !strings.Contains(got, w) {
					t.Errorf("missing %q in\n%s", w, got)
				}
			}
			for _, w := range c.notWant {
				if strings.Contains(got, w) {
					t.Errorf("still has %q", w)
				}
			}
		})
	}
}

func TestAstEditAddImportIdempotent(t *testing.T) {
	for name, src := range map[string]string{
		"a.py": "import os\nfrom x import (\n    a,\n    b,\n)\n\nx = 1\n",
		"a.rs": "use std::io;\n\nfn main() {}\n",
		"a.go": "package x\n\nfunc A() {}\n",
	} {
		line := map[string]string{"a.py": "import sys", "a.rs": "use std::fmt;", "a.go": "\"fmt\""}[name]
		once, ok, err := addImport(name, src, line)
		if err != nil || !ok {
			t.Fatalf("%s: %v %v", name, ok, err)
		}
		twice, ok, err := addImport(name, once, line)
		if err != nil || ok || twice != once {
			t.Fatalf("%s not idempotent: %v %v", name, ok, err)
		}
	}
	got, _, _ := addImport("a.py", "import os\nfrom x import (\n    a,\n)\n\nx = 1\n", "import sys")
	if !strings.Contains(got, ")\nimport sys\n\nx = 1") {
		t.Fatalf("not after the multi-line import:\n%s", got)
	}
	if _, _, err := addImport("a.txt", "x", "y"); err == nil {
		t.Fatal("unknown language accepted")
	}
}

func TestAstEditUnsupportedFile(t *testing.T) {
	_, got, err := runAst(t, "notes.txt", "hello\n", map[string]any{"symbol": "x", "new_source": "y"})
	if err == nil || !strings.Contains(err.Error(), "cannot parse") || got != "hello\n" {
		t.Fatalf("%v %q", err, got)
	}
}

func TestAstEditIsAnEditToolToTheGates(t *testing.T) {
	var _ Toucher = AstEdit{}
	var _ Previewer = AstEdit{}
	var _ Guard = AstEdit{}
	if !(AstEdit{}).Mutating() {
		t.Fatal("not mutating")
	}
	root := t.TempDir()
	in := json.RawMessage(`{"path":"f.go","symbol":"A","new_source":"func A() {}"}`)
	if ps, err := (AstEdit{Root: root}).Touches(in); err != nil || len(ps) != 1 || !strings.HasSuffix(ps[0], "f.go") {
		t.Fatalf("%v %v", ps, err)
	}
}
