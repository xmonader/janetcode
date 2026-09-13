package proof

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
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
	gitIn(t, bare, "init", "-q", "--bare")

	origin := t.TempDir()
	gitIn(t, origin, "clone", "-q", bare, ".")
	write(t, origin, "foo.go", "package foo\n")
	sha := commit(t, origin, "add foo")
	gitIn(t, origin, "push", "-q", "origin", "HEAD")

	rec := Build("sess-1", "model-a", origin,
		[]ChangeInput{{Seq: 1, Turn: 1, Tool: "write_file", Intent: "add foo", Path: "foo.go", Added: 1}},
		map[int]TurnOutcomeInput{1: {Verdict: "verified", Why: "built"}},
		[]string{"foo.go"})
	if err := Attach(ctx, origin, sha, rec); err != nil {
		t.Fatalf("Attach: %v", err)
	}

	other := t.TempDir()
	gitIn(t, other, "clone", "-q", bare, ".")
	if _, ok, err := Read(ctx, other, sha); err != nil || ok {
		t.Fatalf("expected no note before push: ok=%v err=%v", ok, err)
	}

	if err := Push(ctx, origin, "origin"); err != nil {
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
