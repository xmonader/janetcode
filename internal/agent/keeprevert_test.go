package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/session"
	"github.com/xmonader/janetcode/internal/snapshot"
	"github.com/xmonader/janetcode/internal/tool"
)

func TestRegressOnlyWhenACheckGoesRed(t *testing.T) {
	ok := TurnOutcome{Verdict: VerdictVerified, Edited: 1, Why: "tests passed"}
	bad := TurnOutcome{Verdict: VerdictCheckFailed, Edited: 1, Why: "tests failed"}
	none := TurnOutcome{Verdict: VerdictNoChanges, Why: "nothing edited"}
	if !regress(ok, bad) {
		t.Fatal("verified → check failed is a regression")
	}
	if regress(ok, none) {
		t.Fatal("a turn that changed nothing is not a regression")
	}
	if regress(bad, bad) {
		t.Fatal("staying failed is not a new regression")
	}
	if regress(none, ok) {
		t.Fatal("getting better is not a regression")
	}
}

func TestKeepOrRevertOffDoesNothing(t *testing.T) {
	a, _ := newAgent(t, &fake{}, nil)
	a.Off = map[string]bool{"keeprevert": true}
	a.Log = &session.Memory{}
	a.CreateGoal("ship it", 3)
	a.goalOutSet = true
	a.lastGoalOut = TurnOutcome{Verdict: VerdictVerified, Edited: 1, Why: "ok"}
	if a.keepOrRevert(context.Background(), TurnOutcome{Verdict: VerdictCheckFailed, Edited: 1, Why: "no"}, func(Event) {}) {
		t.Fatal("keeprevert off must not continue the turn")
	}
}

func TestKeepOrRevertRestoresARegressedGoalRound(t *testing.T) {
	f := &fake{responses: []*llm.Response{
		toolCall("t1", "write_file", `{"path":"x.txt","content":"good"}`),
		text("ok"),
		toolCall("t2", "write_file", `{"path":"x.txt","content":"broken"}`),
		text("still going"),
		text("recovered"),
	}}
	a, root := newAgent(t, f, nil)
	a.AutoApprove = true
	a.Tools = tool.NewRegistry(tool.WriteFile{Root: root})
	a.Log = &session.Memory{}
	if err := a.Run(context.Background(), "write good", nil); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(filepath.Join(root, "x.txt")); string(data) != "good" {
		t.Fatalf("setup: %q", data)
	}
	a.CreateGoal("keep x.txt good", 4)
	a.goalOutSet = true
	a.lastGoalOut = TurnOutcome{Verdict: VerdictVerified, Edited: 1, Why: "the file is good"}
	// The breaking turn has no verify runner, so the loop's own
	// keepOrRevert sees Unverified and would overwrite lastGoalOut.
	// Switch it off for the write, then drive the comparison ourselves.
	a.Off = map[string]bool{"keeprevert": true}
	evs, emit := collect()
	if err := a.Run(context.Background(), "break it", emit); err != nil {
		t.Fatal(err)
	}
	a.Off = nil
	if data, _ := os.ReadFile(filepath.Join(root, "x.txt")); string(data) != "broken" {
		t.Fatalf("after the breaking turn: %q", data)
	}
	a.turnN = a.Stats().Turns
	a.goalOutSet = true
	a.lastGoalOut = TurnOutcome{Verdict: VerdictVerified, Edited: 1, Why: "the file is good"}
	if !a.keepOrRevert(context.Background(), TurnOutcome{Verdict: VerdictCheckFailed, Edited: 1, Why: "x.txt is broken"}, emit) {
		t.Fatal("a failed goal round should revert and continue")
	}
	if data, _ := os.ReadFile(filepath.Join(root, "x.txt")); string(data) != "good" {
		t.Fatalf("file was not restored: %q", data)
	}
	var saw bool
	for _, e := range *evs {
		if e.Kind == EvNotice && strings.Contains(e.Text, "keep-or-revert") {
			saw = true
		}
	}
	if !saw {
		t.Fatalf("no revert notice: %+v", evs)
	}
}

// keep-or-revert runs mid-turn, before endTurn records which files the
// turn changed. It restored the whole tree to the turn's snapshot, so a
// file the user saved in their editor while the turn ran went back too.
// Only what this turn's own tools changed may be reverted, and a file the
// turn edited that someone changed again since is left alone as well.
func TestKeepOrRevertMidTurnRevertsOnlyTheTurnsOwnEdits(t *testing.T) {
	a, root := newAgent(t, &fake{}, nil)
	x, y, z := filepath.Join(root, "x.txt"), filepath.Join(root, "y.txt"), filepath.Join(root, "z.txt")
	for _, p := range []string{x, y, z} {
		if err := os.WriteFile(p, []byte("before\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	gitInit(t, root)
	repo, err := snapshot.Open(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	a.Snapshots = repo
	a.AutoApprove = true
	a.Tools = tool.NewRegistry(tool.WriteFile{Root: root})
	a.Log = &session.Memory{}
	a.CreateGoal("keep things green", 4)
	a.goalOutSet = true
	a.lastGoalOut = TurnOutcome{Verdict: VerdictVerified, Edited: 1, Why: "green"}

	// The turn, as far as it has got: two edits by a file tool, with the
	// checkpoint still open and no manifest yet.
	ctx := context.Background()
	a.turnN = 1
	a.cp.beginTurn(1, len(a.Messages))
	for i, p := range []string{x, z} {
		u := llm.Block{Type: llm.BlockToolUse, ID: fmt.Sprint("w", i), Name: "write_file", Input: json.RawMessage(fmt.Sprintf(`{"path":%q,"content":"turn\n"}`, p))}
		if _, _, isErr, err := a.executeTool(ctx, u, func(Event) {}); err != nil || isErr {
			t.Fatalf("write %s: %v %v", p, isErr, err)
		}
	}
	// Meanwhile the user saves y.txt, and edits z.txt after the turn did.
	if err := os.WriteFile(y, []byte("user\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(z, []byte("turn\nuser\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var notice string
	if !a.keepOrRevert(ctx, TurnOutcome{Verdict: VerdictCheckFailed, Edited: 2, Why: "red"}, func(e Event) {
		if e.Kind == EvNotice {
			notice = e.Text
		}
	}) {
		t.Fatal("a regressed goal round should revert and continue")
	}
	for p, want := range map[string]string{x: "before\n", y: "user\n", z: "turn\nuser\n"} {
		if data, _ := os.ReadFile(p); string(data) != want {
			t.Errorf("%s = %q, want %q", filepath.Base(p), data, want)
		}
	}
	if !strings.Contains(notice, "y.txt") || !strings.Contains(notice, "z.txt") {
		t.Errorf("the notice should name what was left alone: %q", notice)
	}
}

// A turn that changed files only through the shell has nothing the
// harness can tell apart from the user's own edits, so keep-or-revert
// leaves the tree and the checkpoint alone; /undo still has it.
func TestKeepOrRevertLeavesAShellOnlyTurnAlone(t *testing.T) {
	a, root := newAgent(t, &fake{}, nil)
	gitInit(t, root)
	repo, err := snapshot.Open(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	a.Snapshots = repo
	a.AutoApprove = true
	a.Tools = tool.NewRegistry(tool.Bash{Root: root})
	a.Log = &session.Memory{}
	a.CreateGoal("keep things green", 4)
	a.goalOutSet = true
	a.lastGoalOut = TurnOutcome{Verdict: VerdictVerified, Edited: 1, Why: "green"}
	ctx := context.Background()
	a.turnN = 1
	a.cp.beginTurn(1, len(a.Messages))
	u := llm.Block{Type: llm.BlockToolUse, ID: "b", Name: "bash", Input: json.RawMessage(`{"command":"echo shell > a.txt"}`)}
	if _, _, isErr, err := a.executeTool(ctx, u, func(Event) {}); err != nil || isErr {
		t.Fatalf("bash: %v %v", isErr, err)
	}
	if a.keepOrRevert(ctx, TurnOutcome{Verdict: VerdictCheckFailed, Edited: 1, Why: "red"}, func(Event) {}) {
		t.Fatal("nothing of the turn's own could be reverted, so the turn should stand")
	}
	if data, _ := os.ReadFile(filepath.Join(root, "a.txt")); string(data) != "shell\n" {
		t.Fatalf("a.txt = %q", data)
	}
	if len(a.Checkpoints()) != 1 {
		t.Fatalf("the turn's checkpoint should stay for /undo: %+v", a.Checkpoints())
	}
}

func TestKeepOrRevertSkipsASteeredTurn(t *testing.T) {
	a, _ := newAgent(t, &fake{}, nil)
	a.Log = &session.Memory{}
	a.CreateGoal("ship it", 3)
	a.goalOutSet = true
	a.lastGoalOut = TurnOutcome{Verdict: VerdictVerified, Edited: 1, Why: "ok"}
	a.steeredThisTurn = true
	if a.keepOrRevert(context.Background(), TurnOutcome{Verdict: VerdictCheckFailed, Edited: 1, Why: "no"}, func(Event) {}) {
		t.Fatal("a steered turn must not be reverted")
	}
}

func TestKeepOrRevertCapsConsecutiveReverts(t *testing.T) {
	a, _ := newAgent(t, &fake{}, nil)
	a.Log = &session.Memory{}
	a.CreateGoal("ship it", 3)
	a.goalOutSet = true
	a.lastGoalOut = TurnOutcome{Verdict: VerdictVerified, Edited: 1, Why: "ok"}
	a.keepReverts = maxKeepReverts
	if a.keepOrRevert(context.Background(), TurnOutcome{Verdict: VerdictCheckFailed, Edited: 1, Why: "no"}, func(Event) {}) {
		t.Fatal("consecutive reverts must be capped")
	}
}
