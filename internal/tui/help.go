package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

func (m *model) helpText() string {
	var sb strings.Builder
	sb.WriteString("keys\n")
	sb.WriteString("  enter send · ctrl+j / alt+enter newline · up/down recall prompts, or move in an open menu · tab/enter accept a menu row\n")
	sb.WriteString("  /command names match on their letters in order (/rsm finds /resume) · tab completes the argument of /resume, /model, /forget, /undo, /open and /diff\n")
	sb.WriteString("  esc cancel the turn (or clear the input) · ctrl+c cancel, or when idle twice to quit · ctrl+d quit\n")
	sb.WriteString("  pgup/pgdn or mouse wheel scroll · ctrl+o expand the last turn (again folds it) · click a tool line or a thought to fold or unfold it · /verbose does the whole transcript · ctrl+l redraw\n")
	sb.WriteString("  ctrl+g moves a command running in the foreground to the background: it becomes a job and keeps running, and the turn goes on\n")
	sb.WriteString("  typing while the model works queues the message; it is sent when the turn ends · ctrl+s hands it to the running turn instead · up takes the last queued one back · esc cancels and gives them all back\n")
	sb.WriteString("  @path attaches a file, image or directory listing to the prompt · tab completes the path · ctrl+v pastes a clipboard image\n")
	sb.WriteString("  !command runs a shell command here and shares its output with the model\n")
	sb.WriteString("  shift+tab cycles ask → plan → auto → yolo · ctrl+r cycles how hard the model thinks (/effort) · ctrl+t opens the subagents overlay (/agents)\n")
	sb.WriteString("permission prompt: y allow once · a allow this tool for the session · r save a rule so matching calls never ask again · n deny (then type why, or enter to skip) · esc deny · c cancel the whole turn\n")
	all := m.allCommands()
	byName := map[string]command{}
	for _, c := range all {
		byName[c.name] = c
	}
	width := m.width
	if width < 40 {
		width = 80
	}
	const col = 22
	descStyle := lipgloss.NewStyle().Width(width - col - 2)
	seen := map[string]bool{}
	// A heading printed hard against the previous group's last command
	// read as that command's second line: "/lsp  language servers and
	// the diagnostics they reported" followed by "diagnostics".
	writeGroup := func(title string, names []string) {
		sb.WriteString("\n" + title + "\n")
		for _, n := range names {
			c, ok := byName[n]
			if !ok {
				continue
			}
			seen[n] = true
			desc := strings.ReplaceAll(descStyle.Render(c.help), "\n", "\n"+strings.Repeat(" ", col))
			if len(c.name) > 10 || len(c.args) > 8 {
				// Too wide for the columns: the description starts on
				// its own line rather than wrapping from wherever the
				// arguments ended.
				fmt.Fprintf(&sb, "  %s %s\n%s%s\n", c.name, c.args, strings.Repeat(" ", col), desc)
				continue
			}
			fmt.Fprintf(&sb, "  %-10s %-8s %s\n", c.name, c.args, desc)
		}
	}
	for _, g := range helpGroups {
		writeGroup(g.title, g.names)
	}
	var rest []string
	for _, c := range all {
		if !seen[c.name] {
			rest = append(rest, c.name)
		}
	}
	if len(rest) > 0 {
		writeGroup("custom commands and skills", rest)
	}
	sb.WriteString("\ncustom commands: drop prompt templates in .janetcode/commands/<name>.md (or the config dir); $ARGUMENTS is replaced")
	return sb.String()
}

// helpGroups orders /help by how often a command is reached for; the
// table itself stays in the order the code grew.
var helpGroups = []struct {
	title string
	names []string
}{
	{"everyday", []string{"/help", "/connect", "/model", "/yolo", "/auto", "/edits", "/undo", "/redo", "/compact", "/compact-fast", "/clear", "/resume", "/settle", "/quit"}},
	{"reviewing the work", []string{"/open", "/diff", "/changes", "/blame", "/find", "/verbose", "/export", "/usage", "/stats", "/context", "/review"}},
	{"steering the session", []string{"/plan", "/readonly", "/goal", "/todos", "/pin", "/unpin", "/fork", "/title", "/history", "/feedback", "/agents", "/jobs", "/cd", "/ssh", "/docker", "/sandbox", "/loop", "/add-dir", "/btw", "/copy", "/config", "/doctor"}},
	{"the project", []string{"/init", "/memory", "/remember", "/forget", "/dream", "/sleep", "/skills", "/curator", "/verify", "/permissions", "/hooks", "/mcp", "/cua", "/lsp"}},
	{"diagnostics", []string{"/why", "/inspect", "/trajectory", "/reload", "/hints", "/followup"}},
}
