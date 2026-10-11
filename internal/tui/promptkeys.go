package tui

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/xmonader/janetcode/internal/keymap"
)

// singleKeys drops the chords from a list of keys: the textarea's own
// bindings match one key press at a time.
func singleKeys(keys []string) []string {
	var out []string
	for _, k := range keys {
		if !strings.Contains(k, " ") {
			out = append(out, k)
		}
	}
	return out
}

// histSearch is the reverse incremental search over the prompt history
// (ctrl+r), as a shell has it: each typed character narrows the search,
// ctrl+r again finds the next older match, enter keeps the match in the
// composer to edit or send, and esc puts back what was being typed. The
// history is the per-directory file, so prompts from earlier sessions in
// this project are found as well as this session's.
type histSearch struct {
	query string
	idx   int    // the matching entry, len(entries) when there is none yet
	draft string // the composer before the search opened
	fail  bool   // the last narrowing found nothing
}

// keyHistorySearch opens the search, or steps to an older match while it
// is open (handleSearchKey routes the second press here too).
func (m *model) keyHistorySearch(tea.KeyMsg) (tea.Cmd, bool) {
	m.noteUse("ctrl+r")
	if len(m.hist.entries) == 0 {
		m.status = "no prompt history in this directory yet"
		m.refresh(false)
		return nil, true
	}
	m.hsearch = &histSearch{idx: len(m.hist.entries), draft: m.input.Value()}
	m.refresh(false)
	return nil, true
}

// searchFrom finds the newest entry older than from that contains the
// query, skipping ones that read the same as the current match.
func (m *model) searchFrom(from int) {
	s := m.hsearch
	q := strings.ToLower(s.query)
	current := ""
	if s.idx < len(m.hist.entries) {
		current = m.hist.recall(s.idx)
	}
	for i := min(from, len(m.hist.entries)) - 1; i >= 0; i-- {
		text := m.hist.recall(i)
		if text == current && from <= s.idx {
			continue
		}
		if strings.Contains(strings.ToLower(text), q) {
			s.idx, s.fail = i, false
			m.setInput(text)
			return
		}
	}
	s.fail = true
}

// handleSearchKey reads the keyboard while the search is open.
func (m *model) handleSearchKey(msg tea.KeyMsg) (tea.Cmd, bool) {
	s := m.hsearch
	name := keyName(msg)
	a, bound := m.keys.Lookup(name)
	switch {
	case bound && a == keymap.HistorySearch:
		m.searchFrom(s.idx)
	case msg.Type == tea.KeyRunes && !msg.Alt, msg.Type == tea.KeySpace:
		if len(msg.Runes) == 0 {
			s.query += " "
		}
		s.query += string(msg.Runes)
		// Narrowing keeps the current match while it still matches.
		m.searchFrom(s.idx + 1)
	case name == "backspace":
		if r := []rune(s.query); len(r) > 0 {
			s.query = string(r[:len(r)-1])
		}
		if s.query == "" {
			s.idx, s.fail = len(m.hist.entries), false
			m.setInput(s.draft)
		} else {
			m.searchFrom(len(m.hist.entries) + 1)
		}
	case bound && a == keymap.Submit:
		m.hsearch = nil
		m.status = "found in the history · enter sends it, or edit it first"
	case name == "esc", name == "ctrl+g", bound && (a == keymap.Cancel || a == keymap.Interrupt):
		m.hsearch = nil
		m.setInput(s.draft)
		m.status = ""
	default:
		// Any other key keeps the match and does what it does: an arrow
		// moves in the recalled text, as it does in a shell.
		m.hsearch = nil
		m.refresh(false)
		return nil, false
	}
	m.refresh(false)
	return nil, true
}

// searchStatus is the status line while the search is open.
func (m *model) searchStatus() string {
	s := m.hsearch
	label := "reverse-i-search"
	if s.fail {
		label = "failing " + label
	}
	more := " · " + m.keys.Label(keymap.HistorySearch) + " older · enter keep · esc back"
	return fmt.Sprintf("(%s)`%s'%s", label, s.query, more)
}

// editorDoneMsg is the external editor having exited.
type editorDoneMsg struct {
	path string
	err  error
}

// editorCommand is the user's editor: $VISUAL, then $EDITOR, then vi (or
// notepad on Windows). It may carry arguments ("code --wait").
func editorCommand(getenv func(string) string) []string {
	for _, v := range []string{"VISUAL", "EDITOR"} {
		if f := strings.Fields(getenv(v)); len(f) > 0 {
			return f
		}
	}
	if runtime.GOOS == "windows" {
		return []string{"notepad"}
	}
	return []string{"vi"}
}

// keyExternalEditor hands the prompt to $VISUAL/$EDITOR (ctrl+x ctrl+e,
// as in bash): a long prompt is easier to write in an editor than in a
// six-line box. What the editor saves comes back into the composer to be
// read over and sent, not sent on its own.
func (m *model) keyExternalEditor(tea.KeyMsg) (tea.Cmd, bool) {
	m.noteUse("ctrl+x ctrl+e")
	f, err := os.CreateTemp("", "janetcode-prompt-*.md")
	if err != nil {
		m.status = "external editor: " + err.Error()
		m.refresh(false)
		return nil, true
	}
	_, werr := f.WriteString(m.input.Value())
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		os.Remove(f.Name())
		m.status = "external editor: " + werr.Error()
		m.refresh(false)
		return nil, true
	}
	argv := append(editorCommand(os.Getenv), f.Name())
	c := exec.Command(argv[0], argv[1:]...)
	path := f.Name()
	run := m.runEditor
	if run == nil {
		run = tea.ExecProcess
	}
	return run(c, func(err error) tea.Msg { return editorDoneMsg{path: path, err: err} }), true
}

// editorDone takes the edited text back into the composer.
func (m *model) editorDone(msg editorDoneMsg) {
	defer os.Remove(msg.path)
	if msg.err != nil {
		m.status = "external editor: " + msg.err.Error() + " · the prompt is unchanged"
		m.refresh(false)
		return
	}
	data, err := os.ReadFile(msg.path)
	if err != nil {
		m.status = "external editor: " + err.Error()
		m.refresh(false)
		return
	}
	// Editors end a file with a newline the prompt never had. One that
	// saves CRLF would come back double-spaced: the textarea makes \r and
	// \n each a line break.
	text := strings.ReplaceAll(strings.TrimRight(string(data), "\r\n"), "\r\n", "\n")
	m.setInput(text)
	m.status = "edited in " + editorCommand(os.Getenv)[0] + " · enter sends it"
	m.refresh(false)
}
