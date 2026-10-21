package agent

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// The live plan file. The user keeps the plan in .janetcode/plan.md and
// edits it in their own editor while the agent works; before every model
// call the harness reads it, and when it changed since the last look the
// model is sent only the change, as a harness note — steering without
// interrupting the turn and without retyping the plan. The first look in
// a session sends the plan whole (the change from nothing).
//
// It is opt-in (config plan_file): it adds text to the conversation, and
// a plan.md that came with a cloned repository is not something the user
// wrote.

// PlanFilePath is where the live plan lives, relative to the project.
const PlanFilePath = ".janetcode/plan.md"

// planfileMaxBytes bounds the plan read: a file larger than this is not a
// plan, and is left alone.
const planfileMaxBytes = 64 << 10

// planfileMaxNote bounds the diff sent in one note.
const planfileMaxNote = 6000

// planfileState is what the harness last saw of the plan.
type planfileState struct {
	mu     sync.Mutex
	seen   bool
	last   string
	exists bool
}

// SetPlanFile turns the live plan file on or off.
func (a *Agent) SetPlanFile(on bool) {
	a.featMu.Lock()
	a.PlanFile = on
	a.featMu.Unlock()
}

// PlanFileOn is PlanFile, safe to read while a turn runs.
func (a *Agent) PlanFileOn() bool {
	a.featMu.RLock()
	defer a.featMu.RUnlock()
	return a.PlanFile
}

// planfileCheck queues the change to the plan since the last look, if
// any, for the model call about to be made. It runs on the turn
// goroutine before the steering notes are delivered, so the change goes
// out with them.
func (a *Agent) planfileCheck(emit func(Event)) {
	if !a.PlanFileOn() {
		return
	}
	path := filepath.Join(a.RootDir(), filepath.FromSlash(PlanFilePath))
	cur, exists, ok := planfileRead(path)
	if !ok {
		return
	}
	st := &a.planfile
	st.mu.Lock()
	first := !st.seen
	prev, prevExists := st.last, st.exists
	st.seen, st.last, st.exists = true, cur, exists
	st.mu.Unlock()
	if !first && cur == prev && exists == prevExists {
		return
	}
	if first && !exists {
		return
	}
	// The agent's own write to the plan is not the user steering: the
	// model already knows what it wrote.
	if !first && a.planfileOwnWrite(path, cur, exists) {
		return
	}
	note, added, removed := planfileNote(prev, cur, first, exists)
	if note == "" {
		return
	}
	a.queueHarness(note)
	// The user changing course is a decision the mechanical-streak policy
	// cannot see coming, as a typed steering note is.
	a.stepRoute.taint("the user edited the plan file")
	switch {
	case first:
		emit(Event{Kind: EvNotice, Text: fmt.Sprintf("plan file: sent %s (%d lines) to the model", PlanFilePath, added)})
	case !exists:
		emit(Event{Kind: EvNotice, Text: "plan file: " + PlanFilePath + " was deleted; told the model"})
	default:
		emit(Event{Kind: EvNotice, Text: fmt.Sprintf("plan file: %s changed (+%d −%d lines); sent the change to the model", PlanFilePath, added, removed)})
	}
}

// planfileSaw records the plan file as it now stands as already seen. The
// harness calls it after writing the file itself — capturePlan puts a
// plan-mode turn's plan there — so that write, which no file tool made
// and no change records, is not sent back as the user's edit.
func (a *Agent) planfileSaw() {
	path := filepath.Join(a.RootDir(), filepath.FromSlash(PlanFilePath))
	cur, exists, ok := planfileRead(path)
	if !ok {
		return
	}
	st := &a.planfile
	st.mu.Lock()
	st.seen, st.last, st.exists = true, cur, exists
	st.mu.Unlock()
}

// planfileRead reads the plan. ok is false when it cannot be read as a
// plan (too large, binary, a directory): the last look stands.
func planfileRead(path string) (text string, exists, ok bool) {
	st, err := os.Stat(path)
	if os.IsNotExist(err) {
		return "", false, true
	}
	if err != nil || st.IsDir() || st.Size() > planfileMaxBytes {
		return "", false, false
	}
	data, err := os.ReadFile(path)
	if err != nil || bytes.IndexByte(data, 0) >= 0 {
		return "", false, false
	}
	return strings.ReplaceAll(string(data), "\r\n", "\n"), true, true
}

// planfileOwnWrite reports whether the plan's current content is what
// this session's own last edit of it left there.
func (a *Agent) planfileOwnWrite(path, cur string, exists bool) bool {
	owner := a
	if a.UndoOwner != nil {
		owner = a.UndoOwner
	}
	changes := owner.Changes()
	for i := len(changes) - 1; i >= 0; i-- {
		c := changes[i]
		if filepath.Clean(c.Path) != filepath.Clean(path) {
			continue
		}
		if c.Gone {
			return !exists
		}
		return exists && strings.ReplaceAll(string(c.After), "\r\n", "\n") == cur
	}
	return false
}

// planfileNote is the note for the change from prev to cur, with how many
// lines it adds and removes. A first look sends the plan whole.
func planfileNote(prev, cur string, first, exists bool) (note string, added, removed int) {
	if first {
		lines := planfileLines(cur)
		if len(strings.TrimSpace(cur)) == 0 {
			return "", 0, 0
		}
		body := cur
		if len(body) > planfileMaxNote {
			body = body[:planfileMaxNote] + "\n[... plan cut; read " + PlanFilePath + " for the rest]"
		}
		return "[live plan] The user keeps the plan for this work in " + PlanFilePath + " and may edit it while you work; when they do, you will be sent only what changed. The plan as it stands:\n\n" + body, len(lines), 0
	}
	if !exists {
		return "[live plan] The user deleted " + PlanFilePath + ". Carry on with the request as they last stated it.", 0, len(planfileLines(prev))
	}
	diff, added, removed := planfileDiff(planfileLines(prev), planfileLines(cur))
	if added == 0 && removed == 0 {
		// Only blank-line or trailing-space churn: nothing to say.
		return "", 0, 0
	}
	if len(diff) > planfileMaxNote {
		diff = diff[:planfileMaxNote] + "\n[... change cut; read " + PlanFilePath + " for the rest]"
	}
	return "[live plan edited] The user just changed " + PlanFilePath + " while you were working. Only the change is shown (+ added, - removed, with a line of context):\n\n```diff\n" + diff + "```\n\nFollow the plan as it now stands from your next action on. If the change contradicts work you already did, say so and adjust it.", added, removed
}

// planfileLines splits a plan into lines, trailing spaces dropped and
// blank lines kept out of the comparison.
func planfileLines(s string) []string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		l = strings.TrimRight(l, " \t")
		if l != "" {
			out = append(out, l)
		}
	}
	return out
}

// planfileDiff is a line diff of a against b by longest common
// subsequence, each change shown with the unchanged line before it as
// context. Plans are short; past a size where the quadratic table would
// be wasteful, the whole of both sides is shown instead.
func planfileDiff(a, b []string) (string, int, int) {
	const most = 600
	var sb strings.Builder
	if len(a) > most || len(b) > most {
		for _, l := range a {
			sb.WriteString("- " + l + "\n")
		}
		for _, l := range b {
			sb.WriteString("+ " + l + "\n")
		}
		return sb.String(), len(b), len(a)
	}
	n, m := len(a), len(b)
	lcs := make([][]int, n+1)
	for i := range lcs {
		lcs[i] = make([]int, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if a[i] == b[j] {
				lcs[i][j] = lcs[i+1][j+1] + 1
			} else {
				lcs[i][j] = max(lcs[i+1][j], lcs[i][j+1])
			}
		}
	}
	added, removed := 0, 0
	context, shown := "", false
	emit := func(prefix, line string) {
		if !shown && context != "" {
			sb.WriteString("  " + context + "\n")
		}
		shown = true
		sb.WriteString(prefix + line + "\n")
	}
	i, j := 0, 0
	for i < n || j < m {
		switch {
		case i < n && j < m && a[i] == b[j]:
			context, shown = a[i], false
			i++
			j++
		case j < m && (i == n || lcs[i][j+1] >= lcs[i+1][j]):
			emit("+ ", b[j])
			added++
			j++
		default:
			emit("- ", a[i])
			removed++
			i++
		}
	}
	return sb.String(), added, removed
}
