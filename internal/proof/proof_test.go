package proof

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/gitx/gitxtest"
)

func initRepo(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	gitxtest.InitRepo(t, root)
	return root
}

func write(t *testing.T, root, rel, content string) {
	t.Helper()
	p := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func commit(t *testing.T, root, msg string) string {
	t.Helper()
	run := func(args ...string) string {
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	run("add", "-A")
	run("commit", "-q", "-m", msg)
	return run("rev-parse", "HEAD")
}

func TestBuildFiltersToCommittedFiles(t *testing.T) {
	root := t.TempDir()
	changes := []ChangeInput{
		{Seq: 1, Turn: 1, Tool: "write_file", Intent: "add foo", Path: filepath.Join(root, "foo.go"), Added: 3,
			Before: nil, After: []byte("line1\n")},
		{Seq: 2, Turn: 1, Tool: "edit_file", Intent: "tweak foo", Path: filepath.Join(root, "foo.go"), Added: 1, Removed: 1,
			Before: []byte("line1\n"), After: []byte("line1\nline2\n")},
		{Seq: 3, Turn: 2, Tool: "write_file", Intent: "scratch file, not committed", Path: filepath.Join(root, "scratch.go"), Added: 2,
			Before: nil, After: []byte("scratch\n")},
	}
	outcomes := map[int]TurnOutcomeInput{
		1: {Verdict: "verified", Why: "tests ran and passed", Tested: true},
		2: {Verdict: "unverified", Why: "nothing was run"},
	}
	committed := map[string]FileVersions{"foo.go": {Commit: []byte("line1\nline2\n")}}
	r := Build("sess-1", "test-model", root, changes, outcomes, committed)
	if r.Empty() {
		t.Fatal("expected a non-empty record")
	}
	if len(r.Files) != 1 || r.Files[0].Path != "foo.go" {
		t.Fatalf("files = %+v, want just foo.go", r.Files)
	}
	if len(r.Files[0].Changes) != 2 {
		t.Fatalf("changes = %+v, want 2", r.Files[0].Changes)
	}
	if len(r.Turns) != 1 || r.Turns[0].Turn != 1 || r.Turns[0].Verdict != "verified" {
		t.Fatalf("turns = %+v, want only turn 1 verified", r.Turns)
	}
}

func TestBuildEmptyWhenNothingLanded(t *testing.T) {
	root := t.TempDir()
	changes := []ChangeInput{{Seq: 1, Turn: 1, Tool: "write_file", Path: filepath.Join(root, "other.go"), After: []byte("x\n")}}
	committed := map[string]FileVersions{"foo.go": {Commit: []byte("y\n")}}
	r := Build("sess-1", "m", root, changes, nil, committed)
	if !r.Empty() {
		t.Fatalf("expected empty record, got %+v", r)
	}
}

// TestBuildExcludesAnEarlierCommitsChange is defect #2's regression test:
// a file edited in turn 1 (committed by commit A) and edited again in
// turn 3 (committed by commit B) must show only turn 3's change in B's
// record — turn 1's change already belongs to A, and sharing a path with
// B is not enough to put it there too.
func TestBuildExcludesAnEarlierCommitsChange(t *testing.T) {
	root := t.TempDir()
	changes := []ChangeInput{
		{Seq: 1, Turn: 1, Tool: "write_file", Intent: "turn 1: create foo", Path: filepath.Join(root, "foo.go"),
			Before: nil, After: []byte("one\ntwo\nthree\n")},
		{Seq: 2, Turn: 3, Tool: "edit_file", Intent: "turn 3: add four", Path: filepath.Join(root, "foo.go"),
			Before: []byte("one\ntwo\nthree\n"), After: []byte("one\ntwo\nthree\nfour\n")},
	}
	outcomes := map[int]TurnOutcomeInput{
		1: {Verdict: "verified", Why: "commit A"},
		3: {Verdict: "verified", Why: "commit B"},
	}
	// Commit A: parent is empty (nothing existed yet), its own version is
	// exactly what turn 1 wrote.
	commitA := map[string]FileVersions{"foo.go": {Commit: []byte("one\ntwo\nthree\n")}}
	rA := Build("sess-1", "m", root, changes, outcomes, commitA)
	if len(rA.Files) != 1 || len(rA.Files[0].Changes) != 1 || rA.Files[0].Changes[0].Seq != 1 {
		t.Fatalf("commit A = %+v, want only turn 1's change", rA.Files)
	}
	// Commit B: parent is what turn 1 already committed (commit A's
	// version), its own version is what turn 3 left behind.
	commitB := map[string]FileVersions{"foo.go": {
		Parent: []byte("one\ntwo\nthree\n"),
		Commit: []byte("one\ntwo\nthree\nfour\n"),
	}}
	rB := Build("sess-1", "m", root, changes, outcomes, commitB)
	if len(rB.Files) != 1 || len(rB.Files[0].Changes) != 1 || rB.Files[0].Changes[0].Seq != 2 {
		t.Fatalf("commit B = %+v, want only turn 3's change", rB.Files)
	}
}

func TestAttachReadRoundTrip(t *testing.T) {
	ctx := context.Background()
	root := initRepo(t)
	write(t, root, "foo.go", "package foo\n")
	sha := commit(t, root, "add foo")

	versions, err := CommittedFileVersions(ctx, root, sha, []string{"foo.go"})
	if err != nil {
		t.Fatalf("CommittedFileVersions: %v", err)
	}
	r := Build("sess-1", "model-a", root,
		[]ChangeInput{{Seq: 1, Turn: 1, Tool: "write_file", Intent: "add foo", Path: filepath.Join(root, "foo.go"), Added: 1,
			Before: nil, After: []byte("package foo\n")}},
		map[int]TurnOutcomeInput{1: {Verdict: "verified", Why: "built"}},
		versions)
	if err := Attach(ctx, root, sha, r); err != nil {
		t.Fatalf("Attach: %v", err)
	}
	got, ok, err := Read(ctx, root, sha)
	if err != nil || !ok {
		t.Fatalf("Read: ok=%v err=%v", ok, err)
	}
	if got.Session != "sess-1" || len(got.Files) != 1 || got.Files[0].Path != "foo.go" {
		t.Fatalf("round trip mismatch: %+v", got)
	}
	summary := Summary(got)
	if !strings.Contains(summary, "foo.go") || !strings.Contains(summary, "verified") {
		t.Fatalf("summary missing expected content: %s", summary)
	}
}

// TestAttachSurvivesAmend is defect #5's regression test: attaching a
// note configures git (see EnsureRewriteConfig) to copy a note onto a
// rewritten commit, so `git commit --amend` does not silently drop it.
func TestAttachSurvivesAmend(t *testing.T) {
	ctx := context.Background()
	root := initRepo(t)
	write(t, root, "foo.go", "package foo\n")
	sha := commit(t, root, "add foo")

	r := Build("sess-1", "model-a", root,
		[]ChangeInput{{Seq: 1, Turn: 1, Tool: "write_file", Intent: "add foo", Path: filepath.Join(root, "foo.go"), Added: 1,
			Before: nil, After: []byte("package foo\n")}},
		map[int]TurnOutcomeInput{1: {Verdict: "verified", Why: "built"}},
		map[string]FileVersions{"foo.go": {Commit: []byte("package foo\n")}})
	if err := Attach(ctx, root, sha, r); err != nil {
		t.Fatalf("Attach: %v", err)
	}

	gitIn(t, root, "commit", "--amend", "-q", "-m", "add foo (amended)")
	amended := gitIn(t, root, "rev-parse", "HEAD")
	if amended == sha {
		t.Fatal("amend did not move HEAD")
	}

	got, ok, err := Read(ctx, root, amended)
	if err != nil || !ok {
		t.Fatalf("Read(amended HEAD): ok=%v err=%v -- the note did not survive the amend", ok, err)
	}
	if got.Session != "sess-1" || len(got.Files) != 1 || got.Files[0].Path != "foo.go" {
		t.Fatalf("note on amended commit = %+v", got)
	}
}

// TestEnsureRewriteConfigLeavesAnExplicitSettingAlone checks the other
// half of defect #5's fix: EnsureRewriteConfig must not clobber a
// notes.rewrite.amend the user (or another tool) already set on purpose.
func TestEnsureRewriteConfigLeavesAnExplicitSettingAlone(t *testing.T) {
	ctx := context.Background()
	root := initRepo(t)
	gitIn(t, root, "config", "notes.rewrite.amend", "false")

	if err := EnsureRewriteConfig(ctx, root); err != nil {
		t.Fatalf("EnsureRewriteConfig: %v", err)
	}
	if got := gitIn(t, root, "config", "--get", "notes.rewrite.amend"); got != "false" {
		t.Fatalf("notes.rewrite.amend = %q, want the explicit false left alone", got)
	}
	if got := gitIn(t, root, "config", "--get", "notes.rewrite.rebase"); got != "true" {
		t.Fatalf("notes.rewrite.rebase = %q, want janetcode's default of true", got)
	}
	if got := gitIn(t, root, "config", "--get-all", "notes.rewriteRef"); !strings.Contains(got, NotesRef) {
		t.Fatalf("notes.rewriteRef = %q, want it to include %s", got, NotesRef)
	}

	// A second call must not duplicate notes.rewriteRef.
	if err := EnsureRewriteConfig(ctx, root); err != nil {
		t.Fatalf("EnsureRewriteConfig (second call): %v", err)
	}
	out := gitIn(t, root, "config", "--get-all", "notes.rewriteRef")
	if n := strings.Count(out, NotesRef); n != 1 {
		t.Fatalf("notes.rewriteRef has %d copies of %s, want 1: %q", n, NotesRef, out)
	}
}

func TestReadNoNote(t *testing.T) {
	ctx := context.Background()
	root := initRepo(t)
	write(t, root, "foo.go", "package foo\n")
	sha := commit(t, root, "add foo")
	_, ok, err := Read(ctx, root, sha)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if ok {
		t.Fatal("expected no note on a fresh commit")
	}
}

func TestAttachMergesTwoSessions(t *testing.T) {
	ctx := context.Background()
	root := initRepo(t)
	write(t, root, "foo.go", "package foo\n")
	sha := commit(t, root, "add foo")

	versions, err := CommittedFileVersions(ctx, root, sha, []string{"foo.go"})
	if err != nil {
		t.Fatalf("CommittedFileVersions: %v", err)
	}
	r1 := Build("sess-1", "model-a", root,
		[]ChangeInput{{Seq: 1, Turn: 1, Tool: "write_file", Intent: "first pass", Path: filepath.Join(root, "foo.go"), Added: 1,
			Before: nil, After: []byte("package foo\n")}},
		map[int]TurnOutcomeInput{1: {Verdict: "verified", Why: "built"}},
		versions)
	if err := Attach(ctx, root, sha, r1); err != nil {
		t.Fatalf("attach 1: %v", err)
	}
	r2 := Build("sess-2", "model-b", root,
		[]ChangeInput{{Seq: 1, Turn: 1, Tool: "edit_file", Intent: "second pass", Path: filepath.Join(root, "foo.go"), Added: 2,
			Before: nil, After: []byte("package foo\n")}},
		map[int]TurnOutcomeInput{1: {Verdict: "verified", Why: "tests passed"}},
		versions)
	if err := Attach(ctx, root, sha, r2); err != nil {
		t.Fatalf("attach 2: %v", err)
	}
	got, ok, err := Read(ctx, root, sha)
	if err != nil || !ok {
		t.Fatalf("Read: ok=%v err=%v", ok, err)
	}
	if len(got.Files) != 1 {
		t.Fatalf("expected one file entry, got %d", len(got.Files))
	}
	if len(got.Files[0].Changes) != 2 {
		t.Fatalf("expected both sessions' changes to survive the merge, got %+v", got.Files[0].Changes)
	}
	sessions := map[string]bool{}
	for _, c := range got.Files[0].Changes {
		sessions[c.Session] = true
	}
	if !sessions["sess-1"] || !sessions["sess-2"] {
		t.Fatalf("expected both sessions represented, got %+v", got.Files[0].Changes)
	}
	if len(got.Turns) != 2 {
		t.Fatalf("expected both sessions' turn 1 to show separately, got %+v", got.Turns)
	}
}

// A merged record lists turns in turn order. The key was a string, so
// "10" sorted before "2" and a session that ran more than nine turns had
// its proof note reordered into something no reader expects.
func TestMergeOrdersTurnsNumerically(t *testing.T) {
	var a, b Record
	a.Session = "s"
	b.Session = "s"
	a.Files = []File{{Path: "x.go", Changes: []ChangeRef{{Seq: 1, Turn: 1}}}}
	b.Files = a.Files
	for _, n := range []int{1, 2, 9, 10, 11, 20} {
		if n%2 == 0 {
			a.Turns = append(a.Turns, Turn{Turn: n, Verdict: "verified"})
		} else {
			b.Turns = append(b.Turns, Turn{Turn: n, Verdict: "verified"})
		}
	}
	got := merge(a, b)
	var order []int
	for _, tn := range got.Turns {
		order = append(order, tn.Turn)
	}
	want := []int{1, 2, 9, 10, 11, 20}
	if len(order) != len(want) {
		t.Fatalf("turns = %v, want %v", order, want)
	}
	for i := range want {
		if order[i] != want[i] {
			t.Fatalf("turns = %v, want %v", order, want)
		}
	}
}

func TestCommittedFilesIncludingRoot(t *testing.T) {
	ctx := context.Background()
	root := initRepo(t)
	write(t, root, "a.go", "package a\n")
	write(t, root, "b.go", "package b\n")
	rootSHA := commit(t, root, "root commit")
	files, err := CommittedFiles(ctx, root, rootSHA)
	if err != nil {
		t.Fatalf("CommittedFiles(root): %v", err)
	}
	got := map[string]bool{}
	for _, f := range files {
		got[f] = true
	}
	if !got["a.go"] || !got["b.go"] {
		t.Fatalf("root commit files = %v, want a.go and b.go", files)
	}

	write(t, root, "a.go", "package a\n\nfunc F() {}\n")
	sha2 := commit(t, root, "change a")
	files2, err := CommittedFiles(ctx, root, sha2)
	if err != nil {
		t.Fatalf("CommittedFiles: %v", err)
	}
	if len(files2) != 1 || files2[0] != "a.go" {
		t.Fatalf("second commit files = %v, want just a.go", files2)
	}
}

func TestBlameCommittedAttribution(t *testing.T) {
	ctx := context.Background()
	root := initRepo(t)
	write(t, root, "foo.go", "package foo\n\nfunc A() {}\n")
	sha1 := commit(t, root, "add foo")
	versions1, err := CommittedFileVersions(ctx, root, sha1, []string{"foo.go"})
	if err != nil {
		t.Fatalf("CommittedFileVersions(sha1): %v", err)
	}
	r1 := Build("sess-1", "model-a", root,
		[]ChangeInput{{Seq: 1, Turn: 1, Tool: "write_file", Intent: "add A", Path: filepath.Join(root, "foo.go"), Added: 3,
			Before: nil, After: []byte("package foo\n\nfunc A() {}\n")}},
		map[int]TurnOutcomeInput{1: {Verdict: "verified", Why: "built and ran"}},
		versions1)
	if err := Attach(ctx, root, sha1, r1); err != nil {
		t.Fatalf("attach: %v", err)
	}

	write(t, root, "foo.go", "package foo\n\nfunc A() {}\n\nfunc B() {}\n")
	sha2 := commit(t, root, "add B")
	versions2, err := CommittedFileVersions(ctx, root, sha2, []string{"foo.go"})
	if err != nil {
		t.Fatalf("CommittedFileVersions(sha2): %v", err)
	}
	r2 := Build("sess-1", "model-a", root,
		[]ChangeInput{{Seq: 2, Turn: 2, Tool: "edit_file", Intent: "add B", Path: filepath.Join(root, "foo.go"), Added: 2,
			Before: []byte("package foo\n\nfunc A() {}\n"), After: []byte("package foo\n\nfunc A() {}\n\nfunc B() {}\n")}},
		map[int]TurnOutcomeInput{2: {Verdict: "unverified", Why: "nothing was run"}},
		versions2)
	if err := Attach(ctx, root, sha2, r2); err != nil {
		t.Fatalf("attach 2: %v", err)
	}

	lines, err := BlameCommitted(ctx, root, filepath.Join(root, "foo.go"))
	if err != nil {
		t.Fatalf("BlameCommitted: %v", err)
	}
	var sawA, sawB bool
	for _, l := range lines {
		if strings.Contains(l.Text, "func A()") {
			sawA = true
			if l.Commit != sha1 || l.Intent != "add A" || l.Verdict != "verified" {
				t.Errorf("line A = %+v, want commit %s intent %q verdict verified", l, sha1, "add A")
			}
		}
		if strings.Contains(l.Text, "func B()") {
			sawB = true
			if l.Commit != sha2 || l.Intent != "add B" || l.Verdict != "unverified" {
				t.Errorf("line B = %+v, want commit %s intent %q verdict unverified", l, sha2, "add B")
			}
		}
	}
	if !sawA || !sawB {
		t.Fatalf("did not see both lines: %+v", lines)
	}
}
