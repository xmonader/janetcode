package agent

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/session"
	"github.com/xmonader/janetcode/internal/snapshot"
	"github.com/xmonader/janetcode/internal/tool"
)

// loggedTurns is threeTurns with a real session file behind it: the
// conversation half of "/undo turn" is about what the log says, and the
// only thing that proves it is a round trip through one.
func loggedTurns(t *testing.T, responses []*llm.Response, extra ...tool.Tool) (*Agent, string, *session.Session, string) {
	t.Helper()
	a, root := newAgent(t, &fake{responses: responses}, nil)
	gitInit(t, root)
	repo, err := snapshot.Open(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	s, err := session.New(dir, root, "fake", "m")
	if err != nil {
		t.Fatal(err)
	}
	a.Snapshots = repo
	a.Log = s
	a.UndoPath = s.UndoPath()
	a.AutoApprove = true
	a.Tools = tool.NewRegistry(append([]tool.Tool{tool.WriteFile{Root: root}}, extra...)...)
	return a, root, s, dir
}

// writeTurns scripts one write_file call and a closing reply per turn.
func writeTurns(names ...string) []*llm.Response {
	var out []*llm.Response
	for _, n := range names {
		out = append(out, toolCall("w"+n, "write_file", `{"path":"`+n+`.txt","content":"`+n+`"}`))
		out = append(out, text("wrote "+n))
	}
	return out
}

func messageTexts(msgs []llm.Message) string {
	var sb strings.Builder
	for _, m := range msgs {
		sb.WriteString(m.Text())
		sb.WriteString("\n")
	}
	return sb.String()
}

// TestRewindDropsTheTurnsAndKeepsTheEarlierOnes is the whole point of
// the feature: after "/undo turn 2" the model's context no longer holds
// turns 2 and 3 — not their prompts, not their replies, not the tool
// results — and still holds turn 1 exactly as it was.
func TestRewindDropsTheTurnsAndKeepsTheEarlierOnes(t *testing.T) {
	a, root, sess, dir := loggedTurns(t, writeTurns("one", "two", "three"))
	for _, p := range []string{"do one", "do two", "do three"} {
		if err := a.Run(context.Background(), p, nil); err != nil {
			t.Fatal(err)
		}
	}
	before := len(a.Messages)

	tr, err := a.PreviewUndoTurn(context.Background(), 2, false)
	if err != nil {
		t.Fatal(err)
	}
	if tr.Conversation == nil {
		t.Fatalf("the preview says nothing about the conversation: %+v", tr)
	}
	if tr.Conversation.Turns != 2 {
		t.Fatalf("the preview drops %d turns, want turns 2 and 3", tr.Conversation.Turns)
	}
	// A preview touches neither the tree nor the conversation.
	if len(a.Messages) != before {
		t.Fatal("the preview changed the conversation")
	}
	sheet := FormatTurnRestore(root, tr)
	if !strings.Contains(sheet, "rewind this conversation") || !strings.Contains(sheet, "2 turns") {
		t.Fatalf("the preview does not say plainly what it drops:\n%s", sheet)
	}
	if !strings.Contains(sheet, "--files-only") {
		t.Fatalf("the preview does not offer the files-only spelling:\n%s", sheet)
	}

	rep, err := a.ApplyPendingUndo(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Rewound == nil || rep.Rewound.FromTurn != 2 || rep.Rewound.Turns != 2 {
		t.Fatalf("report does not describe the rewind: %+v", rep.Rewound)
	}
	got := messageTexts(a.Messages)
	if !strings.Contains(got, "do one") || !strings.Contains(got, "wrote one") {
		t.Fatalf("turn 1 did not survive the rewind:\n%s", got)
	}
	for _, gone := range []string{"do two", "wrote two", "do three", "wrote three"} {
		if strings.Contains(got, gone) {
			t.Fatalf("%q is still in the context after a rewind to before turn 2:\n%s", gone, got)
		}
	}
	last := a.Messages[len(a.Messages)-1]
	if !strings.Contains(last.Text(), "rewound") || !strings.Contains(last.Text(), "two.txt") {
		t.Fatalf("no note stands where the dropped turns were: %q", last.Text())
	}
	if last.Role != llm.RoleUser {
		t.Fatalf("the note reaches the model as %s, which no provider accepts last", last.Role)
	}

	// The log keeps every event it ever wrote — the rewind is one more
	// appended record, not a rewritten file.
	raw, err := os.ReadFile(sess.Path())
	if err != nil {
		t.Fatal(err)
	}
	for _, kept := range []string{"do two", "do three"} {
		if !strings.Contains(string(raw), kept) {
			t.Fatalf("the rewind rewrote history: %q is no longer in the session file", kept)
		}
	}

	// And a resume reads back the rewound conversation, not the dropped
	// turns: same fold, same answer.
	_, resumed, err := session.Open(dir, sess.Meta.ID)
	if err != nil {
		t.Fatal(err)
	}
	if messageTexts(resumed) != got {
		t.Fatalf("a resume sees a different conversation:\nlive:\n%s\nresumed:\n%s", got, messageTexts(resumed))
	}
}

// TestRewindDropsTheCheckpointsAndArchiveOfTheDroppedTurns: what the
// dropped turns left behind goes with them. The checkpoints because the
// tree no longer holds what they did (that is the old behaviour), the
// archived outputs because the messages naming their ids are gone, so
// expand_output could only hand the model the output of work that no
// longer exists.
func TestRewindDropsTheCheckpointsAndArchiveOfTheDroppedTurns(t *testing.T) {
	big := strings.Repeat("row of output\n", InlineOutput/8)
	responses := []*llm.Response{
		toolCall("w1", "write_file", `{"path":"one.txt","content":"one"}`),
		text("wrote one"),
		toolCall("b1", "bigtool", `{}`),
		toolCall("w2", "write_file", `{"path":"two.txt","content":"two"}`),
		text("wrote two"),
	}
	a, _, _, _ := loggedTurns(t, responses, bigTool{out: big})
	a.Archive = NewArchive(t.TempDir())
	for _, p := range []string{"do one", "do two"} {
		if err := a.Run(context.Background(), p, nil); err != nil {
			t.Fatal(err)
		}
	}
	if n := len(a.Archive.List()); n != 1 {
		t.Fatalf("%d archived outputs, want the one turn 2 made", n)
	}
	id := a.Archive.List()[0].ID

	tr, err := a.PreviewUndoTurn(context.Background(), 2, false)
	if err != nil {
		t.Fatal(err)
	}
	if tr.Conversation.Archived != 1 {
		t.Fatalf("the preview does not count the archived output: %+v", tr.Conversation)
	}
	if !strings.Contains(FormatTurnRestore("", tr), "archived tool output") {
		t.Fatalf("the preview does not say the archived output goes too:\n%s", FormatTurnRestore("", tr))
	}
	if _, err := a.ApplyPendingUndo(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	if _, _, err := a.Archive.Get(id); !errors.Is(err, ErrArchiveMissing) {
		t.Fatalf("%s outlived the turn that made it: %v", id, err)
	}
	if got := a.RestorableTurns(); len(got) != 1 || got[0] != 1 {
		t.Fatalf("restorable turns after the rewind = %v, want only turn 1", got)
	}
}

// TestUndoTurnFilesOnlyKeepsTheConversation: the old behaviour is still
// there under a name, and the preview says plainly that the model will
// go on believing it did the work.
func TestUndoTurnFilesOnlyKeepsTheConversation(t *testing.T) {
	a, root, _, _ := loggedTurns(t, writeTurns("one", "two"))
	for _, p := range []string{"do one", "do two"} {
		if err := a.Run(context.Background(), p, nil); err != nil {
			t.Fatal(err)
		}
	}
	before := messageTexts(a.Messages)

	tr, err := a.PreviewUndoTurn(context.Background(), 2, true)
	if err != nil {
		t.Fatal(err)
	}
	if tr.Conversation != nil {
		t.Fatalf("--files-only previewed a rewind: %+v", tr.Conversation)
	}
	if sheet := FormatTurnRestore(root, tr); !strings.Contains(sheet, "--files-only") || !strings.Contains(sheet, "goes on believing") {
		t.Fatalf("the preview does not say what --files-only leaves behind:\n%s", sheet)
	}
	rep, err := a.ApplyPendingUndo(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Rewound != nil {
		t.Fatalf("--files-only rewound the conversation anyway: %+v", rep.Rewound)
	}
	if _, err := os.Stat(filepath.Join(root, "two.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("the files were not restored")
	}
	if !strings.HasPrefix(messageTexts(a.Messages), before) {
		t.Fatalf("--files-only changed the conversation:\nwas:\n%s\nnow:\n%s", before, messageTexts(a.Messages))
	}
	// The old note is still how the model finds out, since nothing in
	// the conversation itself changed.
	a.TellModelUndone(rep)
	if !strings.Contains(messageTexts(a.Messages), "reverted some of your earlier changes") {
		t.Fatal("--files-only left the model with no word of the revert at all")
	}
}

// TestRewindLeavesCompactionAccountingConsistent: the context manager
// indexes the history by position — lastInputMsgs says how many messages
// the last call's token count already covers — so a rewind that left
// those numbers in place would price a conversation that no longer
// exists, and the integrity check would report the shrink as a fault.
func TestRewindLeavesCompactionAccountingConsistent(t *testing.T) {
	a, _, _, _ := loggedTurns(t, append(writeTurns("one", "two"), text("after")))
	for _, p := range []string{"do one", "do two"} {
		if err := a.Run(context.Background(), p, nil); err != nil {
			t.Fatal(err)
		}
	}
	// As after a real model call: the last request covered every message
	// there was, and its input tokens stand for them.
	a.lastInputMsgs, a.lastInputTokens = len(a.Messages), 90000
	a.integ.lastLen = len(a.Messages)

	if _, err := a.PreviewUndoTurn(context.Background(), 2, false); err != nil {
		t.Fatal(err)
	}
	if _, err := a.ApplyPendingUndo(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	if a.lastInputMsgs != 0 || a.lastInputTokens != 0 {
		t.Fatalf("the token accounting still covers %d messages worth %d tokens that are gone", a.lastInputMsgs, a.lastInputTokens)
	}
	if est := a.TokenEstimate(); est > 50000 {
		t.Fatalf("the estimate is ~%d tokens: it still prices the dropped turns", est)
	}
	// And the next turn's integrity check does not report the shrink as
	// a fault: it was a legitimate rewrite, like a compaction.
	evs, emit := collect()
	if err := a.Run(context.Background(), "carry on", emit); err != nil {
		t.Fatal(err)
	}
	for _, e := range *evs {
		if e.Kind == EvNotice && strings.Contains(e.Text, "history shrank") {
			t.Fatalf("the rewind was reported as a fault: %q", e.Text)
		}
	}
}

// TestRewindRefusesWhenTheContextMoved: a compaction replaces a whole
// head with one summary, and the checkpoint's message count then points
// somewhere else entirely. Rather than cutting the conversation at the
// wrong place, the restore says the point cannot be found and leaves the
// conversation alone — the files still go back.
func TestRewindRefusesWhenTheContextMoved(t *testing.T) {
	a, _, _, _ := loggedTurns(t, writeTurns("one", "two"))
	for _, p := range []string{"do one", "do two"} {
		if err := a.Run(context.Background(), p, nil); err != nil {
			t.Fatal(err)
		}
	}
	// Stand in for a compaction: the history is rewritten to a summary
	// plus a short tail, so the recorded counts no longer index it.
	a.setMessages([]llm.Message{
		llm.HarnessMessage(llm.RoleUser, "summary of the conversation so far"),
		llm.TextMessage(llm.RoleAssistant, "understood"),
	})
	tr, err := a.PreviewUndoTurn(context.Background(), 2, false)
	if err != nil {
		t.Fatal(err)
	}
	if tr.Conversation != nil {
		t.Fatalf("a rewind was offered into a conversation that moved: %+v", tr.Conversation)
	}
	if !strings.Contains(tr.NoRewind, "compaction") {
		t.Fatalf("the preview does not say why the conversation stays: %q", tr.NoRewind)
	}
	if !strings.Contains(FormatTurnRestore("", tr), tr.NoRewind) {
		t.Fatal("the sheet does not pass the reason on to the user")
	}
	rep, err := a.ApplyPendingUndo(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Rewound != nil {
		t.Fatalf("the conversation was cut anyway: %+v", rep.Rewound)
	}
	if len(a.Messages) != 2 {
		t.Fatalf("the conversation was cut anyway: %d messages", len(a.Messages))
	}
	// With no rewind to speak for it, the model is told the old way.
	a.TellModelUndone(rep)
	if !strings.Contains(messageTexts(a.Messages), "reverted some of your earlier changes") {
		t.Fatal("nothing told the model the files went back")
	}
}

// rewindTool asks for a turn restore from inside a running turn, which
// is the one thing that must never work: the rewind rewrites the very
// history the turn goroutine is appending to.
type rewindTool struct {
	a   *Agent
	err error
}

func (rewindTool) Name() string        { return "rewindnow" }
func (rewindTool) Description() string { return "" }
func (rewindTool) Mutating() bool      { return false }
func (rewindTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object"}`)
}
func (r *rewindTool) Run(ctx context.Context, _ json.RawMessage) (string, error) {
	_, r.err = r.a.PreviewUndoTurn(ctx, 1, false)
	return "asked", nil
}

// TestRestoreToTurnRefusesWhileATurnIsRunning: both front-ends keep
// /undo off the key handler mid-turn (slash.idleOnly), and the agent
// refuses on its own account too, so a path that forgets cannot corrupt
// the conversation.
func TestRestoreToTurnRefusesWhileATurnIsRunning(t *testing.T) {
	rt := &rewindTool{}
	a, _, _, _ := loggedTurns(t, []*llm.Response{
		toolCall("w1", "write_file", `{"path":"one.txt","content":"one"}`),
		text("wrote one"),
		toolCall("r1", "rewindnow", `{}`),
		text("asked"),
	}, rt)
	rt.a = a
	for _, p := range []string{"do one", "now rewind"} {
		if err := a.Run(context.Background(), p, nil); err != nil {
			t.Fatal(err)
		}
	}
	if !errors.Is(rt.err, ErrTurnRunning) {
		t.Fatalf("a restore from inside a running turn returned %v", rt.err)
	}
	// Between turns the same call is allowed again.
	if _, err := a.PreviewUndoTurn(context.Background(), 1, false); err != nil {
		t.Fatalf("the flag outlived the turn: %v", err)
	}
	if _, err := a.ApplyPendingUndo(context.Background(), false); err != nil {
		t.Fatal(err)
	}
}
