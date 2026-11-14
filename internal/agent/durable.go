package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/session"
	"github.com/xmonader/janetcode/internal/tool"
)

// Durable turns: continuing the turn a session died in. The design is in
// docs/notes/durable-turns.md; in short, a turn that started with the
// session.TurnLogsEnd marker and has no turn/end is one the process died
// in, and ContinueInterrupted carries it on as the next turn, with no new
// user message: the step's finished results as they were, reads that were
// running run again, writes that were running described rather than
// repeated, and calls that never started reported as not run.

// ErrNothingToContinue is ContinueInterrupted on a session whose last turn
// ended, or never started.
var ErrNothingToContinue = errors.New("the last turn ended; there is nothing to continue")

// resumeState is the turn the next one continues.
type resumeState struct {
	turn  int
	calls []session.PendingCall
}

// Interrupted is the turn this session died in, or nil.
func (a *Agent) Interrupted() *session.Interrupted {
	if a.Log == nil {
		return nil
	}
	return session.FindInterrupted(a.logEvents())
}

// InterruptedNotice is the one line a front-end shows on opening a
// session that died mid-turn, or "" when it did not.
func (a *Agent) InterruptedNotice(how string) string {
	n := a.interruptedNotice(how)
	if o := a.OrphanNotice(); o != "" {
		if n != "" {
			n += "\n"
		}
		n += o
	}
	return n
}

func (a *Agent) interruptedNotice(how string) string {
	it := a.Interrupted()
	if it == nil {
		return ""
	}
	if len(it.Calls) == 0 {
		return fmt.Sprintf("turn %d was interrupted while waiting for the model; %s carries it on", it.Turn, how)
	}
	done, running, queued := 0, 0, 0
	for _, c := range it.Calls {
		switch {
		case c.Done:
			done++
		case c.Started:
			running++
		default:
			queued++
		}
	}
	return fmt.Sprintf("turn %d was interrupted during its tool calls (%d finished, %d running, %d not started); %s carries it on", it.Turn, done, running, queued, how)
}

// ContinueInterrupted carries on the turn the session died in, as a new
// turn with no new user message. It is RunBlocks for that case: the same
// loop, gates and logging.
func (a *Agent) ContinueInterrupted(ctx context.Context, emit func(Event)) error {
	it := a.Interrupted()
	if it == nil {
		return ErrNothingToContinue
	}
	if o := a.OrphanNotice(); o != "" {
		a.AddHarnessContext(o)
		a.orphans = nil
	}
	a.resume = &resumeState{turn: it.Turn, calls: it.Calls}
	return a.RunBlocks(ctx, nil, emit)
}

// logResumedResults is beginTurn's part of a continuation when the dead
// step's tool calls are unanswered: the turn/start is logged, and the
// answers are worked out and appended by recoverCalls once openTurn has
// the turn's event sink. Here it only remembers what to recover.
func (a *Agent) logResumedResults(r *resumeState) {
	a.resumeCalls, a.resumeOf = r.calls, r.turn
}

// recoverCalls answers the dead step's calls and puts the answers where
// the step's results would have gone: in place of the fold's stand-in
// answers, on the history and on the log.
func (a *Agent) recoverCalls(ctx context.Context, emit func(Event)) {
	calls := a.resumeCalls
	a.resumeCalls = nil
	if len(calls) == 0 {
		return
	}
	a.setEmitter(emit)
	ids := map[string]bool{}
	for _, c := range calls {
		ids[c.ReplyID] = true
	}
	// The fold answered the orphans so the history was valid to show; the
	// real answers replace that message. Through setMessages, like every
	// other change to the history, because a front-end may be reading it.
	if n := len(a.Messages); n > 0 && a.Messages[n-1].Role == llm.RoleUser && answersOnly(a.Messages[n-1], ids) {
		a.setMessages(a.Messages[:n-1])
	}
	blocks := make([]llm.Block, 0, len(calls))
	for _, c := range calls {
		out, isErr := a.recoverCall(ctx, c, emit)
		blocks = append(blocks, llm.ToolResult(c.ReplyID, out, isErr))
	}
	msg := llm.ToolResultMessage(blocks...)
	a.appendMessage(msg)
	a.logEvent(session.Event{Type: session.TypeToolResult, Turn: a.turnN, Surface: true, Message: &msg,
		Reason: "recovered from an interrupted turn"})
	emit(Event{Kind: EvNotice, Text: fmt.Sprintf("continuing turn %d: %d of its tool calls answered", a.resumeOf, len(calls))})
}

// answersOnly reports whether m holds nothing but tool results for ids.
func answersOnly(m llm.Message, ids map[string]bool) bool {
	if len(m.Blocks) == 0 {
		return false
	}
	for _, b := range m.Blocks {
		if b.Type != llm.BlockToolResult || !ids[b.ToolUseID] {
			return false
		}
	}
	return true
}

// recoverCall answers one call of the dead step.
func (a *Agent) recoverCall(ctx context.Context, c session.PendingCall, emit func(Event)) (string, bool) {
	switch {
	case c.Done:
		return c.Output, c.IsError
	case !c.Started:
		return session.OrphanNotStarted, true
	}
	t, ok := a.Tools.Get(c.Name)
	if ok && !tool.MutatingCall(t, c.Input) {
		// A read that was running: reading again is harmless, and the
		// model gets what it asked for instead of an unknown.
		u := llm.Block{Type: llm.BlockToolUse, ID: c.ID, ReplyID: c.ReplyID, Name: c.Name, Input: c.Input}
		out, _, isErr, err := a.execute(ctx, u, emit)
		if err != nil {
			return session.OrphanRunning, true
		}
		return "(run again after the session restarted) " + out, isErr
	}
	// A change that was running is never repeated: half of it may be on
	// disk. Say what can be known about it.
	return describeInterrupted(a.RootDir(), c), true
}

// describeInterrupted is the answer for a mutating call that was running
// when the session died: it is not run again, and the evidence about
// whether it took effect comes with it.
func describeInterrupted(root string, c session.PendingCall) string {
	head := fmt.Sprintf("%s was running when the session ended and is not run again: it may or may not have taken effect.", c.Name)
	var in struct {
		Path      string `json:"path"`
		Content   string `json:"content"`
		OldString string `json:"old_string"`
		NewString string `json:"new_string"`
		Edits     []struct {
			OldString string `json:"old_string"`
			NewString string `json:"new_string"`
		} `json:"edits"`
	}
	_ = json.Unmarshal(c.Input, &in)
	path := in.Path
	if path != "" && !filepath.IsAbs(path) && root != "" {
		path = filepath.Join(root, path)
	}
	switch c.Name {
	case "write_file":
		if path == "" {
			break
		}
		data, err := os.ReadFile(path)
		switch {
		case errors.Is(err, os.ErrNotExist):
			return head + " " + in.Path + " does not exist now: the write did not happen."
		case err != nil:
		case string(data) == in.Content:
			return head + " " + in.Path + " now holds exactly the content this call writes: it completed."
		default:
			return head + fmt.Sprintf(" %s exists but does not hold this call's content (%d bytes now): the write did not complete. Read it before changing it.", in.Path, len(data))
		}
	case "edit_file":
		if path == "" {
			break
		}
		edits := in.Edits
		if in.OldString != "" || in.NewString != "" {
			edits = append(edits, struct {
				OldString string `json:"old_string"`
				NewString string `json:"new_string"`
			}{in.OldString, in.NewString})
		}
		data, err := os.ReadFile(path)
		if err != nil || len(edits) == 0 {
			break
		}
		text := string(data)
		applied, pending := 0, 0
		for _, e := range edits {
			hasNew := e.NewString != "" && strings.Contains(text, e.NewString)
			hasOld := e.OldString != "" && strings.Contains(text, e.OldString)
			switch {
			case hasNew && !hasOld:
				applied++
			case hasOld && !hasNew:
				pending++
			}
		}
		switch {
		case applied == len(edits):
			return head + " " + in.Path + " holds this call's new text and not the old: it appears to have completed."
		case pending == len(edits):
			return head + " " + in.Path + " still holds the old text: it did not take effect."
		}
		return head + " " + in.Path + " is neither before nor after this edit; read it before changing it."
	}
	if st := gitStatus(root); st != "" {
		return head + " What the working tree shows now (git status --short):\n" + st
	}
	return head + " Check the state it would have changed before assuming either way."
}

// gitStatus is a short `git status --short` of root, "" when it is not a
// repository or git is slow or absent.
func gitStatus(root string) string {
	if root == "" {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "git", "-C", root, "status", "--short").Output()
	if err != nil {
		return ""
	}
	lines := strings.Split(strings.TrimRight(string(bytes.TrimSpace(out)), "\n"), "\n")
	if len(lines) == 1 && lines[0] == "" {
		return "(clean)"
	}
	if len(lines) > 40 {
		lines = append(lines[:40], fmt.Sprintf("… %d more", len(lines)-40))
	}
	return strings.Join(lines, "\n")
}
