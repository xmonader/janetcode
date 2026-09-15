package agent

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/xmonader/janetcode/internal/diff"
	"github.com/xmonader/janetcode/internal/fsutil"
	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/textfmt"
	"github.com/xmonader/janetcode/internal/tool"
)

// Change is one file changed by one tool call: the unit of provenance.
// Before and After are the file's content around the call, so a single
// decision can be shown, blamed and reverted on its own after later
// changes landed on top.
type Change struct {
	Seq     int       `json:"seq"`
	Turn    int       `json:"turn"`
	Call    string    `json:"call"`
	Tool    string    `json:"tool"`
	Intent  string    `json:"intent"`
	When    time.Time `json:"when"`
	Path    string    `json:"path"`
	Existed bool      `json:"existed"`
	Before  []byte    `json:"before,omitempty"`
	After   []byte    `json:"after,omitempty"`
	Undone  bool      `json:"undone,omitempty"`
}

// Hunks is the change as a unified diff.
func (c Change) Hunks() []Hunk {
	return makeHunks(diffLines(diff.SplitLinesKeepEOL(string(c.Before)), diff.SplitLinesKeepEOL(string(c.After))))
}

// Stats counts the lines the change added and removed.
func (c Change) Stats() (add, del int) {
	for _, h := range c.Hunks() {
		a, d := h.stats()
		add += a
		del += d
	}
	return add, del
}

const (
	maxChanges      = 400
	maxChangesBytes = 16 << 20
)

// ErrNoSuchChange is returned for an unknown change number.
var ErrNoSuchChange = errors.New("no such change")

// ErrNoChange is returned by Blame when this session never changed the
// path, so a caller can fall back to committed history (see
// internal/proof.BlameCommitted) instead of matching the message text.
var ErrNoChange = errors.New("no change to this path in this session")

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
		a.record(Change{Turn: a.Stats().Turns, Call: u.ID, Tool: u.Name, Intent: a.intentFor(u.ID, explicitIntent), Path: p.path, Existed: p.existed, Before: p.data, After: data})
	}
}

func (a *Agent) record(c Change) {
	a.cp.mu.Lock()
	defer a.cp.mu.Unlock()
	a.cp.seq++
	c.Seq = a.cp.seq
	c.When = time.Now()
	a.cp.changes = append(a.cp.changes, c)
	a.cp.dirty = true
	total := 0
	for _, x := range a.cp.changes {
		total += len(x.Before) + len(x.After)
	}
	for len(a.cp.changes) > maxChanges || total > maxChangesBytes && len(a.cp.changes) > 1 {
		total -= len(a.cp.changes[0].Before) + len(a.cp.changes[0].After)
		a.cp.changes = a.cp.changes[1:]
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

// Changes lists the session's file changes, oldest first, with content.
func (a *Agent) Changes() []Change {
	a.cp.mu.Lock()
	defer a.cp.mu.Unlock()
	return append([]Change(nil), a.cp.changes...)
}

// Change finds one change by number.
func (a *Agent) Change(seq int) (Change, error) {
	a.cp.mu.Lock()
	defer a.cp.mu.Unlock()
	for _, c := range a.cp.changes {
		if c.Seq == seq {
			return c, nil
		}
	}
	return Change{}, fmt.Errorf("%w: #%d", ErrNoSuchChange, seq)
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
		for _, e := range diffLines(cur, next) {
			switch e.op {
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
	default:
		if !exists {
			return "", fmt.Errorf("#%d edited %s but the file is gone", seq, c.Path)
		}
		reverted, ok, badHunk := revertContent(diff.SplitLinesKeepEOL(string(data)), c)
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
	a.cp.mu.Lock()
	for i := range a.cp.changes {
		if a.cp.changes[i].Seq == seq {
			a.cp.changes[i].Undone = true
		}
	}
	a.cp.mu.Unlock()
	now, _ := os.ReadFile(c.Path)
	a.record(Change{Turn: a.Stats().Turns, Tool: "undo", Intent: fmt.Sprintf("undo #%d: %s", seq, c.Intent), Path: c.Path, Existed: exists, Before: data, After: now})
	// Relative to the session's root: the line is shown to the user and
	// handed to the model, and an absolute path is noise to both.
	return verb + " " + textfmt.RelTo(a.displayRoot(), c.Path), nil
}

// revertContent computes what an existing file's content would be if
// change c were reverted on top of cur (its current lines, e.g. from
// splitLines), without writing anything: each hunk is located in cur and
// put back the way it was, newest hunk first, same as UndoChange itself
// does — this is UndoChange's only implementation of that, factored out so
// a caller that wants to know the answer without writing (the undo
// coupling guard, see undo_coupling.go) can never drift from what actually
// happens on disk. ok is false when some hunk's lines could not be found
// (edited since c was recorded); badHunk is that hunk's 1-based index
// among c.Hunks() for the caller's own message, 0 when ok.
func revertContent(cur []string, c Change) (reverted []string, ok bool, badHunk int) {
	lines := append([]string(nil), cur...)
	hunks := c.Hunks()
	for i := len(hunks) - 1; i >= 0; i-- {
		h := hunks[i]
		old, new := h.sides()
		at, found := locate(lines, new, h.NewStart-1)
		if !found {
			return nil, false, i + 1
		}
		lines = append(append(append([]string{}, lines[:at]...), old...), lines[at+len(new):]...)
	}
	return lines, true, 0
}

// locate finds block in lines, preferring the occurrence nearest to hint.
func locate(lines, block []string, hint int) (int, bool) {
	if len(block) == 0 {
		return min(max(hint, 0), len(lines)), true
	}
	best, found := 0, false
	for i := 0; i+len(block) <= len(lines); i++ {
		match := true
		for j := range block {
			if lines[i+j] != block[j] {
				match = false
				break
			}
		}
		if match && (!found || abs(i-hint) < abs(best-hint)) {
			best, found = i, true
		}
	}
	return best, found
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

// ChangeGroup is the changes made for one stated intent within a turn.
type ChangeGroup struct {
	Turn    int
	Intent  string
	Changes []Change
}

// ChangeGroups lists the session's changes grouped by turn and intent,
// oldest first.
func (a *Agent) ChangeGroups() []ChangeGroup {
	var groups []ChangeGroup
	for _, c := range a.Changes() {
		if n := len(groups); n > 0 && groups[n-1].Turn == c.Turn && groups[n-1].Intent == c.Intent {
			groups[n-1].Changes = append(groups[n-1].Changes, c)
			continue
		}
		groups = append(groups, ChangeGroup{Turn: c.Turn, Intent: c.Intent, Changes: []Change{c}})
	}
	sort.SliceStable(groups, func(i, j int) bool { return groups[i].Turn < groups[j].Turn })
	return groups
}
