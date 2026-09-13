package proof

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func initRepo(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q")
	run("config", "user.name", "t")
	run("config", "user.email", "t@t")
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
		{Seq: 1, Turn: 1, Tool: "write_file", Intent: "add foo", Path: filepath.Join(root, "foo.go"), Added: 3},
		{Seq: 2, Turn: 1, Tool: "edit_file", Intent: "tweak foo", Path: filepath.Join(root, "foo.go"), Added: 1, Removed: 1},
		{Seq: 3, Turn: 2, Tool: "write_file", Intent: "scratch file, not committed", Path: filepath.Join(root, "scratch.go"), Added: 2},
	}
	outcomes := map[int]TurnOutcomeInput{
		1: {Verdict: "verified", Why: "tests ran and passed", Tested: true},
		2: {Verdict: "unverified", Why: "nothing was run"},
	}
	r := Build("sess-1", "test-model", root, changes, outcomes, []string{"foo.go"})
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
	changes := []ChangeInput{{Seq: 1, Turn: 1, Tool: "write_file", Path: filepath.Join(root, "other.go")}}
	r := Build("sess-1", "m", root, changes, nil, []string{"foo.go"})
	if !r.Empty() {
		t.Fatalf("expected empty record, got %+v", r)
	}
}

func TestAttachReadRoundTrip(t *testing.T) {
	ctx := context.Background()
	root := initRepo(t)
	write(t, root, "foo.go", "package foo\n")
	sha := commit(t, root, "add foo")

	r := Build("sess-1", "model-a", root,
		[]ChangeInput{{Seq: 1, Turn: 1, Tool: "write_file", Intent: "add foo", Path: filepath.Join(root, "foo.go"), Added: 1}},
		map[int]TurnOutcomeInput{1: {Verdict: "verified", Why: "built"}},
		[]string{"foo.go"})
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

	r1 := Build("sess-1", "model-a", root,
		[]ChangeInput{{Seq: 1, Turn: 1, Tool: "write_file", Intent: "first pass", Path: filepath.Join(root, "foo.go"), Added: 1}},
		map[int]TurnOutcomeInput{1: {Verdict: "verified", Why: "built"}},
		[]string{"foo.go"})
	if err := Attach(ctx, root, sha, r1); err != nil {
		t.Fatalf("attach 1: %v", err)
	}
	r2 := Build("sess-2", "model-b", root,
		[]ChangeInput{{Seq: 1, Turn: 1, Tool: "edit_file", Intent: "second pass", Path: filepath.Join(root, "foo.go"), Added: 2}},
		map[int]TurnOutcomeInput{1: {Verdict: "verified", Why: "tests passed"}},
		[]string{"foo.go"})
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
	r1 := Build("sess-1", "model-a", root,
		[]ChangeInput{{Seq: 1, Turn: 1, Tool: "write_file", Intent: "add A", Path: filepath.Join(root, "foo.go"), Added: 3}},
		map[int]TurnOutcomeInput{1: {Verdict: "verified", Why: "built and ran"}},
		[]string{"foo.go"})
	if err := Attach(ctx, root, sha1, r1); err != nil {
		t.Fatalf("attach: %v", err)
	}

	write(t, root, "foo.go", "package foo\n\nfunc A() {}\n\nfunc B() {}\n")
	sha2 := commit(t, root, "add B")
	r2 := Build("sess-1", "model-a", root,
		[]ChangeInput{{Seq: 2, Turn: 2, Tool: "edit_file", Intent: "add B", Path: filepath.Join(root, "foo.go"), Added: 2}},
		map[int]TurnOutcomeInput{2: {Verdict: "unverified", Why: "nothing was run"}},
		[]string{"foo.go"})
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
