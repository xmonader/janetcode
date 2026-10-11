package tui

import (
	"os"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/xmonader/janetcode/internal/agent"
	"github.com/xmonader/janetcode/internal/config"
	"github.com/xmonader/janetcode/internal/jev"
	"github.com/xmonader/janetcode/internal/llm"
)

func jevHarness(t *testing.T) (*harness, *config.Config, string) {
	t.Helper()
	// A key the test owns, so nothing depends on the machine's own.
	t.Setenv("OPENROUTER_API_KEY", "test-key")
	h := newHarness(t)
	cfg := &config.Config{Provider: "fake", Model: "main-model", Providers: map[string]config.Provider{
		"fake": {Type: "openai", BaseURL: "http://fake", APIKey: "k0"},
	}}
	path := h.withConfig(t, cfg, func(c *config.Config) (llm.Provider, error) { return &listing{}, nil })
	return h, cfg, path
}

// /jev on writes a block the next load will accept, and says what is
// still missing rather than pretending the feature now does something.
func TestJevOnWritesALoadableBlock(t *testing.T) {
	h, cfg, path := jevHarness(t)
	h.keys("/jev on")
	h.enter()
	if cfg.Decisions == nil || !cfg.Decisions.On() {
		t.Fatalf("decisions = %+v", cfg.Decisions)
	}
	if cfg.Decisions.Model != jev.DefaultModel {
		t.Fatalf("model = %q, want the default slug", cfg.Decisions.Model)
	}
	if err := cfg.Decisions.Validate(); err != nil {
		t.Fatalf("/jev on wrote a block that will not load: %v", err)
	}
	if !strings.Contains(h.m.status, "no consumer is on") {
		t.Fatalf("status = %q", h.m.status)
	}
	raw, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(raw), `"decisions"`) || !strings.Contains(string(raw), jev.DefaultModel) {
		t.Fatalf("saved config: %s %v", raw, err)
	}

	// A consumer on, and the live agent has a client for it.
	h.keys("/jev triage on")
	h.enter()
	if !h.m.opts.Agent.DecisionsOn(agent.DecisionTriage) || !h.m.opts.Agent.DecisionsClient() {
		t.Fatal("triage did not reach the running session")
	}
	if !strings.Contains(h.m.status, "triage") {
		t.Fatalf("status = %q", h.m.status)
	}
}

// /jev off leaves the session with no client at all -- the point being
// that a consumer left on in the file asks nothing.
func TestJevOffStopsEveryConsumer(t *testing.T) {
	h, cfg, _ := jevHarness(t)
	h.keys("/jev triage on")
	h.enter()
	h.keys("/jev intake on")
	h.enter()
	if !h.m.opts.Agent.DecisionsClient() {
		t.Fatal("no client after two consumers were turned on")
	}
	h.keys("/jev off")
	h.enter()
	for _, c := range []agent.DecisionConsumer{agent.DecisionTriage, agent.DecisionIntake, agent.DecisionRoute, agent.DecisionOutcome} {
		if h.m.opts.Agent.DecisionsOn(c) {
			t.Fatalf("%s survived /jev off", c)
		}
	}
	if h.m.opts.Agent.DecisionsClient() {
		t.Fatal("a client survived /jev off")
	}
	// The file keeps what was on, so /jev on restores it.
	if !cfg.Decisions.Triage || !cfg.Decisions.Intake {
		t.Fatal("/jev off forgot the consumer keys")
	}
	h.keys("/jev on")
	h.enter()
	if !h.m.opts.Agent.DecisionsOn(agent.DecisionTriage) {
		t.Fatal("/jev on did not bring triage back")
	}
}

// The kill switch is not a suggestion: with it set, no /jev command puts
// a client on the agent, and the status says why.
func TestJevRespectsTheKillSwitch(t *testing.T) {
	h, _, _ := jevHarness(t)
	h.m.opts.Agent.Off = map[string]bool{"decisions": true}
	h.keys("/jev triage on")
	h.enter()
	if h.m.opts.Agent.DecisionsClient() || h.m.opts.Agent.DecisionsOn(agent.DecisionTriage) {
		t.Fatal("JANETCODE_OFF=decisions was overridden")
	}
	if !strings.Contains(h.m.jevStatusLine(), "JANETCODE_OFF=decisions") {
		t.Fatalf("status = %q", h.m.jevStatusLine())
	}
}

// The model list offers the verified slug and takes a typed id, judged
// by the rule the config loader applies -- an alias is refused here
// rather than written to a file that then refuses to load.
func TestJevModelPickerOffersTheSlugAndRefusesAliases(t *testing.T) {
	h, cfg, _ := jevHarness(t)
	h.keys("/jev model")
	h.updateCmd(tea.KeyMsg{Type: tea.KeyEnter})
	if h.m.picker == nil {
		t.Fatal("no picker")
	}
	if v := plain(h.m.View()); !strings.Contains(v, "Decision model") || !strings.Contains(v, jev.DefaultModel) {
		t.Fatalf("view:\n%s", v)
	}
	h.enter()
	if h.m.picker != nil {
		t.Fatal("picker stayed open")
	}
	if cfg.Decisions == nil || cfg.Decisions.Model != jev.DefaultModel {
		t.Fatalf("model = %+v", cfg.Decisions)
	}
	// A typed alias is refused, and the configured model is untouched.
	h.keys("/jev model jev-latest")
	h.enter()
	if cfg.Decisions.Model != jev.DefaultModel {
		t.Fatalf("an alias was accepted: %q", cfg.Decisions.Model)
	}
	if !strings.Contains(plain(h.m.View()), "not a pinned version") {
		t.Fatalf("view:\n%s", plain(h.m.View()))
	}
	// A pinned version is taken.
	h.keys("/jev model jev-1.13.0")
	h.enter()
	if cfg.Decisions.Model != "jev-1.13.0" {
		t.Fatalf("model = %q", cfg.Decisions.Model)
	}
}

// The provider list is a list, not one hardcoded endpoint: whatever
// config.DecisionEndpoints holds is what the picker offers.
func TestJevProviderPickerOffersTheEndpointList(t *testing.T) {
	h, cfg, _ := jevHarness(t)
	h.keys("/jev provider")
	h.updateCmd(tea.KeyMsg{Type: tea.KeyEnter})
	if h.m.picker == nil {
		t.Fatal("no picker")
	}
	v := plain(h.m.View())
	for _, e := range config.DecisionEndpoints {
		if !strings.Contains(v, e.Name) || !strings.Contains(v, e.Label) {
			t.Fatalf("endpoint %s missing from:\n%s", e.Name, v)
		}
	}
	h.enter()
	if cfg.Decisions == nil || cfg.Decisions.EndpointName() != config.DecisionEndpoints[0].Name {
		t.Fatalf("endpoint = %+v", cfg.Decisions)
	}
}

// /features lists the slots and the switches, and changes one.
func TestFeaturesListsAndToggles(t *testing.T) {
	h, cfg, path := jevHarness(t)
	// Tall enough for the whole list: a switch added later must not
	// push the slots off the top of the view.
	h.update(tea.WindowSizeMsg{Width: 100, Height: 60})
	h.keys("/features")
	h.enter()
	v := plain(h.m.View())
	for _, want := range []string{"model slots", "cheap: unset", "escalate", "advisor", "stale-nudge", "triage"} {
		if !strings.Contains(v, want) {
			t.Fatalf("%q missing from:\n%s", want, v)
		}
	}
	h.keys("/features escalate on")
	h.enter()
	if cfg.EscalateAfter != 1 || h.m.opts.Agent.EscalateRounds() != 1 {
		t.Fatalf("escalate_after = %d, agent = %d", cfg.EscalateAfter, h.m.opts.Agent.EscalateRounds())
	}
	if !strings.Contains(h.m.status, "the strong slot is the session's own model") {
		t.Fatalf("status = %q", h.m.status)
	}
	// escalate_after is the count as well as the switch, so off writes
	// zero and on comes back at one round rather than at whatever it
	// was; /config escalate_after=3 is how a different count is set.
	cfg.EscalateAfter = 3
	h.keys("/features escalate off")
	h.enter()
	h.keys("/features escalate on")
	h.enter()
	if cfg.EscalateAfter != 1 {
		t.Fatalf("escalate_after = %d, want one round after off and on", cfg.EscalateAfter)
	}
	h.keys("/features stale-nudge on")
	h.enter()
	if !cfg.StaleCheckNudge || !h.m.opts.Agent.StaleNudgeOn() {
		t.Fatal("stale-nudge did not take")
	}
	raw, _ := os.ReadFile(path)
	if !strings.Contains(string(raw), `"stale_check_nudge": true`) {
		t.Fatalf("saved config: %s", raw)
	}
	// An unknown name says what the names are.
	h.keys("/features wizard on")
	h.enter()
	if !strings.Contains(plain(h.m.View()), "no feature") {
		t.Fatalf("view:\n%s", plain(h.m.View()))
	}
}

// Tab completion offers both commands' words, with their current state.
func TestJevAndFeaturesComplete(t *testing.T) {
	h, _, _ := jevHarness(t)
	h.keys("/jev tri")
	got := h.m.completions()
	if len(got) != 1 || got[0].name != "/jev triage" {
		t.Fatalf("completions = %+v", got)
	}
	h.inputReset()
	h.keys("/features esc")
	got = h.m.completions()
	if len(got) != 1 || got[0].name != "/features escalate" || !strings.HasPrefix(got[0].help, "off") {
		t.Fatalf("completions = %+v", got)
	}
}
