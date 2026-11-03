package tool

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestSearchArgv(t *testing.T) {
	ok := map[string]bool{
		`grep -rn foo src`:                  true,
		`grep -rn "foo bar" src | head -20`: true,
		`rg 'a.b' | tail -n 5`:              true,
		`git grep -i foo`:                   true,
		`egrep -r foo . | head`:             true,
		`grep foo a | sort`:                 false,
		`grep foo a | head | tail`:          false,
		`grep foo a && echo`:                false,
		`grep foo a; ls`:                    false,
		`grep foo a > out`:                  false,
		`grep foo a 2>&1`:                   false,
		`grep "$(x)" a`:                     false,
		"grep `x` a":                        false,
		`grep foo *.go`:                     false,
		`grep foo src/?`:                    false,
		`FOO=1 grep foo a`:                  false,
		`grep foo a | head -n x`:            false,
		`git log`:                           false,
		`grep`:                              false,
		`grep 'unterminated`:                false,
	}
	for cmd, want := range ok {
		if _, _, got := searchArgv(cmd); got != want {
			t.Errorf("%q: got %v want %v", cmd, got, want)
		}
	}
	if argv, piped, _ := searchArgv(`rg "a b" -g '*.go' | head -3`); !piped || !reflect.DeepEqual(argv, []string{"rg", "a b", "-g", "*.go"}) {
		t.Errorf("argv %q piped %v", argv, piped)
	}
}

func TestSearchProbes(t *testing.T) {
	names := func(argv ...string) []string {
		var out []string
		for _, p := range searchProbes(argv) {
			out = append(out, strings.Join(p.argv, " "))
		}
		return out
	}
	got := names("grep", "-rn", "foo.bar", "--include=*.go", "src")
	want := []string{
		"grep -i -rn foo.bar --include=*.go src",
		"grep -F -rn foo.bar --include=*.go src",
		"grep -rn foo.bar src",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("grep: %q", got)
	}
	got = names("rg", "-i", "-t", "go", "foo", "-g", "!x")
	if !reflect.DeepEqual(got, []string{"rg -i foo"}) {
		t.Errorf("rg: %q", got)
	}
	got = names("git", "grep", "-e", "a(b")
	if !reflect.DeepEqual(got, []string{"git grep -i -e a(b", "git grep -F -e a(b"}) {
		t.Errorf("git: %q", got)
	}
	got = names("grep", "-A", "3", "x+", "f")
	if len(got) != 2 || got[1] != "grep -F -A 3 x+ f" {
		t.Errorf("value opt: %q", got)
	}
	for _, c := range [][]string{
		{"grep", "-q", "x", "f"}, {"grep", "-c", "x", "f"}, {"grep", "-f", "pats", "f"},
		{"grep", "-ie"}, {"grep", "-rc", "x"}, {"rg", "--files", "-g", "x"},
	} {
		if p := searchProbes(c); p != nil {
			t.Errorf("%q: want none, got %v", c, p)
		}
	}
}

func TestBashEmptyGrepProbes(t *testing.T) {
	if _, err := exec.LookPath("grep"); err != nil {
		t.Skip("no grep")
	}
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "a.txt"), []byte("Hello World\nsum x[1]\n"), 0o644)
	run := func(b Bash, cmd string) string {
		out, err := b.Run(context.Background(), func() json.RawMessage { j, _ := json.Marshal(map[string]string{"command": cmd}); return j }())
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	on := Bash{Root: root, Probe: &EmptyProbe{}}
	out := run(on, `grep -n hello a.txt`)
	if !strings.Contains(out, "[exit code 1]") || !strings.Contains(out, "[empty search probe] Case-insensitive, the same search matches 1 lines, e.g.:\n  1:Hello World") {
		t.Fatalf("bare: %q", out)
	}
	out = run(on, `grep 'x[1]' a.txt | head -5`)
	if !strings.Contains(out, "As a literal string") || !strings.Contains(out, "sum x[1]") {
		t.Fatalf("piped: %q", out)
	}
	if out = run(on, `grep -n hello a.txt | sort`); strings.Contains(out, "probe") {
		t.Fatalf("other pipe probed: %q", out)
	}
	if out = run(on, `grep -n zzz a.txt`); strings.Contains(out, "probe") {
		t.Fatalf("no variant matches: %q", out)
	}
	if out = run(Bash{Root: root}, `grep -n hello a.txt`); strings.Contains(out, "probe") {
		t.Fatalf("nil Probe probed: %q", out)
	}
	if out = run(on, `grep -n hello missing.txt`); strings.Contains(out, "probe") {
		t.Fatalf("error exit probed: %q", out)
	}
	spent := Bash{Root: root, Probe: &EmptyProbe{Max: 1}}
	run(spent, `grep -n zzz a.txt`)
	if out = run(spent, `grep -n hello a.txt`); strings.Contains(out, "probe") {
		t.Fatalf("budget not shared: %q", out)
	}
}
