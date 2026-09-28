package gitx

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// A second edit to a file that is already modified leaves its status
// line as it was, so only the size and modification time tell the two
// states apart. git prints status paths relative to the repository root,
// so a root below the top level must still find the file to stat it:
// otherwise the fingerprint of a subdirectory session never changes
// between such edits and the shell cache answers from the old tree.
func TestTreeStateSeesRepeatedEditsFromASubdirectory(t *testing.T) {
	dir := repo(t)
	sub := filepath.Join(dir, "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	f := filepath.Join(sub, "f.txt")
	if err := os.WriteFile(f, []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for _, args := range [][]string{{"add", "-A"}, {"commit", "-q", "-m", "sub"}} {
		if _, err := Run(ctx, dir, nil, args...); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(f, []byte("two\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	before := TreeState(sub)
	if before == "" {
		t.Fatal("no fingerprint inside a repository")
	}
	if err := os.WriteFile(f, []byte("three, and longer\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if after := TreeState(sub); after == before {
		t.Fatalf("fingerprint unchanged by an edit to an already-modified file:\n%s", after)
	}
}

// The fingerprint runs before every shell command, so it must not take
// index.lock to write a refreshed index back: a commit the user makes in
// another terminal at that moment would fail on the lock.
func TestTreeStateLeavesTheIndexAlone(t *testing.T) {
	dir := repo(t)
	index := filepath.Join(dir, ".git", "index")
	// A tracked file with a new modification time and the same content
	// is what makes git status want to refresh and rewrite the index.
	later := time.Now().Add(time.Hour)
	if err := os.Chtimes(filepath.Join(dir, "a.txt"), later, later); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(index)
	if err != nil {
		t.Fatal(err)
	}
	if TreeState(dir) == "" {
		t.Fatal("no fingerprint inside a repository")
	}
	after, err := os.Stat(index)
	if err != nil {
		t.Fatal(err)
	}
	if !after.ModTime().Equal(before.ModTime()) {
		t.Fatalf("the index was rewritten: %v -> %v", before.ModTime(), after.ModTime())
	}
}
