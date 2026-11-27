package agent

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/session"
	"github.com/xmonader/janetcode/internal/tool"
)

// forkInTurn is a provider for a parent turn that starts a background
// fork and then waits for the fork's file to appear before finishing, so
// the fork's write lands inside the parent's turn deterministically.
type forkInTurn struct {
	target string
}

func (f *forkInTurn) Name() string { return "fake" }
func (f *forkInTurn) Stream(ctx context.Context, req llm.Request, emit func(llm.Event)) (*llm.Response, error) {
	if strings.Contains(req.System, "You are a fork") {
		last := req.Messages[len(req.Messages)-1]
		if len(last.Blocks) > 0 && last.Blocks[0].Type == llm.BlockToolResult {
			return &llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "wrote it"), StopReason: llm.StopEndTurn}, nil
		}
		in, _ := json.Marshal(map[string]string{"path": f.target, "content": "# from the fork\n"})
		return toolCall("fw", "write_file", string(in)), nil
	}
	last := req.Messages[len(req.Messages)-1]
	if len(last.Blocks) > 0 && last.Blocks[0].Type == llm.BlockToolResult {
		for i := 0; i < 500; i++ { // the fork has written once its file exists
			if _, err := os.Stat(f.target); err == nil {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		return &llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "forked"), StopReason: llm.StopEndTurn}, nil
	}
	return toolCall("tk", "task", `{"description":"write docs","prompt":"write it","fork":true}`), nil
}

// A background fork that writes during a turn is part of that turn's
// checkpoint: the checkpoint lists its file, the manifest records what it
// left, a turn restore puts the tree back, and the fork's change is in
// the session's ledger.
func TestAForkWritingDuringATurnIsInThatTurnsCheckpoint(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "docs.md")
	prov := &forkInTurn{target: target}
	parent := &Agent{Provider: prov, Model: "m", System: "sys", AutoApprove: true, Log: &session.Memory{}}
	task := &Task{Parent: parent}
	parent.Tools = tool.NewRegistry(tool.WriteFile{Root: root}, task)
	if err := parent.Run(context.Background(), "document it", nil); err != nil {
		t.Fatal(err)
	}
	task.Wait(context.Background())

	list := parent.CheckpointList()
	if len(list) != 1 || list[0].Turn != 1 || len(list[0].Files) != 1 || list[0].Files[0] != target {
		t.Fatalf("the turn's checkpoint does not hold the fork's file: %+v", list)
	}
	if !list[0].Scoped || list[0].Changed != 1 {
		t.Fatalf("the manifest must record the fork's write (scoped=%v changed=%d)", list[0].Scoped, list[0].Changed)
	}
	var owned bool
	for _, c := range parent.Changes() {
		if c.Path == target {
			owned = true
		}
	}
	if !owned {
		t.Fatal("the fork's change is not in the owning session's ledger")
	}
	if _, err := parent.PreviewUndoTurn(context.Background(), 1, true); err != nil {
		t.Fatal(err)
	}
	if _, err := parent.ApplyPendingUndo(context.Background(), false); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if _, err := os.Stat(target); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the restore left the fork's file: %v", err)
	}
}

// Every mutating turn names its checkpoint as it ends; a turn that
// changes nothing leaves none and says nothing.
func TestTurnsAnnounceTheirCheckpointAndNoOpTurnsDoNot(t *testing.T) {
	f := &fake{responses: []*llm.Response{
		toolCall("t1", "write_file", `{"path":"one.txt","content":"one"}`),
		text("wrote"),
		text("only talking"),
	}}
	a, root := newAgent(t, f, nil)
	gitInit(t, root)
	a.AutoApprove = true
	a.Tools = tool.NewRegistry(tool.WriteFile{Root: root})
	var mu sync.Mutex
	var notices []string
	emit := func(e Event) {
		if e.Kind == EvNotice && strings.HasPrefix(e.Text, "checkpoint ") {
			mu.Lock()
			notices = append(notices, e.Text)
			mu.Unlock()
		}
	}
	if err := a.Run(context.Background(), "write", emit); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	got := append([]string(nil), notices...)
	mu.Unlock()
	if len(got) != 1 || !strings.HasPrefix(got[0], "checkpoint t1") || !strings.Contains(got[0], "1 path changed") || !strings.Contains(got[0], "/checkpoints") {
		t.Fatalf("notices after a mutating turn = %q", got)
	}
	if list := a.CheckpointList(); len(list) != 1 || !strings.HasPrefix(got[0], "checkpoint "+list[0].ID) {
		t.Fatalf("the announced id must be the listed one: %q vs %+v", got, list)
	}
	if err := a.Run(context.Background(), "just talk", emit); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	n := len(notices)
	mu.Unlock()
	if n != 1 || len(a.CheckpointList()) != 1 {
		t.Fatalf("a turn that changed nothing announced or kept a checkpoint: notices=%d list=%d", n, len(a.CheckpointList()))
	}
}

// cancelAfterWrite lets one write_file through, then cancels the turn
// when the model is called again.
type cancelAfterWrite struct {
	cancel context.CancelFunc
	calls  int
}

func (c *cancelAfterWrite) Name() string { return "fake" }
func (c *cancelAfterWrite) Stream(ctx context.Context, req llm.Request, emit func(llm.Event)) (*llm.Response, error) {
	c.calls++
	if c.calls == 1 {
		return toolCall("w", "write_file", `{"path":"half.txt","content":"half done"}`), nil
	}
	c.cancel()
	return nil, ctx.Err()
}

// A turn cancelled after it changed something still has its checkpoint,
// announced and restorable: the work is on disk, so the way back must be.
func TestACancelledTurnStillLeavesARestorableCheckpoint(t *testing.T) {
	root := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a := &Agent{Provider: &cancelAfterWrite{cancel: cancel}, Model: "m", System: "sys", AutoApprove: true, Log: &session.Memory{}}
	a.Tools = tool.NewRegistry(tool.WriteFile{Root: root})
	var notices []string
	var mu sync.Mutex
	err := a.Run(ctx, "start", func(e Event) {
		if e.Kind == EvNotice && strings.HasPrefix(e.Text, "checkpoint ") {
			mu.Lock()
			notices = append(notices, e.Text)
			mu.Unlock()
		}
	})
	if err == nil {
		t.Fatal("the cancelled turn reported success")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(notices) != 1 || !strings.HasPrefix(notices[0], "checkpoint t1") {
		t.Fatalf("a cancelled turn must still name its checkpoint: %q", notices)
	}
	if _, err := os.Stat(filepath.Join(root, "half.txt")); err != nil {
		t.Fatal("the write did not happen")
	}
	if list := a.CheckpointList(); len(list) != 1 || !list[0].Scoped {
		t.Fatalf("checkpoint after cancellation = %+v", list)
	}
	if _, err := a.Undo(context.Background()); err != nil {
		t.Fatalf("undo after cancellation: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "half.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("undo did not remove the cancelled turn's file")
	}
}

// After a resume, the restore refuses a conflict the same way: the
// manifest travels with the checkpoint in the sidecar.
func TestResumedCheckpointsStillRefuseConflicts(t *testing.T) {
	f := &fake{responses: []*llm.Response{
		toolCall("t1", "write_file", `{"path":"x.txt","content":"from the turn"}`),
		text("done"),
	}}
	a, root := newAgent(t, f, nil)
	a.AutoApprove = true
	a.Tools = tool.NewRegistry(tool.WriteFile{Root: root})
	a.UndoPath = filepath.Join(t.TempDir(), "s.undo.json")
	if err := a.Run(context.Background(), "write", nil); err != nil {
		t.Fatal(err)
	}
	b := &Agent{}
	b.UndoPath = a.UndoPath
	if err := b.LoadUndo(); err != nil {
		t.Fatal(err)
	}
	if got := b.CheckpointList(); len(got) != 1 || got[0].ID != a.CheckpointList()[0].ID || !got[0].Scoped {
		t.Fatalf("the resumed list differs: %+v vs %+v", got, a.CheckpointList())
	}
	if err := os.WriteFile(filepath.Join(root, "x.txt"), []byte("edited after the resume"), 0o644); err != nil {
		t.Fatal(err)
	}
	tr, err := b.PreviewUndoTurn(context.Background(), 1, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(tr.Conflicts) != 1 {
		t.Fatalf("conflicts after resume = %+v", tr.Conflicts)
	}
	if _, err := b.ApplyPendingUndo(context.Background(), false); !errors.Is(err, ErrTurnRestoreConflict) {
		t.Fatalf("apply after resume = %v", err)
	}
}
