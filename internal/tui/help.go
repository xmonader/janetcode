package tui

import (
	"fmt"
	"regexp"
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
	k := m.keys.First
	helpLine(&sb,
		keyed(k(keymap.Submit), i18n.T("%s send")),
		keyed(k(keymap.Newline), i18n.T("%s newline")),
		keyed(k(keymap.Cancel), i18n.T("%s cancel the turn")),
		keyed(k(keymap.Quit), i18n.T("%s quit")))
	sb.WriteString("  " + i18n.T("@path attaches a file · !command runs it and shares the output") + "\n")
	helpLine(&sb, keyed(k(keymap.ModeCycle), i18n.T("%s changes what asks first")))
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
	// Every key named here is the one bound in this session: with the
	// keybindings moved, the sheet used to send people to the defaults.
	// An action with no key is left out rather than called "unbound".
	k, every := m.keys.First, m.keyNames
	helpLine(&sb,
		keyed(every(keymap.Submit), "%s send"),
		keyed(every(keymap.Newline), "%s newline"),
		keyed(m.keyPair(keymap.HistoryPrev, keymap.HistoryNext), "%s recall prompts, or move in an open menu"),
		keyed(m.keyPair(keymap.Complete, keymap.Submit), "%s accept a menu row"))
	helpLine(&sb, "/command names match on their letters in order (/rsm finds /resume)",
		keyed(k(keymap.Complete), "%s completes the argument of "+completedCommands()))
	escEsc := ""
	if e := k(keymap.Cancel); e != "" {
		escEsc = e + " " + e
	}
	helpLine(&sb,
		keyed(k(keymap.Cancel), "%s cancel the turn (or clear the input)"),
		keyed(escEsc, "%s on an empty prompt lists the checkpoints to rewind to"),
		keyed(k(keymap.Interrupt), "%s cancel, or when idle twice to quit"),
		keyed(k(keymap.Quit), "%s quit"))
	scroll := "mouse wheel scroll"
	if p := m.keyPair(keymap.PageUp, keymap.PageDown); p != "" {
		scroll = p + " or " + scroll
	}
	helpLine(&sb, scroll,
		keyed(k(keymap.Expand), "%s expand the last turn (again folds it)"),
		"click a tool line or a thought to fold or unfold it",
		"/verbose does the whole transcript",
		keyed(k(keymap.Redraw), "%s redraw"))
	helpLine(&sb, keyed(k(keymap.Background), "%s moves a command running in the foreground to the background: it becomes a job and keeps running, and the turn goes on"))
	helpLine(&sb, "typing while the model works queues the message; it is sent when the turn ends",
		keyed(k(keymap.Steer), "%s hands it to the running turn instead"),
		keyed(k(keymap.HistoryPrev), "%s takes the last queued one back"),
		keyed(k(keymap.Cancel), "%s cancels and gives them all back"))
	helpLine(&sb, "@path attaches a file, image or directory listing to the prompt",
		keyed(k(keymap.Complete), "%s completes the path"),
		keyed(k(keymap.PasteImage), "%s pastes a clipboard image"))
	sb.WriteString("  !command runs a shell command here and shares its output with the model\n")
	helpLine(&sb,
		keyed(every(keymap.ModeCycle), "%s cycles ask → plan → auto → yolo"),
		keyed(every(keymap.Effort), "%s cycles how hard the model thinks (/effort)"),
		keyed(every(keymap.Agents), "%s opens the subagents overlay (/agents)"))
	helpLine(&sb,
		keyed(every(keymap.HistorySearch), "%s searches the prompt history as you type"),
		keyed(every(keymap.ExternalEditor), "%s writes the prompt in $VISUAL/$EDITOR; what you save becomes the prompt"),
		"/vim edits the prompt the vim way",
		"/help keys lists every key and its action (the keybindings config key moves them)")
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
			desc := strings.ReplaceAll(descStyle.Render(m.boundKeysIn(c.name, i18n.T(c.help)+i18n.T(c.keyHelp))), "\n", "\n"+strings.Repeat(" ", col))
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

// keyNames is every key bound to an action, "" when there is none.
func (m *model) keyNames(a keymap.Action) string {
	return strings.Join(m.keys.Keys(a), " / ")
}

// keyPair names the first keys of two actions as "up/down", or the one
// that is bound, or "" when neither is.
func (m *model) keyPair(a, b keymap.Action) string {
	ka, kb := m.keys.First(a), m.keys.First(b)
	switch {
	case ka != "" && kb != "":
		return ka + "/" + kb
	case ka != "":
		return ka
	}
	return kb
}

// keyed fills a "%s does this" phrase with a key, or is "" when the key
// is: a phrase about an action with no key would send the reader looking
// for one.
func keyed(key, phrase string) string {
	if key == "" {
		return ""
	}
	return fmt.Sprintf(phrase, key)
}

// joinKept joins the parts that are not empty: status lines built from
// keyed phrases drop the clause of an unbound action.
func joinKept(sep string, parts ...string) string {
	var kept []string
	for _, p := range parts {
		if p != "" {
			kept = append(kept, p)
		}
	}
	return strings.Join(kept, sep)
}

// helpLine writes one indented line of the parts that are left, joined by
// dots, and nothing when none are.
func helpLine(sb *strings.Builder, parts ...string) {
	var kept []string
	for _, p := range parts {
		if p != "" {
			kept = append(kept, p)
		}
	}
	if len(kept) > 0 {
		sb.WriteString("  " + strings.Join(kept, " · ") + "\n")
	}
}

// helpKeys are the default keys the command table's descriptions name,
// which /help rewrites to the keys this session has. drop is the English
// clause that goes when the action has no key at all; a translation
// without it says "unbound" in the key's place instead.
var helpKeys = []struct {
	cmd, key string
	action   keymap.Action
	drop     string
}{
	{"/effort", "alt+e", keymap.Effort, "; alt+e cycles it (the keybindings config key can move it)"},
	{"/verbose", "ctrl+o", keymap.Expand, " (ctrl+o expands one turn)"},
	{"/checkpoints", "esc esc", keymap.Cancel, " (esc esc opens it too)"},
	{"/followup", "tab", keymap.Complete, "; tab accepts"},
}

// boundKeysIn rewrites the default key a command's description names to
// the key bound to that action.
func (m *model) boundKeysIn(cmd, desc string) string {
	for _, hk := range helpKeys {
		if hk.cmd != cmd {
			continue
		}
		bound := m.keys.First(hk.action)
		if bound == "" {
			if strings.Contains(desc, hk.drop) {
				desc = strings.Replace(desc, hk.drop, "", 1)
				continue
			}
			bound = "unbound"
		} else if hk.key == "esc esc" {
			bound += " " + bound
		}
		re := regexp.MustCompile(`(^|[\s(])` + regexp.QuoteMeta(hk.key) + `($|[\s),.;:])`)
		desc = re.ReplaceAllString(desc, "${1}"+strings.ReplaceAll(bound, "$", "$$")+"${2}")
	}
	return desc
}
