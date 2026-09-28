package gitx

import (
	"context"
	"os"
	"path/filepath"
	"testing"
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
