package tui

import (
	"errors"
	"os"
	"os/exec"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// editorDoneMsg carries the prompt back from $EDITOR.
type editorDoneMsg struct {
	text string
	err  error
}

// editorCommand is $VISUAL, else $EDITOR, else vi, split into words so
// "code --wait" works.
func editorCommand(getenv func(string) string) []string {
	for _, v := range []string{"VISUAL", "EDITOR"} {
		if f := strings.Fields(getenv(v)); len(f) > 0 {
			return f
		}
	}
	return []string{"vi"}
}

// keyEditor is ctrl+x: write the prompt being typed to a file, hand the
// terminal to the user's editor, and take what they saved back as the
// prompt. A long, multi-paragraph prompt is easier to write there.
func (m *model) keyEditor(tea.KeyMsg) (tea.Cmd, bool) {
	f, err := os.CreateTemp("", "janetcode-prompt-*.md")
	if err != nil {
		m.transcript.errorf("editor: %v", err)
		m.refresh(false)
		return nil, true
	}
	path := f.Name()
	_, werr := f.WriteString(m.input.Value())
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		os.Remove(path)
		m.transcript.errorf("editor: %v", werr)
		m.refresh(false)
		return nil, true
	}
	argv := append(editorCommand(os.Getenv), path)
	cmd := exec.Command(argv[0], argv[1:]...)
	m.noteUse("ctrl+x")
	return tea.ExecProcess(cmd, func(err error) tea.Msg {
		defer os.Remove(path)
		if err != nil {
			return editorDoneMsg{err: err}
		}
		data, rerr := os.ReadFile(path)
		return editorDoneMsg{text: string(data), err: rerr}
	}), true
}

// finishEditor puts what the editor saved into the composer. An editor
// that exits non-zero (vim's :cq) leaves the prompt as it was.
func (m *model) finishEditor(msg editorDoneMsg) {
	var ee *exec.ExitError
	switch {
	case errors.As(msg.err, &ee):
		m.status = "editor exited " + ee.ProcessState.String() + " · prompt unchanged"
	case msg.err != nil:
		m.transcript.errorf("editor: %v (set $EDITOR)", msg.err)
	default:
		m.input.SetValue(strings.TrimRight(msg.text, "\n"))
		m.input.CursorEnd()
	}
	m.layout()
	m.refresh(false)
}
