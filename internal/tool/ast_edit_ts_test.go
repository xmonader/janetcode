//go:build treesitter

package tool

import (
	"strings"
	"testing"
)

func TestAstEditTreesitterLanguages(t *testing.T) {
	py := "import os\n\n@dec\ndef f(x):\n    return x\n\ndef g():\n    pass\n"
	_, got, err := runAst(t, "a.py", py, map[string]any{"symbol": "f", "new_source": "def f(x):\n    return x + 1"})
	if err != nil || !strings.Contains(got, "return x + 1") || strings.Contains(got, "@dec") || !strings.Contains(got, "def g()") {
		t.Fatalf("py: %v\n%s", err, got)
	}
	rs := "use std::io;\n\n/// doc\nfn f() -> i32 {\n    1\n}\n\nfn g() {}\n"
	_, got, err = runAst(t, "a.rs", rs, map[string]any{"symbol": "f", "new_source": "fn f() -> i32 {\n    2\n}"})
	if err != nil || !strings.Contains(got, "/// doc\nfn f() -> i32 {\n    2\n}") {
		t.Fatalf("rs: %v\n%s", err, got)
	}
	_, got, err = runAst(t, "a.rs", rs, map[string]any{"symbol": "f", "new_source": "fn f( {"})
	if err == nil || !strings.Contains(err.Error(), "rolled back") || got != rs {
		t.Fatalf("rs rollback: %v", err)
	}
	_, _, err = runAst(t, "a.ts", "function f() {}\nclass A { f() {} }\n", map[string]any{"symbol": "f", "new_source": "function f() {}"})
	if err == nil || !strings.Contains(err.Error(), "2 definitions") {
		t.Fatalf("ts ambiguity: %v", err)
	}
}
