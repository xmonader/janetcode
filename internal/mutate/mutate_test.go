package mutate

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xmonader/janetcode/internal/session"
)

func TestMutateFlipsOperatorsOutsideStringsAndComments(t *testing.T) {
	ms := Mutate("a.go", 7, `	if n <= max && s != "a == b" { // x == y`)
	var got []string
	for _, m := range ms {
		got = append(got, m.From+"→"+m.To)
	}
	want := "!=→==, <=→<, &&→||" // operator table order
	if strings.Join(got, ", ") != want {
		t.Fatalf("mutations = %v, want %s", got, want)
	}
	if ms[0].After != `if n <= max && s == "a == b" { // x == y` {
		t.Fatalf("after = %q", ms[0].After)
	}
	if len(Mutate("a.go", 1, "// a == b")) != 0 || len(Mutate("a.py", 1, "# a == b")) != 0 {
		t.Fatal("comments mutated")
	}
	if ms := Mutate("a.go", 1, "x := a + b - c"); len(ms) != 2 || ms[0].To != "-" || ms[1].To != "+" {
		t.Fatalf("arithmetic: %+v", ms)
	}
	if ms := Mutate("a.go", 1, "ch <- v; x := 1 << 2; return a < b"); len(ms) != 1 || ms[0].From != "<" {
		t.Fatalf("channel/shift guarded: %+v", ms)
	}
	if ms := Mutate("a.py", 1, "ok = True if a >= b else False"); len(ms) != 3 {
		t.Fatalf("python: %+v", ms)
	}
	for _, p := range []string{"x_test.go", "test_x.py", "a.spec.ts", "testdata/x.go", "pkg/tests/x.go"} {
		if !IsTestFile(p) {
			t.Errorf("%s should be a test file", p)
		}
	}
	if IsTestFile("internal/x/x.go") {
		t.Error("x.go is not a test file")
	}
}

func TestReportEvent(t *testing.T) {
	rep := Report{Command: "go test ./...", Total: 9, Killed: 7, Survived: []Outcome{{}, {}}}
	e := rep.Event()
	if e.Type != session.TypeMutation {
		t.Fatalf("type = %q, want %q", e.Type, session.TypeMutation)
	}
	var got struct {
		Killed   int    `json:"killed"`
		Survived int    `json:"survived"`
		Total    int    `json:"total"`
		Command  string `json:"command"`
	}
	if err := json.Unmarshal(e.Extra, &got); err != nil {
		t.Fatal(err)
	}
	if got.Killed != 7 || got.Survived != 2 || got.Total != 9 || got.Command != "go test ./..." {
		t.Fatalf("extra = %+v", got)
	}
}

func run(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t", "HOME="+dir)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v: %v\n%s", args, err, out)
	}
}

// TestRunFindsUntestedBranch builds a tiny Go module where the tests pin
// one comparison but not the other, and checks the survivor is reported.
func TestRunFindsUntestedBranch(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("no go toolchain")
	}
	root := t.TempDir()
	write := func(name, body string) {
		os.MkdirAll(filepath.Dir(filepath.Join(root, name)), 0o755)
		if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("go.mod", "module example.com/m\n\ngo 1.22\n")
	write("m.go", "package m\n\nfunc Positive(n int) bool { return n > 0 }\n")
	write("m_test.go", "package m\n\nimport \"testing\"\n\nfunc TestPositive(t *testing.T) {\n\tif !Positive(3) || Positive(-1) {\n\t\tt.Fatal(\"wrong\")\n\t}\n}\n")
	run(t, root, "git", "init", "-q")
	run(t, root, "git", "add", "-A")
	run(t, root, "git", "commit", "-qm", "base")

	// The agent's change: Big is tested only away from the boundary, so
	// flipping > to >= survives; && to || is caught.
	write("m.go", "package m\n\nfunc Positive(n int) bool { return n > 0 }\n\n// Big says whether both are large.\nfunc Big(a, b int) bool {\n\treturn a > 10 && b > 10\n}\n")
	write("m_test.go", "package m\n\nimport \"testing\"\n\nfunc TestPositive(t *testing.T) {\n\tif !Positive(3) || Positive(-1) {\n\t\tt.Fatal(\"wrong\")\n\t}\n}\n\nfunc TestBig(t *testing.T) {\n\tif !Big(20, 20) || Big(20, 1) || Big(1, 20) {\n\t\tt.Fatal(\"wrong\")\n\t}\n}\n")
	write("extra.go", "package m\n\nfunc Same(a, b int) bool { return a == b }\n")

	changed, err := Changed(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if lines := changed["m.go"]; len(lines) == 0 || lines[0] != 4 {
		t.Fatalf("changed = %v", changed)
	}
	if lines := changed["extra.go"]; len(lines) != 3 {
		t.Fatalf("untracked file lines = %v", changed["extra.go"])
	}
	if _, ok := changed["m_test.go"]; ok {
		t.Fatal("test file should be excluded")
	}

	var logs []string
	r := &Runner{Root: root, Command: "go test ./...", Timeout: 2 * time.Minute, Log: func(s string) { logs = append(logs, s) }}
	rep, err := r.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if rep.Total < 4 || rep.OK() {
		t.Fatalf("report = %+v\n%s", rep, strings.Join(logs, "\n"))
	}
	var survived []string
	for _, o := range rep.Survived {
		survived = append(survived, o.Mutant.String())
	}
	joined := strings.Join(survived, "\n")
	if !strings.Contains(joined, "m.go:7: `>` → `>=`") || !strings.Contains(joined, "extra.go:3: `==` → `!=`") {
		t.Fatalf("survivors:\n%s\nlogs:\n%s", joined, strings.Join(logs, "\n"))
	}
	if strings.Contains(joined, "`&&` → `||`") {
		t.Fatalf("&& mutant should be killed:\n%s", joined)
	}
	if fb := rep.Feedback(); !strings.Contains(fb, "do not catch") || !strings.Contains(fb, "was: return a > 10 && b > 10") {
		t.Fatalf("feedback:\n%s", fb)
	}
	if !strings.Contains(rep.Summary(), "mutants killed") {
		t.Fatal(rep.Summary())
	}
	// The real tree was not touched and no worktree is left behind.
	if data, _ := os.ReadFile(filepath.Join(root, "m.go")); !strings.Contains(string(data), "a > 10 && b > 10") {
		t.Fatal("source tree modified")
	}
	out, _ := exec.Command("git", "-C", root, "worktree", "list").Output()
	if strings.Count(strings.TrimSpace(string(out)), "\n") != 0 {
		t.Fatalf("worktree left behind:\n%s", out)
	}
	// Nothing changed → skipped, not failed.
	run(t, root, "git", "add", "-A")
	run(t, root, "git", "commit", "-qm", "all")
	rep, err = r.Run(context.Background())
	if err != nil || rep.Skipped == "" || !rep.OK() {
		t.Fatalf("clean tree: %+v %v", rep, err)
	}
}
