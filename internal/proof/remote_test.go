package proof

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/gitx/gitxtest"
)

// gitIn runs a git command in dir, failing the test on error.
func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// TestPushFetchRoundTripThroughBareRemote checks that a note attached in
// one clone is invisible in another until Push and Fetch move it across a
// bare remote -- the whole point of notes being local only until pushed.
func TestPushFetchRoundTripThroughBareRemote(t *testing.T) {
	ctx := context.Background()

	bare := t.TempDir()
	gitxtest.InitBareRepo(t, bare)

	origin := t.TempDir()
	gitIn(t, origin, "clone", "-q", bare, ".")
	// A clone starts with no identity of its own; Attach below shells out
	// to `git notes add` directly (not through the commit helper's env),
	// so the clone needs a repo-local identity to commit against.
	gitxtest.Configure(t, origin)
	write(t, origin, "foo.go", "package foo\n")
	sha := commit(t, origin, "add foo")
	gitIn(t, origin, "push", "-q", "origin", "HEAD")

	versions, err := CommittedFileVersions(ctx, origin, sha, []string{"foo.go"})
	if err != nil {
		t.Fatalf("CommittedFileVersions: %v", err)
	}
	rec := Build("sess-1", "model-a", origin,
		[]ChangeInput{{Seq: 1, Turn: 1, Tool: "write_file", Intent: "add foo", Path: "foo.go", Added: 1,
			Before: nil, After: []byte("package foo\n")}},
		map[int]TurnOutcomeInput{1: {Verdict: "verified", Why: "built"}},
		versions)
	if err := Attach(ctx, origin, sha, rec); err != nil {
		t.Fatalf("Attach: %v", err)
	}

	other := t.TempDir()
	gitIn(t, other, "clone", "-q", bare, ".")
	if _, ok, err := Read(ctx, other, sha); err != nil || ok {
		t.Fatalf("expected no note before push: ok=%v err=%v", ok, err)
	}

	if _, err := Push(ctx, origin, "origin"); err != nil {
		t.Fatalf("Push: %v", err)
	}
	// A plain fetch does not bring notes on its own.
	gitIn(t, other, "fetch", "-q", "origin")
	if _, ok, err := Read(ctx, other, sha); err != nil || ok {
		t.Fatalf("expected notes to still be absent after a plain fetch: ok=%v err=%v", ok, err)
	}

	if err := Fetch(ctx, other, "origin"); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	got, ok, err := Read(ctx, other, sha)
	if err != nil || !ok {
		t.Fatalf("Read after Fetch: ok=%v err=%v", ok, err)
	}
	if got.Session != "sess-1" {
		t.Fatalf("session = %q, want sess-1", got.Session)
	}
}

// TestPushWarnsWhenNotedCommitIsNotOnTheRemote is defect #4's regression
// test: a commit that carries a proof note but whose own branch was never
// pushed still gets its note pushed (notes are harmless on their own),
// but Push must say so, so the front-ends can tell the user their branch
// needs pushing too.
func TestPushWarnsWhenNotedCommitIsNotOnTheRemote(t *testing.T) {
	ctx := context.Background()

	bare := t.TempDir()
	gitxtest.InitBareRepo(t, bare)

	origin := t.TempDir()
	gitIn(t, origin, "clone", "-q", bare, ".")
	gitxtest.Configure(t, origin)
	write(t, origin, "seed.go", "package seed\n")
	commit(t, origin, "seed")
	gitIn(t, origin, "push", "-q", "origin", "HEAD")

	// A second commit, noted but never pushed.
	write(t, origin, "foo.go", "package foo\n")
	sha := commit(t, origin, "add foo (not pushed)")

	versions, err := CommittedFileVersions(ctx, origin, sha, []string{"foo.go"})
	if err != nil {
		t.Fatalf("CommittedFileVersions: %v", err)
	}
	rec := Build("sess-1", "model-a", origin,
		[]ChangeInput{{Seq: 1, Turn: 1, Tool: "write_file", Intent: "add foo", Path: "foo.go", Added: 1,
			Before: nil, After: []byte("package foo\n")}},
		map[int]TurnOutcomeInput{1: {Verdict: "verified", Why: "built"}},
		versions)
	if err := Attach(ctx, origin, sha, rec); err != nil {
		t.Fatalf("Attach: %v", err)
	}

	warning, err := Push(ctx, origin, "origin")
	if err != nil {
		t.Fatalf("Push: %v", err)
	}
	if !strings.Contains(warning, "1 noted commit(s) are not on origin yet") || !strings.Contains(warning, "push your branch too") {
		t.Fatalf("warning = %q, want it to name the unpushed noted commit", warning)
	}
	// The push happened anyway: the note reached the bare remote's own
	// notes ref despite the warning.
	notesOnBare := gitIn(t, bare, "notes", "--ref="+NotesRef, "list")
	if !strings.Contains(notesOnBare, sha) {
		t.Fatalf("note for %s did not reach the bare remote: %q", sha, notesOnBare)
	}

	// Push the branch too, and the warning goes away.
	gitIn(t, origin, "push", "-q", "origin", "HEAD")
	warning2, err := Push(ctx, origin, "origin")
	if err != nil {
		t.Fatalf("Push (second time): %v", err)
	}
	if warning2 != "" {
		t.Fatalf("warning = %q after pushing the branch, want none", warning2)
	}
}

func TestConfigureFetchAddsRefspecOnceAndIsIdempotent(t *testing.T) {
	ctx := context.Background()
	root := initRepo(t)
	gitIn(t, root, "remote", "add", "origin", "https://example.invalid/repo.git")

	if err := ConfigureFetch(ctx, root, "origin"); err != nil {
		t.Fatalf("ConfigureFetch: %v", err)
	}
	out := gitIn(t, root, "config", "--get-all", "remote.origin.fetch")
	want := "+" + NotesRef + ":" + NotesRef
	if !strings.Contains(out, want) {
		t.Fatalf("fetch config = %q, want it to contain %q", out, want)
	}

	// Calling again must not duplicate the entry.
	if err := ConfigureFetch(ctx, root, "origin"); err != nil {
		t.Fatalf("ConfigureFetch (second call): %v", err)
	}
	out2 := gitIn(t, root, "config", "--get-all", "remote.origin.fetch")
	if n := strings.Count(out2, want); n != 1 {
		t.Fatalf("fetch config has %d copies of the refspec, want 1: %q", n, out2)
	}
}

func TestConfigureFetchDefaultsRemoteToOrigin(t *testing.T) {
	ctx := context.Background()
	root := initRepo(t)
	gitIn(t, root, "remote", "add", "origin", "https://example.invalid/repo.git")
	if err := ConfigureFetch(ctx, root, ""); err != nil {
		t.Fatalf("ConfigureFetch: %v", err)
	}
	out := gitIn(t, root, "config", "--get-all", "remote.origin.fetch")
	if !strings.Contains(out, NotesRef) {
		t.Fatalf("fetch config = %q, want the notes refspec", out)
	}
}

func TestParseArgs(t *testing.T) {
	cases := []struct {
		in   string
		want Args
	}{
		{"", Args{Action: ActionShow}},
		{"  ", Args{Action: ActionShow}},
		{"abc123", Args{Action: ActionShow, Commit: "abc123"}},
		{"push", Args{Action: ActionPush, Remote: "origin"}},
		{"push upstream", Args{Action: ActionPush, Remote: "upstream"}},
		{"fetch", Args{Action: ActionFetch, Remote: "origin"}},
		{"fetch upstream", Args{Action: ActionFetch, Remote: "upstream"}},
	}
	for _, c := range cases {
		if got := ParseArgs(c.in); got != c.want {
			t.Errorf("ParseArgs(%q) = %+v, want %+v", c.in, got, c.want)
		}
	}
}
