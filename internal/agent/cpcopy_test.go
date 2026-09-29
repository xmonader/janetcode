package agent

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/tool"
)

// TestLargeCheckpointCopyIsABlob: a file over inlineCopyBytes that a turn
// overwrites is copied to a blob beside the undo sidecar, not into the
// sidecar's JSON — which is where a 2 GB dump used to land, base64 — and
// an undo still puts every byte back.
func TestLargeCheckpointCopyIsABlob(t *testing.T) {
	f := &fake{responses: []*llm.Response{
		toolCall("t1", "write_file", `{"path":"big.bin","content":"small now\n"}`),
		text("done"),
	}}
	a, root := newAgent(t, f, nil)
	a.AutoApprove = true
	a.UndoPath = filepath.Join(t.TempDir(), "s.undo.json")
	big := bytes.Repeat([]byte("0123456789abcdef"), (2<<20)/16) // 2 MiB
	path := filepath.Join(root, "big.bin")
	if err := os.WriteFile(path, big, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := a.Run(context.Background(), "shrink it", nil); err != nil {
		t.Fatal(err)
	}
	cps := a.Checkpoints()
	if len(cps) != 1 || len(cps[0].Files) != 1 {
		t.Fatalf("checkpoints = %+v", cps)
	}
	cf := cps[0].Files[0]
	if cf.Blob == "" || len(cf.Data) != 0 {
		t.Fatalf("a 2 MiB copy should be a blob, not inline: blob=%q data=%d bytes", cf.Blob, len(cf.Data))
	}
	if filepath.Dir(cf.Blob) != a.UndoPath+blobSuffix {
		t.Fatalf("blob %s is not beside the undo sidecar", cf.Blob)
	}
	// A resumed session finds it through the sidecar.
	b := &Agent{}
	b.UndoPath = a.UndoPath
	if err := b.LoadUndo(); err != nil || b.Checkpoints()[0].Files[0].Blob != cf.Blob {
		t.Fatalf("the blob did not survive the sidecar: %v", err)
	}
	if _, err := a.Undo(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(path); !bytes.Equal(got, big) {
		t.Fatalf("undo restored %d bytes, want the original %d", len(got), len(big))
	}
}

// TestTooLargeFileIsNotCopied: past maxCopyBytes the checkpoint does not
// copy the file at all. Outside git the turn is told at once, and an undo
// leaves the file as it is and says why instead of writing an empty
// "before" over it.
func TestTooLargeFileIsNotCopied(t *testing.T) {
	old := maxCopyBytes
	maxCopyBytes = 1024
	defer func() { maxCopyBytes = old }()
	f := &fake{responses: []*llm.Response{
		toolCall("t1", "write_file", `{"path":"huge.log","content":"trimmed\n"}`),
		text("done"),
	}}
	a, root := newAgent(t, f, nil)
	a.AutoApprove = true
	path := filepath.Join(root, "huge.log")
	if err := os.WriteFile(path, bytes.Repeat([]byte("x"), 4096), 0o644); err != nil {
		t.Fatal(err)
	}
	evs, emit := collect()
	if err := a.Run(context.Background(), "trim", emit); err != nil {
		t.Fatal(err)
	}
	var said bool
	for _, e := range *evs {
		if e.Kind == EvNotice && strings.Contains(e.Text, "over the") && strings.Contains(e.Text, "/undo will not") {
			said = true
		}
	}
	if !said {
		t.Fatal("the turn was not told the file was too large to checkpoint")
	}
	cps := a.Checkpoints()
	if len(cps) != 1 || !cps[0].Files[0].TooLarge {
		t.Fatalf("checkpoint = %+v", cps)
	}
	preview, _ := a.UndoPreview(context.Background())
	if len(preview) != 1 || !strings.Contains(preview[0], "too large") {
		t.Fatalf("preview = %v", preview)
	}
	_, err := a.Undo(context.Background())
	if err == nil || !strings.Contains(err.Error(), "was not kept") {
		t.Fatalf("undo error = %v", err)
	}
	if got, _ := os.ReadFile(path); string(got) != "trimmed\n" {
		t.Fatalf("undo touched a file it had no copy of: %q", got)
	}
}

// TestUnreferencedBlobsAreCollected: a blob no checkpoint names any more
// is removed when the sidecar is saved; a named one, and anything in the
// directory that is not a blob, stays.
func TestUnreferencedBlobsAreCollected(t *testing.T) {
	dir := t.TempDir()
	keep := filepath.Join(dir, strings.Repeat("a", 64))
	gone := filepath.Join(dir, strings.Repeat("b", 64))
	other := filepath.Join(dir, "notes.txt")
	for _, p := range []string{keep, gone, other} {
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	collectBlobs(dir, []Checkpoint{{Files: []Snapshot{{Blob: keep}}}})
	if _, err := os.Stat(keep); err != nil {
		t.Fatal("a referenced blob was removed")
	}
	if _, err := os.Stat(gone); !os.IsNotExist(err) {
		t.Fatal("an unreferenced blob was kept")
	}
	if _, err := os.Stat(other); err != nil {
		t.Fatal("a file that is not a blob was removed")
	}
}

// TestHugeChangeIsLeftOutOfProvenance: the provenance record never evicts
// its last change, so one change over its whole byte bound used to hold
// it over the bound by itself, twice over (before and after).
func TestHugeChangeIsLeftOutOfProvenance(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "dump.sql")
	if err := os.WriteFile(path, bytes.Repeat([]byte("y"), maxChangesBytes+1), 0o644); err != nil {
		t.Fatal(err)
	}
	a := &Agent{}
	w := tool.WriteFile{Root: root}
	pend := a.before(w, []byte(`{"path":"dump.sql","content":"z"}`))
	if len(pend) != 0 {
		t.Fatalf("a %d-byte file was read into the provenance record", maxChangesBytes+1)
	}
}
