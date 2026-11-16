package tui

import (
	"context"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/xmonader/janetcode/internal/agent"
)

func askSecretAsync(h *harness) chan agent.SecretReply {
	out := make(chan agent.SecretReply, 1)
	sa := h.m.opts.Agent.Asker.(agent.SecretAsker)
	go func() {
		r, _ := sa.AskSecret(context.Background(), agent.SecretRequest{
			Name: "deploy", EnvVar: "JANETCODE_SECRET_DEPLOY", Purpose: "push the release", Usage: "curl with a bearer header",
		})
		out <- r
	}()
	h.pump(func() bool { return h.m.secreting != nil })
	return out
}

// The typed value is masked on every frame, never reaches the composer,
// and arrives intact (including a pasted tail) at the asker.
func TestSecretPromptMasksAndDeliversTheValue(t *testing.T) {
	h := newHarness(t)
	h.m.input.SetValue("half-typed message")
	out := askSecretAsync(h)
	if h.m.mode() != modeSecret {
		t.Fatalf("mode = %s", h.m.mode())
	}
	v := plain(h.m.View())
	for _, want := range []string{"deploy", "push the release", "curl with a bearer header", "JANETCODE_SECRET_DEPLOY"} {
		if !strings.Contains(v, want) {
			t.Errorf("prompt lacks %q:\n%s", want, v)
		}
	}
	h.keys("hunter2")
	h.update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("-xyz"), Paste: true})
	v = plain(h.m.View())
	if strings.Contains(v, "hunter2") || strings.Contains(v, "xyz") {
		t.Fatalf("the value is on screen:\n%s", v)
	}
	if !strings.Contains(v, "•••••••••••") {
		t.Fatalf("no mask shown:\n%s", v)
	}
	if got := h.m.input.Value(); got != "" {
		t.Fatalf("the composer holds %q while the prompt is up", got)
	}
	h.enter()
	r := <-out
	if r.Declined || r.Value != "hunter2-xyz" {
		t.Fatalf("reply = %+v", r)
	}
	if h.m.secreting != nil || h.m.mode() != modePrompt {
		t.Fatal("the prompt did not close")
	}
	if got := h.m.input.Value(); got != "half-typed message" {
		t.Fatalf("draft = %q", got)
	}
	if strings.Contains(plain(h.m.View()), "hunter2") {
		t.Fatal("the value shows after the prompt closed")
	}
	for _, e := range h.m.hist.entries {
		if strings.Contains(e.Text, "hunter2") {
			t.Fatalf("history holds the value: %q", e.Text)
		}
	}
}

func TestSecretPromptEscAndEmptyDecline(t *testing.T) {
	for name, act := range map[string]func(h *harness){
		"esc":   func(h *harness) { h.keys("abcdef"); h.update(tea.KeyMsg{Type: tea.KeyEsc}) },
		"empty": func(h *harness) { h.enter() },
	} {
		h := newHarness(t)
		out := askSecretAsync(h)
		act(h)
		if r := <-out; !r.Declined || r.Value != "" {
			t.Errorf("%s: reply = %+v", name, r)
		}
		if h.m.secreting != nil {
			t.Errorf("%s: prompt still up", name)
		}
	}
}

func TestSecretPromptWithoutUIFails(t *testing.T) {
	if _, err := (&tuiAsker{}).AskSecret(context.Background(), agent.SecretRequest{}); err == nil {
		t.Fatal("no UI must be an error, not a hang")
	}
}

func TestSecretPromptEndsWithItsContext(t *testing.T) {
	h := newHarness(t)
	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() {
		_, err := h.m.asker.AskSecret(ctx, agent.SecretRequest{Name: "x", EnvVar: "JANETCODE_SECRET_X"})
		errc <- err
	}()
	h.pump(func() bool { return h.m.secreting != nil })
	cancel()
	if err := <-errc; err == nil {
		t.Fatal("cancelled ask returned no error")
	}
}
