package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A file whose state before the turn could not be read is marked Failed
// and left alone by an undo. That mark has to survive the sidecar: a
// Failed snapshot has Existed false and no Data, so an undo that has
// forgotten why would take the "it did not exist before" branch and
// delete the user's file — an undo that destroys the thing it was asked
// to protect, and reports success.
func TestFailedCaptureSurvivesTheUndoSidecar(t *testing.T) {
	dir := t.TempDir()
	victim := filepath.Join(dir, "unreadable.txt")
	os.WriteFile(victim, []byte("the real content"), 0o644)

	a := &Agent{}
	a.UndoPath = filepath.Join(dir, "s.undo.json")
	a.cp.all = []Checkpoint{{
		When:  time.Now(),
		Turn:  1,
		Files: []Snapshot{{Path: victim, Failed: true}},
	}}
	a.cp.dirty = true
	if err := a.SaveUndo(); err != nil {
		t.Fatal(err)
	}

	b := &Agent{}
	b.UndoPath = a.UndoPath
	if err := b.LoadUndo(); err != nil {
		t.Fatal(err)
	}
	if len(b.cp.all) != 1 || !b.cp.all[0].Files[0].Failed {
		t.Fatalf("the failed mark was lost: %+v", b.cp.all)
	}
	done, err := b.Undo(context.Background())
	if err == nil || !strings.Contains(err.Error(), "could not be captured") {
		t.Fatalf("done=%v err=%v, want the failure reported", done, err)
	}
	if got, rerr := os.ReadFile(victim); rerr != nil || string(got) != "the real content" {
		t.Fatalf("the undo touched a file it could not capture: %q %v", got, rerr)
	}
}

// The sidecar is optional and may not be there yet. Neither case is an
// error, and neither may be turned into one by a stricter read: a session
// that cannot load its undo file refuses to start.
func TestUndoSidecarAbsentOrUnset(t *testing.T) {
	missing := &Agent{}
	missing.UndoPath = filepath.Join(t.TempDir(), "never-written.json")
	if err := missing.LoadUndo(); err != nil {
		t.Fatalf("a sidecar that does not exist yet is not an error: %v", err)
	}
	if len(missing.Checkpoints()) != 0 {
		t.Fatal("checkpoints appeared from nowhere")
	}
	none := &Agent{}
	if err := none.LoadUndo(); err != nil {
		t.Fatalf("LoadUndo with no path: %v", err)
	}
	if err := none.SaveUndo(); err != nil {
		t.Fatalf("SaveUndo with no path: %v", err)
	}
}

// Sidecars written before the file grew its wrapper object are a bare
// array of checkpoints. They still have to load, or the first resume
// after an upgrade loses every undo the user had.
func TestUndoSidecarReadsTheLegacyBareArray(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.undo.json")
	legacy, _ := json.Marshal([]Checkpoint{{When: time.Now(), Turn: 4, Files: []Snapshot{{Path: "a.go", Existed: true, Data: []byte("x")}}}})
	os.WriteFile(path, legacy, 0o600)

	a := &Agent{}
	a.UndoPath = path
	if err := a.LoadUndo(); err != nil {
		t.Fatal(err)
	}
	cps := a.Checkpoints()
	if len(cps) != 1 || cps[0].Turn != 4 || len(cps[0].Files) != 1 {
		t.Fatalf("legacy sidecar not read: %+v", cps)
	}
}
