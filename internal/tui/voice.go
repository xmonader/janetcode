package tui

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/xmonader/janetcode/internal/i18n"
	"github.com/xmonader/janetcode/internal/textfmt"
)

// voiceDoneMsg carries a finished /voice recording's transcript.
type voiceDoneMsg struct {
	text string
	err  error
}

// voiceCommand is /voice: the first starts recording, the next (or
// /voice stop) stops it, and the transcript lands in the input to be
// read, edited and sent like anything typed — never sent on its own,
// since a mishearing sent unread is a request nobody made.
func (m *model) voiceCommand(arg string) tea.Cmd {
	if m.voiceStop != nil {
		close(m.voiceStop)
		m.voiceStop = nil
		m.note(i18n.T("transcribing…"))
		return nil
	}
	if arg == "stop" {
		m.note("not recording")
		return nil
	}
	if m.opts.Voice == nil {
		m.note(i18n.Tf("no recording: %s", "voice input is not set up in this session"))
		return nil
	}
	stop := make(chan struct{})
	m.voiceStop = stop
	m.note(i18n.T("recording · /voice again stops and transcribes"))
	rec, ctx := m.opts.Voice, m.life
	return func() tea.Msg {
		text, err := rec(ctx, stop)
		return voiceDoneMsg{text, err}
	}
}

// finishVoice puts the transcript in the input, after whatever is there.
func (m *model) finishVoice(msg voiceDoneMsg) {
	if m.voiceStop != nil {
		// It ended by itself (no recorder, no device) before a stop.
		m.voiceStop = nil
	}
	text := strings.TrimSpace(msg.text)
	switch {
	case msg.err != nil:
		m.note(i18n.Tf("no recording: %s", textfmt.Clip(textfmt.OneLine(msg.err.Error()), 200)))
		return
	case text == "":
		m.note(i18n.T("nothing was heard"))
		return
	}
	if cur := strings.TrimRight(m.input.Value(), " "); cur != "" {
		text = cur + " " + text
	}
	m.input.SetValue(text)
	m.input.CursorEnd()
	m.layout()
	m.note(i18n.T("transcript is in the input; enter sends it"))
}
