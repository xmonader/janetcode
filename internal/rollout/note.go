package rollout

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/xmonader/janetcode/internal/agent"
	"github.com/xmonader/janetcode/internal/session"
)

// noteBudget bounds the branch note: one line, so the branch's prompt is
// the original prefix plus a sentence, not a second briefing.
const noteBudget = 300

// BranchNote is the one line the live part of a branch is told: what the
// failed attempt did after the critical step and how it ended, so the
// branch does not walk the same way into the same wall. It is built from
// the events, not asked of a model. root, when set, is made relative in
// the paths it names.
func BranchNote(events []session.Event, c Critical, end End, root string) string {
	files := map[string]bool{}
	edits, calls := 0, 0
	step := 0
	last := ""
	for _, e := range events {
		switch e.Type {
		case session.TypeAssistant:
			if e.Message != nil && e.SurfaceOp != session.SurfaceOpReplace {
				step++
			}
		case session.TypeToolCall:
			if step <= c.Step || session.IsNestedCall(e.CallID) {
				continue
			}
			calls++
			last = e.Name
			if agent.MutatingTool(e.Name) {
				edits++
				if p := wastePathOf(e.Arguments); p != "" {
					files[rel(p, root)] = true
				}
			}
		}
	}
	var names []string
	for f := range files {
		names = append(names, f)
	}
	sort.Strings(names)
	if len(names) > 4 {
		names = append(names[:4], fmt.Sprintf("%d more", len(names)-4))
	}
	what := fmt.Sprintf("made %d tool calls", calls)
	if edits > 0 {
		what = fmt.Sprintf("made %d tool calls, %d of them edits to %s", calls, edits, strings.Join(names, ", "))
	}
	how := string(end)
	switch end {
	case EndStuck:
		how = "stuck repeating the same " + orTool(last) + " call"
	case EndTurnLimit:
		how = "out of model calls"
	case EndToolLimit:
		how = "at the tool-call limit"
	case EndGateRed:
		how = "with the project's checks still failing"
	case EndUntilRed:
		how = "with the --until check still failing"
	}
	n := fmt.Sprintf("[harness] An earlier attempt from this point %s and ended %s. Take a different approach this time.", what, how)
	if len(n) > noteBudget {
		n = n[:noteBudget-1] + "…"
	}
	return n
}

func orTool(name string) string {
	if name == "" {
		return "tool"
	}
	return name
}

func rel(p, root string) string {
	if root != "" && filepath.IsAbs(p) {
		if r, err := filepath.Rel(root, p); err == nil && !strings.HasPrefix(r, "..") {
			return filepath.ToSlash(r)
		}
	}
	return filepath.ToSlash(p)
}

// wastePathOf is the path a file tool's arguments name.
func wastePathOf(arguments string) string {
	keys := exploreKeys("read_file", arguments)
	if len(keys) == 0 {
		return ""
	}
	return strings.TrimPrefix(keys[0], "file\x00")
}
