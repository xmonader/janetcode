package agent

import (
	"fmt"
	"strings"

	"github.com/xmonader/janetcode/internal/textfmt"
)

// FormatChangeGroups renders /changes' list for every front-end: one
// header per turn, and under it each stated intent with the changes made
// for it. A turn with a single intent puts it on the header line. Paths
// are shown relative to root. Grouping by turn and intent (ChangeGroups)
// used to print "turn N · intent" above every row whenever each change
// had its own intent, repeating the turn number down the list.
func FormatChangeGroups(groups []ChangeGroup, root string) string {
	var sb strings.Builder
	row := func(indent string, c Change) {
		add, del := c.Stats()
		fmt.Fprintf(&sb, "%s#%-3d %-10s %s  +%d -%d", indent, c.Seq, c.Tool, textfmt.RelTo(root, c.Path), add, del)
		if c.Undone {
			sb.WriteString("  (undone)")
		}
		sb.WriteString("\n")
	}
	for i := 0; i < len(groups); {
		j := i
		for j < len(groups) && groups[j].Turn == groups[i].Turn {
			j++
		}
		turn := groups[i:j]
		fmt.Fprintf(&sb, "turn %d", turn[0].Turn)
		if len(turn) == 1 {
			if turn[0].Intent != "" {
				fmt.Fprintf(&sb, " · %s", turn[0].Intent)
			}
			sb.WriteString("\n")
			for _, c := range turn[0].Changes {
				row("  ", c)
			}
		} else {
			sb.WriteString("\n")
			for _, g := range turn {
				intent := g.Intent
				if intent == "" {
					intent = "(no stated intent)"
				}
				fmt.Fprintf(&sb, "  %s\n", intent)
				for _, c := range g.Changes {
					row("    ", c)
				}
			}
		}
		i = j
	}
	return strings.TrimRight(sb.String(), "\n")
}

// FormatChange renders one change for /changes <n>: its number, tool,
// path relative to root, intent, and hunks.
func FormatChange(c Change, root string) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "#%d  %s  %s", c.Seq, c.Tool, textfmt.RelTo(root, c.Path))
	if c.Intent != "" {
		sb.WriteString("  · " + c.Intent)
	}
	if c.Undone {
		sb.WriteString("  (undone)")
	}
	sb.WriteString("\n")
	for _, h := range c.Hunks() {
		sb.WriteString(h.String())
	}
	return strings.TrimRight(sb.String(), "\n")
}
