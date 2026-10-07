package agent

import (
	"fmt"
	"strings"
	"time"

	"github.com/xmonader/janetcode/internal/gitx"
	"github.com/xmonader/janetcode/internal/textfmt"
)

// The checkpoints behind /undo were only visible through /changes, which
// lists edits, not the restore points: nothing said which turns could be
// put back, what each one covered, or whether it could be trusted to put
// back everything. /checkpoints (and esc esc in the terminal) is that
// list; restoring one is "/undo turn <n>", a preview that "/undo apply"
// carries out.

// CheckpointInfo is one restore point, as /checkpoints lists it.
type CheckpointInfo struct {
	// ID is what the list calls it: "t<turn>", plus the git snapshot's
	// short hash when there is one.
	ID   string    `json:"id"`
	Turn int       `json:"turn"`
	When time.Time `json:"when"`
	// Intent is what the turn said it was doing, from its first recorded
	// change; empty when it said nothing a change carried.
	Intent string `json:"intent,omitempty"`
	// Tree says a git snapshot of the whole work tree backs it, so shell
	// changes restore too; without one only Files can be put back.
	Tree bool `json:"tree"`
	// Files are the paths the file tools changed, whose copies it holds.
	Files []string `json:"files"`
	// Changed is how many paths the turn left different, from its
	// manifest; -1 when it has none (a checkpoint older than manifests).
	Changed int `json:"changed"`
	// Scoped is the manifest's promise: an undo touches only what the
	// turn itself changed and leaves later edits alone.
	Scoped bool `json:"scoped"`
	// Complete is false when some file could not be copied (unreadable,
	// or over the size a checkpoint copies) and nothing else covers it;
	// Gaps says which, and why.
	Complete bool     `json:"complete"`
	Gaps     []string `json:"gaps,omitempty"`
}

// CheckpointList describes every restore point, oldest first.
func (a *Agent) CheckpointList() []CheckpointInfo {
	cps := a.Checkpoints()
	intents := map[int]string{}
	for _, g := range a.ChangeGroups() {
		if _, ok := intents[g.Turn]; !ok && strings.TrimSpace(g.Intent) != "" {
			intents[g.Turn] = g.Intent
		}
	}
	out := make([]CheckpointInfo, 0, len(cps))
	for _, cp := range cps {
		ci := CheckpointInfo{Turn: cp.Turn, When: cp.When, Intent: intents[cp.Turn], Tree: cp.Snap != nil, Scoped: cp.Manifested, Complete: true, Changed: -1}
		ci.ID = fmt.Sprintf("t%d", cp.Turn)
		if cp.Snap != nil {
			short := gitx.Short(cp.Snap.Commit)
			if len(short) > 7 {
				short = short[:7]
			}
			ci.ID += "·" + short
		}
		if cp.Manifested {
			ci.Changed = len(cp.Touched)
		}
		for _, f := range cp.Files {
			ci.Files = append(ci.Files, f.Path)
			switch {
			case f.Failed && !ci.Tree:
				ci.Complete = false
				ci.Gaps = append(ci.Gaps, f.Path+": could not be read before the turn")
			case f.TooLarge && !ci.Tree:
				ci.Complete = false
				ci.Gaps = append(ci.Gaps, f.Path+": "+tooLargeReason(f))
			}
		}
		out = append(out, ci)
	}
	return out
}

// FormatCheckpoints renders the list for a sheet: one block per restore
// point, newest first, with how to restore it.
func FormatCheckpoints(root string, list []CheckpointInfo) string {
	if len(list) == 0 {
		return "no checkpoints yet: one is taken before the first change of every turn that changes something"
	}
	var sb strings.Builder
	for i := len(list) - 1; i >= 0; i-- {
		c := list[i]
		fmt.Fprintf(&sb, "%-12s turn %d · %s ago", c.ID, c.Turn, textfmt.Age(time.Since(c.When)))
		if c.Intent != "" {
			sb.WriteString(" · " + textfmt.Clip(c.Intent, 60))
		}
		sb.WriteString("\n")
		sb.WriteString("             " + c.Coverage(root) + "\n")
		for _, g := range c.Gaps {
			path, why, _ := strings.Cut(g, ": ")
			sb.WriteString("             ✗ " + textfmt.RelTo(root, path) + ": " + why + "\n")
		}
	}
	sb.WriteString("\n/undo turn <n> previews putting the tree (and the conversation) back to before turn n · /undo apply carries it out · /undo reverts the newest")
	return strings.TrimRight(sb.String(), "\n")
}

// Coverage says, in one line, what restoring this point puts back and
// how far it can be trusted.
func (c CheckpointInfo) Coverage(root string) string {
	var parts []string
	if c.Tree {
		parts = append(parts, "whole work tree (git snapshot)")
	} else {
		parts = append(parts, "file copies only — shell changes are not covered")
	}
	if len(c.Files) > 0 {
		names := make([]string, 0, len(c.Files))
		for i, f := range c.Files {
			if i == 3 {
				names = append(names, fmt.Sprintf("+%d more", len(c.Files)-3))
				break
			}
			names = append(names, textfmt.RelTo(root, f))
		}
		parts = append(parts, strings.Join(names, ", "))
	}
	if c.Changed >= 0 {
		parts = append(parts, textfmt.Plural(c.Changed, "path")+" changed")
	}
	switch {
	case !c.Complete:
		parts = append(parts, "incomplete")
	case c.Scoped:
		parts = append(parts, "complete · later edits are left alone")
	default:
		parts = append(parts, "complete · no manifest, so undo restores everything it holds")
	}
	return strings.Join(parts, " · ")
}
