package present

import (
	"fmt"
	"strings"

	"github.com/xmonader/janetcode/internal/textfmt"
)

// Permissions are the facts that decide what a tool call does: the mode,
// the rules, what was allowed for this session, the protected paths and
// the sandbox. They are spread over flags, keys, files and prompts; the
// summary says in one place what they add up to.
type Permissions struct {
	AutoApprove, AutoMode, AutoEdits, ReadOnly, PlanMode bool
	// SessionAllowed are tools allowed for the rest of the session with
	// "a" at a prompt.
	SessionAllowed []string
	Allow, Deny    []string
	// Protected is how many protected path patterns are in force.
	Protected int
	// Sandbox names the sandbox commands run in, or "" when they run on
	// the machine itself.
	Sandbox string
}

// PermissionSummary answers "what will it do without asking me?" in
// plain lines: what runs, what asks, what never runs, where commands
// run, and the rules behind it. change is the front-end's own line on
// how to change it.
func PermissionSummary(p Permissions, change string) string {
	var b strings.Builder
	// One item per line under its label: the lists are short, and a
	// line never wraps in a narrow terminal.
	row := func(label string, items ...string) {
		for i, it := range items {
			if i > 0 {
				label = ""
			}
			fmt.Fprintf(&b, "  %-12s %s\n", label, it)
		}
	}

	switch {
	case p.ReadOnly:
		b.WriteString("read-only: the model can look, and has no tools that change anything\n")
	case p.PlanMode:
		b.WriteString("plan mode: the model explores and writes a plan; nothing else changes\n")
	case p.AutoApprove:
		b.WriteString("yolo: everything runs without asking, except writes to protected paths\n")
	case p.AutoMode:
		b.WriteString("auto: workspace edits and read-only commands run; the rest asks first\n")
	case p.AutoEdits:
		b.WriteString("auto-edits: file edits run; commands and the rest ask first\n")
	default:
		b.WriteString("ask: changing files, running commands and network access ask first\n")
	}

	runs := []string{"reading and searching"}
	switch {
	case p.AutoApprove:
		runs = append(runs, "everything else")
	case p.AutoMode:
		runs = append(runs, "file edits in the workspace", "read-only commands (ls, cat, git status…)")
	case p.AutoEdits:
		runs = append(runs, "file edits")
	}
	if len(p.SessionAllowed) > 0 && !p.AutoApprove {
		runs = append(runs, strings.Join(p.SessionAllowed, ", ")+", allowed for this session")
	}
	if len(p.Allow) > 0 && !p.AutoApprove {
		runs = append(runs, fmt.Sprintf("what the %s below match", textfmt.Plural(len(p.Allow), "allow rule")))
	}
	b.WriteString("\n")
	row("runs", runs...)

	if !p.ReadOnly {
		var asks []string
		switch {
		case p.AutoApprove:
		case p.AutoMode:
			asks = append(asks, "other commands", "web fetches and searches", "MCP tools that change things")
		case p.AutoEdits:
			asks = append(asks, "shell commands", "web fetches and searches", "MCP tools that change things")
		default:
			asks = append(asks, "file edits", "shell commands", "web fetches and searches", "MCP tools that change things")
		}
		if p.Protected > 0 {
			asks = append(asks, "protected paths (dotfiles, git hooks, keys), in every mode")
		}
		if len(asks) > 0 {
			row("asks first", asks...)
		}
	}
	never := []string{"commands that would wreck the machine"}
	if len(p.Deny) > 0 {
		never = append([]string{fmt.Sprintf("what the %s below match", textfmt.Plural(len(p.Deny), "deny rule"))}, never...)
	}
	row("never runs", never...)
	if p.Sandbox != "" {
		row("where", "commands run in the "+p.Sandbox+" sandbox")
	} else {
		row("where", "commands run on this machine, as you (no sandbox)")
	}

	if len(p.Allow)+len(p.Deny) > 0 {
		b.WriteString("\n")
		for _, r := range p.Deny {
			b.WriteString("  deny   " + r + "\n")
		}
		for _, r := range p.Allow {
			b.WriteString("  allow  " + r + "\n")
		}
	}
	if change != "" {
		b.WriteString("\n" + change)
	}
	return strings.TrimRight(b.String(), "\n")
}
