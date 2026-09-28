package tui

import (
	"errors"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/xmonader/janetcode/internal/config"
	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/recipe"
)

// TestConnectEscFromAPresetURLGoesBackToTheList: a preset goes from the
// list straight to its URL, but esc stepped back to stepName, a step it
// never showed, where typing a word renamed the provider being set up.
func TestConnectEscFromAPresetURLGoesBackToTheList(t *testing.T) {
	h := newHarness(t)
	cfg := &config.Config{Provider: "fake", Model: "m", Providers: map[string]config.Provider{
		"fake": {Type: "openai", BaseURL: "http://fake", APIKey: "k0"},
	}}
	h.withConfig(t, cfg, func(c *config.Config) (llm.Provider, error) { return &listing{}, nil })
	h.keys("/connect")
	h.enter()
	h.keys("openrouter")
	h.enter()
	if h.m.connect.step != stepURL {
		t.Fatalf("step = %d, want the URL", h.m.connect.step)
	}
	h.update(tea.KeyMsg{Type: tea.KeyEsc})
	if h.m.connect == nil || h.m.connect.step != stepPick {
		t.Fatalf("esc from a preset's URL went to step %d, want the provider list", h.m.connect.step)
	}
}

// TestTrajectoryPageUpStaysInTheList: pgup wrapped by modulo, and a step
// longer than the list from near its top went negative, which enter then
// used as an index.
func TestTrajectoryPageUpStaysInTheList(t *testing.T) {
	h := newHarness(t)
	for _, c := range []struct {
		idx, n int
		key    string
		want   int
	}{
		{1, 4, "pgup", 0},
		{0, 4, "pgup", 0},
		{2, 4, "pgdown", 3},
		{0, 4, "up", 3},
		{3, 4, "down", 0},
	} {
		h.m.ov.idx = c.idx
		h.m.trajListMove(c.key, c.n)
		if h.m.ov.idx != c.want {
			t.Errorf("%s from %d of %d landed on %d, want %d", c.key, c.idx, c.n, h.m.ov.idx, c.want)
		}
	}
}

// TestQueuedPromptSentAfterVerifyAndRecipe: a prompt typed while /verify
// or /recipe run ran is queued "until this turn ends", but neither's end
// sent the queue: the prompt sat there until some later turn finished.
func TestQueuedPromptSentAfterVerifyAndRecipe(t *testing.T) {
	for name, done := range map[string]tea.Msg{
		"verify": verifyDoneMsg{},
		"recipe": recipeDoneMsg{recipe: recipe.Recipe{Name: "r"}},
	} {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t, &llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "ok"), StopReason: llm.StopEndTurn})
			h.m.startTurn("working")
			h.keys("and then this")
			h.enter()
			if len(h.m.turn.queued) != 1 {
				t.Fatalf("queued = %q", h.m.turn.queued)
			}
			h.update(done)
			if len(h.m.turn.queued) != 0 || !h.m.turn.running {
				t.Fatalf("after %s the queue is %q and running=%v; want it sent", name, h.m.turn.queued, h.m.turn.running)
			}
			h.pump(func() bool { return !h.m.turn.running })
		})
	}
}

// TestConnectIgnoresAModelListFromAnEarlierVisit: the model step's list
// was matched by dialog only, so a list still in flight from an earlier
// visit to the step (a key since replaced) landed in the new one.
func TestConnectIgnoresAModelListFromAnEarlierVisit(t *testing.T) {
	h := newHarness(t)
	cfg := &config.Config{Provider: "fake", Model: "m", Providers: map[string]config.Provider{
		"fake": {Type: "openai", BaseURL: "http://fake", APIKey: "k0"},
	}}
	h.withConfig(t, cfg, func(c *config.Config) (llm.Provider, error) {
		return &listing{err: errors.New("401 unauthorized")}, nil
	})
	h.m.openConnect(stepModel)
	stale := h.m.fetchModels()
	h.m.enterStep(stepModel) // the step visited again, with its own list on the way
	h.update(stale())
	if !h.m.connect.loading || h.m.connect.step != stepModel {
		t.Fatalf("a stale list landed: loading=%v step=%d", h.m.connect.loading, h.m.connect.step)
	}
}
