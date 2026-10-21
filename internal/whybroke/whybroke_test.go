package whybroke

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xmonader/janetcode/internal/change"
	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/proof"
	"github.com/xmonader/janetcode/internal/session"
	"github.com/xmonader/janetcode/internal/snapshot"
)

// whyGit runs git in dir for a fixture.
func whyGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t", "GIT_CONFIG_GLOBAL=/dev/null")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func whyWrite(t *testing.T, root, rel, body string) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	os.MkdirAll(filepath.Dir(p), 0o755)
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// whyFixture is a recorded session over a repository: its start state,
// the turns' prompts, and the provenance record, written the way a real
// session leaves them on disk.
type whyFixture struct {
	t       *testing.T
	root    string
	repo    *snapshot.Repo
	start   snapshot.Snap
	changes []change.Change
	cps     []checkpoint
	prompts []string
}

func newWhyFixture(t *testing.T, files map[string]string) *whyFixture {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not on PATH")
	}
	root := t.TempDir()
	whyGit(t, root, "init", "-q")
	for rel, body := range files {
		whyWrite(t, root, rel, body)
	}
	whyGit(t, root, "add", "-A")
	whyGit(t, root, "commit", "-q", "-m", "base")
	repo, err := snapshot.Open(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	start, err := repo.Take(context.Background(), "start")
	if err != nil {
		t.Fatal(err)
	}
	return &whyFixture{t: t, root: repo.Root, repo: repo, start: start}
}

// turn starts a new turn with prompt, snapshotting the tree first the way
// the agent does before a turn's first change.
func (f *whyFixture) turn(prompt string) {
	f.prompts = append(f.prompts, prompt)
	snap, err := f.repo.Take(context.Background(), "turn")
	if err != nil {
		f.t.Fatal(err)
	}
	f.cps = append(f.cps, checkpoint{Turn: len(f.prompts), Snap: &snap})
}

// edit is a file tool's change, recorded in provenance.
func (f *whyFixture) edit(rel, body, intent string) {
	p := filepath.Join(f.root, rel)
	before, err := os.ReadFile(p)
	f.changes = append(f.changes, change.Change{Seq: len(f.changes) + 1, Turn: len(f.prompts), Call: "c", Tool: "edit_file", Intent: intent,
		Path: p, Existed: err == nil, Before: before, After: []byte(body)})
	whyWrite(f.t, f.root, rel, body)
}

// save writes the session log and undo sidecar and returns the log path.
func (f *whyFixture) save(id string) string {
	dir := f.t.TempDir()
	var lines []string
	meta, _ := json.Marshal(map[string]any{"type": "meta", "meta": session.Meta{ID: id, Cwd: f.root,
		Start: &session.StartState{Head: f.start.Head, Snap: f.start.Commit}}})
	lines = append(lines, string(meta))
	for i, p := range f.prompts {
		for _, e := range []session.Event{
			{Type: session.TypeTurnStart, Turn: i + 1},
			{Type: session.TypeUserMessage, Turn: i + 1, Message: ptr(llm.TextMessage(llm.RoleUser, p))},
		} {
			b, _ := json.Marshal(e)
			lines = append(lines, `{"type":"event",`+string(b[1:]))
		}
	}
	path := filepath.Join(dir, id+".jsonl")
	os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600)
	undo, _ := json.Marshal(map[string]any{"checkpoints": f.cps, "changes": f.changes})
	os.WriteFile(filepath.Join(dir, id+".undo.json"), undo, 0o600)
	return path
}

func ptr[T any](v T) *T { return &v }

const whyCalc = "package calc\n\nfunc Add(a, b int) int { return a + b }\n\nfunc Neg(a int) int { return -a }\n"

const whyCalcTest = `package calc

import "testing"

func TestAdd(t *testing.T) {
	if Add(2, 3) != 5 {
		t.Fatal("Add")
	}
}

func TestNeg(t *testing.T) {
	if Neg(4) != -4 {
		t.Fatal("Neg(4) is not -4")
	}
}
`

// A regression seeded inside a three-turn session that landed as one
// squashed commit: the bisection names the change, its turn and intent,
// and the commit it is inside.
func TestWhyBrokeFindsTheChangeInsideASquashedCommit(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go is not on PATH")
	}
	f := newWhyFixture(t, map[string]string{"go.mod": "module example.com/calc\n\ngo 1.21\n", "calc.go": whyCalc, "calc_test.go": whyCalcTest})
	f.turn("add Mul")
	f.edit("calc.go", whyCalc+"\nfunc Mul(a, b int) int { return a * b }\n", "add Mul")
	f.edit("calc_test.go", whyCalcTest+"\nfunc TestMul(t *testing.T) {\n\tif Mul(2, 3) != 6 {\n\t\tt.Fatal(\"Mul\")\n\t}\n}\n", "test Mul")
	f.turn("tidy the arithmetic helpers")
	broken := strings.Replace(whyCalc, "return -a", "return a", 1) + "\nfunc Mul(a, b int) int { return a * b }\n"
	f.edit("calc.go", broken, "simplify Neg: callers negate themselves")
	f.edit("README.md", "calc\n", "document the package")
	f.turn("add Sub")
	f.edit("calc.go", broken+"\nfunc Sub(a, b int) int { return a - b }\n", "add Sub")
	id := "20261010-000000-why001"
	path := f.save(id)

	whyGit(t, f.root, "add", "-A")
	whyGit(t, f.root, "commit", "-q", "-m", "squashed: Mul, tidy, Sub")
	head := whyGit(t, f.root, "rev-parse", "HEAD")
	var rec proof.Record
	rec.Version, rec.Session = proof.Version, id
	for _, c := range f.changes {
		rec.Files = append(rec.Files, proof.File{Path: relTo(f.root, c.Path), Changes: []proof.ChangeRef{{Seq: c.Seq, Turn: c.Turn, Tool: c.Tool}}})
	}
	rec.Turns = []proof.Turn{{Turn: 1}, {Turn: 2}, {Turn: 3}}
	if err := proof.Attach(context.Background(), f.root, head, rec); err != nil {
		t.Fatal(err)
	}

	r, err := Run(context.Background(), Options{Test: "TestNeg", SessionPath: path})
	if err != nil {
		t.Fatal(err)
	}
	k := r.Culprit
	if r.Mode != "changes" || k == nil || k.Seq != 3 || k.Turn != 2 {
		t.Fatalf("report %+v culprit %+v\n%s", r, k, r.Text())
	}
	if k.Intent != "simplify Neg: callers negate themselves" || k.Prompt != "tidy the arithmetic helpers" || k.Commit != head || k.Squashed != 3 {
		t.Fatalf("culprit %+v", k)
	}
	if !strings.Contains(k.Output, "Neg(4) is not -4") || !strings.Contains(k.Diff, "-func Neg(a int) int { return -a }") {
		t.Errorf("output %q diff %q", k.Output, k.Diff)
	}
	// 6 states (start + 5 changes): both ends, then at most 3 more.
	if len(r.Probes) > 5 {
		t.Errorf("%d probes for 6 states", len(r.Probes))
	}
	if txt := r.Text(); !strings.Contains(txt, "squashes 3 turns") || !strings.Contains(txt, "turn 2 asked: tidy the arithmetic helpers") {
		t.Errorf("text:\n%s", txt)
	}
}

// A break no file tool recorded (a shell command) moves the search to the
// per-turn snapshots, and names the turn.
func TestWhyBrokeFallsBackToTurnSnapshotsForAShellChange(t *testing.T) {
	check := "grep -q ok value.txt\n"
	f := newWhyFixture(t, map[string]string{"value.txt": "ok\n", "check.sh": check})
	f.turn("write notes")
	f.edit("notes.txt", "n\n", "notes")
	f.turn("regenerate the value")
	whyWrite(t, f.root, "value.txt", "bad\n") // a shell command: no Change
	f.edit("gen.txt", "g\n", "record the generator")
	f.turn("more notes")
	f.edit("notes.txt", "n2\n", "more")
	path := f.save("20261010-000000-why002")

	r, err := Run(context.Background(), Options{Test: "sh check.sh", SessionPath: path})
	if err != nil {
		t.Fatal(err)
	}
	if r.Mode != "turns" || r.Culprit == nil || r.Culprit.Turn != 2 || r.Culprit.Prompt != "regenerate the value" {
		t.Fatalf("report %+v culprit %+v\n%s", r, r.Culprit, r.Text())
	}
	// The work-tree snapshot it took is gone again.
	if refs := whyGit(t, f.root, "for-each-ref", snapshot.RefPrefix); strings.Count(refs, "\n")+1 > len(f.cps)+1 {
		t.Errorf("a snapshot ref was left behind:\n%s", refs)
	}
}

func TestWhyBrokePassingAndPreexisting(t *testing.T) {
	f := newWhyFixture(t, map[string]string{"value.txt": "ok\n"})
	f.turn("t")
	f.edit("other.txt", "x\n", "x")
	path := f.save("20261010-000000-why003")
	r, err := Run(context.Background(), Options{Test: "grep -q ok value.txt", SessionPath: path})
	if err != nil || r.Culprit != nil || r.Verdict != "passes" {
		t.Fatalf("passing test: %+v %v", r, err)
	}
	r, err = Run(context.Background(), Options{Test: "grep -q nope value.txt", SessionPath: path})
	if err != nil || r.Culprit != nil || r.Verdict != "failed at start" {
		t.Fatalf("pre-existing failure: %+v %v", r, err)
	}
	if r.Mode != "changes" {
		t.Errorf("a failure at the start moved on to %s", r.Mode)
	}
}

func TestMaterializeRewindsLaterChanges(t *testing.T) {
	dir := t.TempDir()
	whyWrite(t, dir, "a.txt", "v0\n")
	cs := []change.Change{
		{Seq: 1, Path: "/r/a.txt", Existed: true, Before: []byte("v0\n"), After: []byte("v1\n")},
		{Seq: 2, Path: "/r/new.txt", Before: nil, After: []byte("n\n")},
		{Seq: 3, Path: "/r/a.txt", Existed: true, Before: []byte("v1\n"), After: []byte("v3\n")},
		{Seq: 4, Path: "/r/new.txt", Existed: true, Before: []byte("n\n"), Gone: true},
	}
	for k, want := range map[int][2]string{0: {"v0\n", ""}, 1: {"v1\n", ""}, 2: {"v1\n", "n\n"}, 3: {"v3\n", "n\n"}, 4: {"v3\n", ""}} {
		whyWrite(t, dir, "a.txt", "junk")
		if err := materialize("/r", dir, cs, k); err != nil {
			t.Fatal(err)
		}
		a, _ := os.ReadFile(filepath.Join(dir, "a.txt"))
		n, _ := os.ReadFile(filepath.Join(dir, "new.txt"))
		if string(a) != want[0] || string(n) != want[1] {
			t.Errorf("k=%d: a=%q new=%q, want %q", k, a, n, want)
		}
	}
}

func TestCommandForms(t *testing.T) {
	goRoot := t.TempDir()
	whyWrite(t, goRoot, "go.mod", "module m\n")
	pyRoot := t.TempDir()
	whyWrite(t, pyRoot, "test_a.py", "")
	for _, tc := range []struct{ root, test, want string }{
		{goRoot, "TestNeg", "go test -count=1 -run '^TestNeg$' ./..."},
		{goRoot, "TestNeg/zero", "go test -count=1 -run '^TestNeg$/^zero$' ./..."},
		{pyRoot, "test_a.py::test_b", "python3 -m pytest -q 'test_a.py::test_b'"},
		{pyRoot, "test_b", "python3 -m pytest -q -k 'test_b'"},
		{t.TempDir(), "npm test -- -t parses", "npm test -- -t parses"},
	} {
		got, err := Command(tc.root, tc.test)
		if err != nil || got != tc.want {
			t.Errorf("Command(%q) = %q, %v; want %q", tc.test, got, err, tc.want)
		}
	}
	for _, bad := range []string{"", "TestX;rm", "parses"} {
		if _, err := Command(t.TempDir(), bad); err == nil {
			t.Errorf("Command(%q) accepted", bad)
		}
	}
}

// The per-probe timeout is a real bound: a test that leaves a child
// holding its output open (a server it started and forgot) used to keep
// the probe waiting on the pipe long after the timeout killed the shell.
func TestRunTestTimeoutIsABound(t *testing.T) {
	start := time.Now()
	failed, out := runTest(context.Background(), t.TempDir(), "sleep 20 & sleep 20", 200*time.Millisecond)
	if took := time.Since(start); took > 10*time.Second {
		t.Fatalf("runTest took %s past a 200ms timeout", took)
	}
	if !failed || !strings.Contains(out, "timed out") {
		t.Fatalf("failed=%v out=%q", failed, out)
	}
}
