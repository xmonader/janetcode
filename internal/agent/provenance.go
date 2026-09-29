package agent

import (
	"bytes"
	"errors"
	"fmt"
	"github.com/xmonader/janetcode/internal/change"
	"github.com/xmonader/janetcode/internal/session"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/xmonader/janetcode/internal/diff"
	"github.com/xmonader/janetcode/internal/fsutil"
	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/textfmt"
	"github.com/xmonader/janetcode/internal/tool"
)

const (
	maxChanges      = 400
	maxChangesBytes = 16 << 20
)

// pending is the content of the paths a mutating call is about to touch.
type pending struct {
	path    string
	existed bool
	data    []byte
}

// before reads the paths a tool call will touch, for the provenance
// record; nil when the tool does not name its paths.
func (a *Agent) before(t tool.Tool, input []byte) []pending {
	tc, ok := t.(tool.Toucher)
	if !ok {
		return nil
	}
	paths, err := tc.Touches(input)
	if err != nil {
		return nil
	}
	var out []pending
	for _, p := range paths {
		pd := pending{path: p}
		if data, err := os.ReadFile(p); err == nil {
			pd.data, pd.existed = data, true
		} else if !errors.Is(err, os.ErrNotExist) {
			continue
		}
		out = append(out, pd)
	}
	return out
}

// after records a Change for every touched path whose content differs
// from what before saw. explicitIntent is the call's own stated intent —
// ToolCall.Intent, taken off the call's input by tool.TakeIntent when the
// intent feature is on — and is intentFor's first choice before it falls
// back to reading the transcript.
func (a *Agent) after(u llm.Block, explicitIntent string, pend []pending) {
	for _, p := range pend {
		data, err := os.ReadFile(p.path)
		exists := err == nil
		if !exists && !p.existed || exists && p.existed && bytes.Equal(data, p.data) {
			continue
		}
		a.record(Change{Turn: a.turnN, Call: u.ID, Tool: u.Name, Intent: a.intentFor(u.ID, explicitIntent), Path: p.path, Existed: p.existed, Before: p.data, After: data, Gone: !exists})
		a.logEvent(session.Event{Type: session.TypeFileChange, CallID: u.ID, Name: p.path, Arguments: contentDigest(p.data, p.existed) + "->" + contentDigest(data, exists)})
	}
}

// intentFor is why the model made a tool call, one clipped line, tried in
// order of how much it can be trusted:
//
//  1. explicitIntent, the call's own stated intent (ToolCall.Intent /
//     Change.Intent): when the intent feature is on, the model says why
//     in the call's own arguments, which needs no reconstruction and
//     survives a reasoning style — qwen and others — that puts its
//     explanation somewhere other than plain text before the call.
//  2. Text before the tool_use block in the same assistant message: the
//     traditional case, a model that narrates then calls the tool.
//  3. The first line of a thinking block in that same message, for a
//     model whose reasoning about the call lands there instead of in
//     visible text.
//  4. The last text of the previous assistant message earlier in the same
//     turn (bounded by a.cp.turnStart so an earlier turn's chatter is
//     never borrowed), for a model that explains itself once and then
//     just calls tools on later steps — unless that message's own calls
//     changed a file, in which case the text was that change's reason.
//
// Anything found is clipped to one line; finding nothing returns "".
func (a *Agent) intentFor(callID, explicitIntent string) string {
	if s := strings.TrimSpace(explicitIntent); s != "" {
		return intentLine(s, 120)
	}
	turnStart := a.cp.turnStart()
	callIdx := -1
	for i := len(a.Messages) - 1; i >= 0; i-- {
		m := a.Messages[i]
		if m.Role != llm.RoleAssistant {
			continue
		}
		var text, thinking string
		found := false
		for _, b := range m.Blocks {
			if b.Type == llm.BlockToolUse && b.ID == callID {
				found = true
				break
			}
			switch b.Type {
			case llm.BlockText:
				text = b.Text
			case llm.BlockThinking:
				thinking = b.Text
			}
		}
		if !found {
			continue
		}
		callIdx = i
		if s := strings.TrimSpace(text); s != "" {
			return intentLine(s, 120)
		}
		if s := strings.TrimSpace(thinking); s != "" {
			return intentLine(s, 120)
		}
		break
	}
	if callIdx < 0 {
		return ""
	}
	for i := callIdx - 1; i >= turnStart && i >= 0; i-- {
		m := a.Messages[i]
		if m.Role != llm.RoleAssistant {
			continue
		}
		if s := strings.TrimSpace(m.Text()); s != "" {
			if a.changedByMessage(m) {
				// That text already explains the change its own call made;
				// borrowing it would put one change's reason on another.
				return ""
			}
			return intentLine(s, 120)
		}
	}
	return ""
}

// changedByMessage reports whether any tool call in m recorded a change.
func (a *Agent) changedByMessage(m llm.Message) bool {
	calls := map[string]bool{}
	for _, b := range m.Blocks {
		if b.Type == llm.BlockToolUse {
			calls[b.ID] = true
		}
	}
	a.cp.mu.Lock()
	defer a.cp.mu.Unlock()
	for _, c := range a.cp.changes {
		if calls[c.Call] {
			return true
		}
	}
	return false
}

// intentLine is the first substantive line of s with any Markdown heading
// or list marker taken off, clipped to n runes. It differs from
// textfmt.HeadingLine in also dropping "*", "-" and "1." markers, which is
// how a model often opens the sentence explaining a call, and in skipping
// the openers a model writes before it says anything (see
// boilerplateOpener): "The user wants me to:" labelled every change of a
// real session. When every line is an opener, the first line is kept.
func intentLine(s string, n int) string {
	first := ""
	for _, line := range strings.Split(strings.TrimSpace(s), "\n") {
		line = strings.TrimSpace(listMarker.ReplaceAllString(strings.TrimSpace(line), ""))
		if line == "" {
			continue
		}
		if first == "" {
			first = line
		}
		if !boilerplateOpener(line) {
			return textfmt.Clip(line, n)
		}
	}
	return textfmt.Clip(first, n)
}

// listMarker is a leading Markdown heading, bullet or numbered-list marker.
var listMarker = regexp.MustCompile(`^(?:[#*-]+\s*|\d+[.)]\s+)`)

// openerPrefixes begin the lines a model writes before saying anything:
// restating the request, or announcing that it is about to act.
var openerPrefixes = []string{"the user wants", "the user is asking", "the user asked", "let me", "i'll", "i will", "i need to", "okay", "ok,", "alright"}

// boilerplateOpener reports whether a line says nothing about the change:
// one that ends in ":" introducing what follows, or one that opens the
// way a model announces itself.
func boilerplateOpener(line string) bool {
	if strings.HasSuffix(line, ":") {
		return true
	}
	lower := strings.ToLower(line)
	for _, p := range openerPrefixes {
		if strings.HasPrefix(lower, p) {
			return true
		}
	}
	return false
}

// BlameLine attributes one line of a file to the change that wrote it;
// Seq 0 means the line predates the session's changes.
type BlameLine struct {
	Seq  int
	Text string
}

// Blame replays the session's changes to path and says which change each
// line of its current content comes from. The note is set when the file
// on disk differs from the last recorded state (edited outside the
// session since), in which case the attribution is of that last state.
func (a *Agent) Blame(path string) (lines []BlameLine, note string, err error) {
	var changes []Change
	for _, c := range a.Changes() {
		if c.Path == path {
			changes = append(changes, c)
		}
	}
	if len(changes) == 0 {
		return nil, "", fmt.Errorf("%w: %s", ErrNoChange, path)
	}
	cur := diff.SplitLinesKeepEOL(string(changes[0].Before))
	owners := make([]int, len(cur))
	for _, c := range changes {
		next := diff.SplitLinesKeepEOL(string(c.After))
		var nextOwners []int
		oi := 0
		for _, e := range change.DiffLines(cur, next) {
			switch e.Op {
			case '=':
				nextOwners = append(nextOwners, owners[oi])
				oi++
			case '-':
				oi++
			case '+':
				nextOwners = append(nextOwners, c.Seq)
			}
		}
		cur, owners = next, nextOwners
	}
	if data, rerr := os.ReadFile(path); rerr != nil || !bytes.Equal(data, changes[len(changes)-1].After) {
		note = "the file changed outside the session after the last recorded change; attribution is of that last state"
	}
	for i, l := range cur {
		lines = append(lines, BlameLine{Seq: owners[i], Text: strings.TrimRight(l, "\r\n")})
	}
	return lines, note, nil
}

// UndoChange reverts one change on its own, leaving later changes to the
// same file in place: each hunk is located in the file as it is now and
// put back the way it was. A hunk whose lines were edited since refuses
// rather than guessing. The revert is recorded as a change itself.
func (a *Agent) UndoChange(seq int) (string, error) {
	c, err := a.Change(seq)
	if err != nil {
		return "", err
	}
	if c.Undone {
		return "", fmt.Errorf("#%d was already undone", seq)
	}
	data, rerr := os.ReadFile(c.Path)
	if rerr != nil && !errors.Is(rerr, os.ErrNotExist) {
		return "", rerr
	}
	exists := rerr == nil
	var verb string
	switch {
	case !c.Existed:
		if !exists || !bytes.Equal(data, c.After) {
			return "", fmt.Errorf("#%d created %s but the file was changed since; refusing to remove it", seq, c.Path)
		}
		if err := os.Remove(c.Path); err != nil {
			return "", err
		}
		verb = "removed"
	case c.Gone:
		// The change removed the file; undoing it puts it back, unless
		// something has put a file there since.
		if exists {
			return "", fmt.Errorf("#%d removed %s but a file is there again; refusing to overwrite it", seq, c.Path)
		}
		if err := os.MkdirAll(filepath.Dir(c.Path), 0o755); err != nil {
			return "", err
		}
		if err := fsutil.AtomicWrite(c.Path, c.Before); err != nil {
			return "", err
		}
		verb = "recreated"
	default:
		if !exists {
			return "", fmt.Errorf("#%d edited %s but the file is gone", seq, c.Path)
		}
		reverted, ok, badHunk := change.Revert(diff.SplitLinesKeepEOL(string(data)), c)
		if !ok {
			h := c.Hunks()[badHunk-1]
			return "", fmt.Errorf("hunk %d of #%d (%s) no longer applies: those lines were edited since", badHunk, seq, h.Header())
		}
		if err := fsutil.AtomicWrite(c.Path, []byte(strings.Join(reverted, ""))); err != nil {
			return "", err
		}
		verb = "restored"
		if hunks := c.Hunks(); len(hunks) > 0 {
			verb = fmt.Sprintf("reverted %d hunk(s) of", len(hunks))
		}
	}
	a.markUndone(seq)
	now, nerr := os.ReadFile(c.Path)
	a.record(Change{Turn: a.turnN, Tool: "undo", Intent: fmt.Sprintf("undo #%d: %s", seq, c.Intent), Path: c.Path, Existed: exists, Before: data, After: now, Gone: errors.Is(nerr, os.ErrNotExist)})
	// Relative to the session's root: the line is shown to the user and
	// handed to the model, and an absolute path is noise to both.
	return verb + " " + textfmt.RelTo(a.displayRoot(), c.Path), nil
}

// ChangeGroup is the changes made for one stated intent within a turn.
type ChangeGroup struct {
	Turn    int
	Intent  string
	Changes []Change
}
