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

	"github.com/xmonader/janetcode/internal/project"
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
		if !project.IsTestFile(p) {
			t.Errorf("%s should be a test file", p)
		}
	}
	if project.IsTestFile("internal/x/x.go") {
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

// Changed works in a repository with no commit yet (every source file is
// new) and fails outside a repository rather than reporting no changes.
func TestChangedWithoutHEAD(t *testing.T) {
	root := t.TempDir()
	if out, err := exec.Command("git", "-C", root, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v %s", err, out)
	}
	if err := os.WriteFile(filepath.Join(root, "m.go"), []byte("package m\n\nvar X = 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	changed, err := Changed(context.Background(), root)
	if err != nil {
		t.Fatalf("no commit yet: %v", err)
	}
	if got := changed["m.go"]; len(got) != 3 {
		t.Fatalf("changed = %v, want the three lines of m.go", changed)
	}
	if _, err := Changed(context.Background(), t.TempDir()); err == nil {
		t.Fatal("outside a repository: no error")
	}
}

// An arithmetic identity is not a mutant: `x + 0` and `x - 0` are the same
// value, so the flip can never be killed. Generating one spends a whole
// test run and then reports a survivor the model is told to write a test
// for — a test that cannot exist.
func TestNoEquivalentArithmeticMutants(t *testing.T) {
	for _, line := range []string{"\tn = n + 0", "\tn = n - 0"} {
		for _, m := range Mutate("m.go", 1, line) {
			if m.From == "+" || m.From == "-" {
				t.Fatalf("%q produced the equivalent mutant %s", line, m)
			}
		}
	}
	// Only the right operand is symmetric: 0 + x and 0 - x differ, and a
	// plain `a + b` must still flip.
	for _, line := range []string{"\tn = 0 + n", "\tn = a + b"} {
		var got []Mutant
		for _, m := range Mutate("m.go", 1, line) {
			if m.From == "+" {
				got = append(got, m)
			}
		}
		if len(got) != 1 || got[0].To != "-" {
			t.Fatalf("%q should still mutate + to -, got %v", line, got)
		}
	}
}

// The mutant carries the line it was cut from. When the worktree's copy
// of that line is something else, mutating by line number would corrupt
// an unrelated line and score the result as if it meant something.
func TestRunnerSkipsAMutantWhoseLineMoved(t *testing.T) {
	root := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		if out, err := exec.Command("git", append([]string{"-C", root}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	run("init", "-q")
	run("config", "user.email", "t@example.com")
	run("config", "user.name", "t")
	if err := os.WriteFile(filepath.Join(root, "m.go"), []byte("package m\n\nfunc F(a, b int) bool { return a == b }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "-A")
	run("commit", "-qm", "base")
	// A mutant naming a line the tree does not have: line 1 is
	// "package m", not this.
	r := &Runner{Root: root, Command: "true", Timeout: 10 * time.Second}
	rep := Report{}
	_ = rep
	got, err := r.runMutants(context.Background(), []Mutant{{
		Path: "m.go", Line: 1, From: "==", To: "!=",
		Before: "func F(a, b int) bool { return a == b }",
		After:  "func F(a, b int) bool { return a != b }",
	}})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if got.Total != 0 || len(got.Survived) != 0 {
		t.Fatalf("a mutant whose line moved must not be scored: %+v", got)
	}
	data, rerr := os.ReadFile(filepath.Join(root, "m.go"))
	if rerr != nil || !strings.Contains(string(data), "a == b") {
		t.Fatalf("the source tree was touched: %q %v", data, rerr)
	}
}

// TestRunBoundsAPipeHeldByALeakedGrandchild is the Supervise fix: a test
// command that leaves something running (a server a package test starts
// and forgets) holds the output pipes open after the shell exits, and
// CombinedOutput's Wait used to block on them for as long as the orphan
// lived — past the per-mutant timeout and past cancellation. The runner
// now supervises the command: the group dies on cancel and the pipe wait
// itself is bounded.
func TestRunBoundsAPipeHeldByALeakedGrandchild(t *testing.T) {
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
	write("m.go", "package m\n\nfunc Positive(n int) bool { return n >= 0 }\n")

	r := &Runner{Root: root, Command: "go test ./...; sleep 30 &", Timeout: time.Minute, Max: 1}
	start := time.Now()
	if _, err := r.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(start); elapsed > 20*time.Second {
		t.Fatalf("run took %s; the leaked grandchild held the pipes past the command", elapsed)
	}
}

// A project that is a subdirectory of its repository is still mutated in
// place: git diff names files from the repository's top, ls-files from the
// directory it runs in, and the throwaway worktree is a checkout of the
// top. Mixing those up read the wrong files (or none) and ran the test
// command at the top of the checkout instead of in the project.
func TestRunInSubdirectoryOfRepository(t *testing.T) {
	top := t.TempDir()
	root := filepath.Join(top, "sub")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	run(t, top, "git", "init", "-q")
	if err := os.WriteFile(filepath.Join(root, "a.go"), []byte("package a\n\nvar A = 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(t, top, "git", "add", "-A")
	run(t, top, "git", "commit", "-qm", "base")
	// A tracked change and an untracked file, both in the subdirectory.
	if err := os.WriteFile(filepath.Join(root, "a.go"), []byte("package a\n\nvar A = 1 == 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "b.go"), []byte("package a\n\nvar B = 2 == 2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	changed, err := Changed(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if len(changed["a.go"]) != 1 || len(changed["b.go"]) != 3 || len(changed) != 2 {
		t.Fatalf("changed = %v, want a.go and b.go relative to the project", changed)
	}
	// The command fails only when it sees a mutant in its own directory,
	// so every mutant is killed exactly when it lands where it should.
	r := &Runner{Root: root, Command: "! grep -q '!=' a.go b.go", Timeout: 30 * time.Second}
	rep, err := r.Run(context.Background())
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if rep.Total != 2 || rep.Killed != 2 {
		t.Fatalf("report = %+v", rep)
	}
}

// The test command runs code the model wrote, the same as a verify
// command does, so it gets the same environment: without the agent's own
// credentials. The command here fails — kills the mutant — only when the
// key reached it.
func TestMutantCommandDoesNotSeeTheAgentsCredentials(t *testing.T) {
	root := t.TempDir()
	run(t, root, "git", "init", "-q")
	if err := os.WriteFile(filepath.Join(root, "m.go"), []byte("package m\n\nvar X = 1 == 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(t, root, "git", "add", "-A")
	run(t, root, "git", "commit", "-qm", "base")
	t.Setenv("ANTHROPIC_API_KEY", "sk-must-not-leak")
	r := &Runner{Root: root, Command: `test -z "$ANTHROPIC_API_KEY" && test "$CI" = true`, Timeout: 30 * time.Second}
	rep, err := r.runMutants(context.Background(), Mutate("m.go", 3, "var X = 1 == 1"))
	if err != nil {
		t.Fatal(err)
	}
	if rep.Total != 1 || rep.Killed != 0 {
		t.Fatalf("the mutant command saw the key or missed the scrubbed environment: %+v", rep)
	}
}
