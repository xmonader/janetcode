package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/snapshot"
	"github.com/xmonader/janetcode/internal/tool"
)

// TestUndoRevertsShellChangesOutsideGit: in a directory that is not a
// git repository, a turn's shell changes used to be beyond /undo — only
// the file tools' copies existed. A shadow store snapshots the tree, so
// the undo puts back what the shell created, edited and deleted, and the
// turn's checkpoint says it covers the whole tree.
func TestUndoRevertsShellChangesOutsideGit(t *testing.T) {
	f := &fake{responses: []*llm.Response{
		toolCall("t1", "bash", `{"command":"echo made > made.txt; echo changed > a.txt; rm b.txt"}`),
		text("done"),
	}}
	a, root := newAgent(t, f, nil)
	if err := os.WriteFile(filepath.Join(root, "b.txt"), []byte("b\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	repo, err := snapshot.OpenShadow(context.Background(), root, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	a.Snapshots = repo
	a.AutoApprove = true
	a.Tools = tool.NewRegistry(tool.Bash{Root: root})
	if err := a.Run(context.Background(), "change", nil); err != nil {
		t.Fatal(err)
	}
	if list := a.CheckpointList(); len(list) != 1 || !list[0].Tree {
		t.Fatalf("checkpoint = %+v", list)
	}
	done, err := a.Undo(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(filepath.Join(root, "a.txt")); string(data) != "alpha\n" {
		t.Fatalf("a.txt after undo = %q (%v)", data, done)
	}
	if data, _ := os.ReadFile(filepath.Join(root, "b.txt")); string(data) != "b\n" {
		t.Fatalf("b.txt after undo = %q", data)
	}
	if _, err := os.Stat(filepath.Join(root, "made.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("made.txt survived the undo")
	}
	if strings.Contains(strings.Join(done, "\n"), "left") {
		t.Fatalf("undo left something alone: %v", done)
	}
	// A shadow store is not the user's repository.
	if a.userRepo() != nil {
		t.Fatal("proof notes would look for commits in the shadow store")
	}
}
