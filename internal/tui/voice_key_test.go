package tui

import (
	"context"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/xmonader/janetcode/internal/keymap"
)

// The voice action has no key until the config gives it one; bound, the
// key starts a recording and the same key stops it.
func TestVoiceIsABindableAction(t *testing.T) {
	if keymap.Default().First(keymap.Voice) != "" {
		t.Fatal("voice has a default key; it should start unbound")
	}
	h := harnessWithKeys(t, `{"voice": "f5"}`, false)
	h.m.opts.Voice = func(ctx context.Context, stop <-chan struct{}) (string, error) {
		<-stop
		return "spoken words", nil
	}
	h.update(tea.KeyMsg{Type: tea.KeyF5})
	if h.m.voiceStop == nil {
		t.Fatal("f5 bound to voice did not start a recording")
	}
	h.update(tea.KeyMsg{Type: tea.KeyF5})
	if h.m.voiceStop != nil {
		t.Fatal("f5 again did not stop the recording")
	}
}
