package undoabout

import (
	"fmt"
	"github.com/xmonader/janetcode/internal/change"
	"sort"
	"strings"

	"github.com/xmonader/janetcode/internal/textfmt"
)

// FormatSelection renders a selection for confirmation: every candidate
// considered, numbered, with its path, reason and line counts, marked
// undo, keep or mixed, in seq order so the listing reads like /changes
// does, followed by the mixed changes' reasons and any coupling warnings
// shown prominently, since either one blocks /undo apply short of
// --force.
func FormatSelection(cwd string, sel Selection) string {
	all := append(append([]change.Change(nil), sel.Undo...), sel.Keep...)
	for _, m := range sel.Mixed {
		all = append(all, m.Change)
	}
	sort.Slice(all, func(i, j int) bool { return all[i].Seq < all[j].Seq })
	mark := make(map[int]string, len(all))
	for _, c := range sel.Undo {
		mark[c.Seq] = "undo"
	}
	for _, c := range sel.Keep {
		mark[c.Seq] = "keep"
	}
	for _, m := range sel.Mixed {
		mark[m.Change.Seq] = "mixed"
	}
	var sb strings.Builder
	if sel.Fallback {
		sb.WriteString("no model available for this: matched by keyword overlap, not meaning\n")
	}
	for _, c := range all {
		add, del := c.Stats()
		fmt.Fprintf(&sb, "[%-4s] #%-3d %-30s +%d -%d", mark[c.Seq], c.Seq, textfmt.RelTo(cwd, c.Path), add, del)
		if c.Intent != "" {
			fmt.Fprintf(&sb, "  · %s", c.Intent)
		}
		sb.WriteString("\n")
	}
	if len(all) == 0 {
		sb.WriteString("no changes to consider\n")
	}
	if sel.Why != "" {
		fmt.Fprintf(&sb, "\n%s\n", sel.Why)
	}
	if len(sel.Mixed) > 0 {
		sb.WriteString("\nmixed: these changes contain both matching and unrelated edits and were put on neither side:\n")
		for _, m := range sel.Mixed {
			fmt.Fprintf(&sb, "  #%d %s\n", m.Change.Seq, m.Why)
		}
	}
	if len(sel.Warnings) > 0 {
		sb.WriteString("\nwarning:\n")
		for _, w := range sel.Warnings {
			fmt.Fprintf(&sb, "  %s\n", w)
		}
	}
	if len(sel.Unavailable) > 0 {
		sb.WriteString("\nunavailable: these changes could not be located and were left out of undo and keep:\n")
		for _, u := range sel.Unavailable {
			fmt.Fprintf(&sb, "  #%d %s\n", u.Change.Seq, u.Why)
		}
	}
	switch {
	case len(sel.Undo) == 0 && len(sel.Mixed) == 0:
		sb.WriteString("\nnothing matched that description; /undo apply would do nothing\n")
	case len(sel.Mixed) > 0 || len(sel.Warnings) > 0:
		sb.WriteString("\n/undo apply refuses while the above stands; /undo apply --force overrides it\n")
	default:
		sb.WriteString("\n/undo apply reverts the changes marked undo\n")
	}
	return strings.TrimRight(sb.String(), "\n")
}

// FormatUndoReport renders what UndoSelection did: what was reverted,
// where it stopped, and — prominently, since staying silent about it is
// exactly the defect this guards against — how (or whether) the result
// was verified afterwards.
func FormatUndoReport(rep UndoReport) string {
	var sb strings.Builder
	for _, r := range rep.Reverted {
		fmt.Fprintf(&sb, "↶ %s\n", r)
	}
	if len(rep.Reverted) == 0 && rep.Refused == "" {
		sb.WriteString("nothing was reverted\n")
	}
	if rep.Refused != "" {
		fmt.Fprintf(&sb, "stopped: %s\n", rep.Refused)
	}
	switch {
	case rep.Checks != "":
		fmt.Fprintf(&sb, "%s\n", rep.Checks)
	case rep.CheckNote != "":
		fmt.Fprintf(&sb, "%s\n", rep.CheckNote)
		if rep.Diagnostics != "" {
			fmt.Fprintf(&sb, "%s\n", rep.Diagnostics)
		}
	}
	return strings.TrimRight(sb.String(), "\n")
}
