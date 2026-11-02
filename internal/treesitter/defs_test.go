//go:build treesitter

package treesitter

import "testing"

func TestDefinitionsCoverTheWholeDefinition(t *testing.T) {
	cases := []struct{ path, src, name, want string }{
		{"a.py", "import os\n\n@dec\ndef f(x):\n    return x\n\nx = 1\n", "f", "@dec\ndef f(x):\n    return x"},
		{"a.rs", "use std::fmt;\n\nstruct S;\n\nfn f() -> i32 {\n    1\n}\n", "f", "fn f() -> i32 {\n    1\n}"},
		{"a.ts", "export function f(a: number) {\n  return a;\n}\nconst g = 1;\n", "f", "export function f(a: number) {\n  return a;\n}"},
		{"a.js", "const f = () => {\n  return 1;\n};\n", "f", "const f = () => {\n  return 1;\n};"},
		{"a.c", "#include <x.h>\nint f(int a)\n{\n  return a;\n}\n", "f", "int f(int a)\n{\n  return a;\n}"},
	}
	for _, c := range cases {
		defs, ok := Definitions(c.path, []byte(c.src))
		if !ok {
			t.Fatalf("%s: not ok", c.path)
		}
		found := false
		for _, d := range defs {
			if d.Name == c.name {
				found = true
				if got := c.src[d.Start:d.End]; got != c.want {
					t.Errorf("%s: range = %q, want %q", c.path, got, c.want)
				}
			}
		}
		if !found {
			t.Errorf("%s: %s not found in %v", c.path, c.name, defs)
		}
	}
}

func TestSyntaxErrors(t *testing.T) {
	if n, ok := SyntaxErrors("a.py", []byte("def f():\n    return 1\n")); !ok || n != 0 {
		t.Fatalf("clean = %d %v", n, ok)
	}
	if n, ok := SyntaxErrors("a.rs", []byte("fn f( {\n")); !ok || n == 0 {
		t.Fatalf("broken = %d %v", n, ok)
	}
	if _, ok := SyntaxErrors("a.txt", nil); ok {
		t.Fatal("txt should be unsupported")
	}
}
