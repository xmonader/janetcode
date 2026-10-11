package tui

import (
	"fmt"
	"sort"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/xmonader/janetcode/internal/i18n"
	"github.com/xmonader/janetcode/internal/keymap"
)

// briefHelp is what plain /help lists: the commands a first week reaches
// for. /help all prints every key and command, grouped.
var briefHelp = []struct{ name, text string }{
	{"/connect", "pick a provider and paste a key"},
	{"/model", "switch the model"},
	{"/undo", "revert what the last turn changed"},
	{"/diff", "what changed against HEAD"},
	{"/plan", "explore and write a plan before changing anything"},
	{"/auto", "in-workspace edits and safe reads run without asking"},
	{"/edits", "file edits run without asking; commands still ask"},
	{"/compact", "summarise the conversation to free context"},
	{"/clear", "start a new session"},
	{"/resume", "reopen an earlier session"},
	{"/init", "write the project's instruction file"},
	{"/mcp", "MCP servers: list, search, add"},
	{"/doctor", "check the setup"},
	{"/quit", "exit"},
}

// briefHelpText is plain /help: the keys that matter from the first
// prompt, the everyday commands, and where the rest are.
func (m *model) briefHelpText() string {
	var sb strings.Builder
	sb.WriteString(i18n.T("keys") + "\n")
	sb.WriteString("  " + i18n.T("enter send · ctrl+j newline · esc cancel the turn · ctrl+d quit") + "\n")
	sb.WriteString("  " + i18n.T("@path attaches a file · !command runs it and shares the output") + "\n")
	sb.WriteString("  " + i18n.T("shift+tab changes what asks first") + "\n")
	sb.WriteString("  " + i18n.T("permission prompt: y allow · a this session · n deny · c cancel") + "\n")
	sb.WriteString("\n" + i18n.T("commands") + "\n")
	for _, b := range briefHelp {
		fmt.Fprintf(&sb, "  %-10s %s\n", b.name, i18n.T(b.text))
	}
	builtin := map[string]bool{}
	for _, c := range commands {
		builtin[c.name] = true
	}
	var own []string
	for _, c := range m.allCommands() {
		if !builtin[c.name] {
			own = append(own, c.name)
		}
	}
	if len(own) > 0 {
		sb.WriteString("\n" + i18n.T("yours") + "\n")
		sb.WriteString(wrapAtDots("  "+strings.Join(own, " · "), 76, "  "))
		sb.WriteString("\n")
	}
	fmt.Fprintf(&sb, "\n/help all        %s\n/help <question> %s", i18n.Tf("every key and all %d commands", len(m.allCommands())), i18n.T("what does what, e.g. /help undo a change"))
	return sb.String()
}

func (m *model) helpText() string {
	var sb strings.Builder
	sb.WriteString(i18n.T("keys") + "\n")
	sb.WriteString("  enter send · ctrl+j / alt+enter newline · up/down recall prompts, or move in an open menu · tab/enter accept a menu row\n")
	sb.WriteString("  /command names match on their letters in order (/rsm finds /resume) · tab completes the argument of " + completedCommands() + "\n")
	sb.WriteString("  esc cancel the turn (or clear the input) · esc esc on an empty prompt lists the checkpoints to rewind to · ctrl+c cancel, or when idle twice to quit · ctrl+d quit\n")
	sb.WriteString("  pgup/pgdn or mouse wheel scroll · ctrl+o expand the last turn (again folds it) · click a tool line or a thought to fold or unfold it · /verbose does the whole transcript · ctrl+l redraw\n")
	sb.WriteString("  ctrl+x writes the prompt in $EDITOR; what you save becomes the prompt\n")
	sb.WriteString("  ctrl+g moves a command running in the foreground to the background: it becomes a job and keeps running, and the turn goes on\n")
	sb.WriteString("  typing while the model works queues the message; it is sent when the turn ends · ctrl+s hands it to the running turn instead · up takes the last queued one back · esc cancels and gives them all back\n")
	sb.WriteString("  @path attaches a file, image or directory listing to the prompt · tab completes the path · ctrl+v pastes a clipboard image\n")
	sb.WriteString("  !command runs a shell command here and shares its output with the model\n")
	fmt.Fprintf(&sb, "  %s cycles ask → plan → auto → yolo · %s cycles how hard the model thinks (/effort) · %s opens the subagents overlay (/agents)\n",
		m.keys.Label(keymap.ModeCycle), m.keys.Label(keymap.Effort), m.keys.Label(keymap.Agents))
	fmt.Fprintf(&sb, "  %s searches the prompt history as you type · %s writes the prompt in $VISUAL/$EDITOR · /vim edits the prompt the vim way · /help keys lists every key and its action (the keybindings config key moves them)\n",
		m.keys.Label(keymap.HistorySearch), m.keys.Label(keymap.ExternalEditor))
	sb.WriteString(i18n.T("permission prompt: y allow once · a allow this tool for the session · r save a rule so matching calls never ask again · n deny (then type why, or enter to skip) · esc deny · c cancel the whole turn") + "\n")
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
		sb.WriteString("\n" + i18n.T(title) + "\n")
		for _, n := range names {
			c, ok := byName[n]
			if !ok {
				continue
			}
			seen[n] = true
			// A description the catalogue has (the everyday group's) is
			// shown in the interface's language; the rest stay English.
			desc := strings.ReplaceAll(descStyle.Render(i18n.T(c.help)), "\n", "\n"+strings.Repeat(" ", col))
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
	{"everyday", []string{"/help", "/connect", "/model", "/effort", "/yolo", "/auto", "/edits", "/undo", "/redo", "/compact", "/compact-fast", "/clear", "/resume", "/settle", "/quit", "/recover"}},
	{"reviewing the work", []string{"/open", "/diff", "/changes", "/tour", "/blame", "/find", "/verbose", "/export", "/usage", "/stats", "/perf", "/context", "/review", "/security-review", "/commit", "/pr", "/pr-comments", "/issue", "/checkpoints"}},
	{"steering the session", []string{"/plan", "/readonly", "/goal", "/todos", "/pin", "/unpin", "/fork", "/title", "/history", "/feedback", "/agents", "/jobs", "/cd", "/ssh", "/docker", "/sandbox", "/loop", "/add-dir", "/btw", "/vim", "/copy", "/budget", "/routing", "/theme", "/config", "/doctor", "/dryrun", "/voice"}},
	{"the project", []string{"/init", "/memory", "/remember", "/forget", "/dream", "/sleep", "/skills", "/curator", "/verify", "/accept", "/rules", "/eval", "/recipe", "/proof", "/permissions", "/hooks", "/mcp", "/cua", "/lsp", "/trust", "/egress", "/plugin"}},
	{"diagnostics", []string{"/why", "/inspect", "/trajectory", "/reload", "/hints", "/followup", "/features", "/jev"}},
}

// keysHelpText is /help keys: every prompt action with the keys it has
// in this session — the defaults with the config's keybindings applied —
// so the sheet is right for the person reading it, not for the defaults.
func (m *model) keysHelpText() string {
	var sb strings.Builder
	sb.WriteString("keys at the prompt · the keybindings config key moves any action: {\"keybindings\": {\"effort\": \"f2\"}}\n")
	for _, d := range keymap.Defs {
		fmt.Fprintf(&sb, "  %-16s %-22s %s\n", d.Action, m.keys.Label(d.Action), d.Help)
	}
	if changed := m.keys.Changed(); len(changed) > 0 {
		names := make([]string, len(changed))
		for i, a := range changed {
			names[i] = string(a)
		}
		sb.WriteString("  yours: " + strings.Join(names, ", ") + " (moved from the defaults by your config)\n")
	}
	sb.WriteString("permission prompt: y allow once · a this session · r save a rule · n deny · esc deny · c cancel the turn\n")
	sb.WriteString("reverse search (" + m.keys.Label(keymap.HistorySearch) + "): type to narrow · again for an older match · enter keeps it · esc puts back what you had\n")
	sb.WriteString("vim mode (/vim, vim_mode): esc normal · h j k l w b e 0 ^ $ gg G move · x dd dw cw ciw diw D C p u edit · i a I A o O insert")
	return sb.String()
}

// completedCommands names the commands whose argument tab completes, from
// the table itself: a hand-kept list here had fallen five behind.
func completedCommands() string {
	names := make([]string, 0, len(argCompleters))
	for n := range argCompleters {
		names = append(names, n)
	}
	sort.Strings(names)
	return strings.Join(names[:len(names)-1], ", ") + " and " + names[len(names)-1]
}
