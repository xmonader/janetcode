package tui

import (
	"os"
	"slices"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/xmonader/janetcode/internal/config"
	"github.com/xmonader/janetcode/internal/llm"
)

func slotHarness(t *testing.T) (*harness, *config.Config, string) {
	t.Helper()
	h := newHarness(t)
	cfg := &config.Config{Provider: "fake", Model: "main-model", Providers: map[string]config.Provider{
		"fake": {Type: "openai", BaseURL: "http://fake", APIKey: "k0"},
	}}
	lister := &listing{models: []llm.ModelInfo{{ID: "cheap/one"}, {ID: "strong/two"}}}
	path := h.withConfig(t, cfg, func(c *config.Config) (llm.Provider, error) { return lister, nil })
	return h, cfg, path
}

// /model <slot> <name> writes the slot, leaves the session's own model
// alone, and persists under the key the config file already uses.
func TestModelSlotSetsTheKeyAndNotTheSession(t *testing.T) {
	h, cfg, path := slotHarness(t)
	h.keys("/model strong qwen/qwen3.7-plus")
	h.enter()
	if cfg.StrongModel != "qwen/qwen3.7-plus" {
		t.Fatalf("strong slot = %q", cfg.StrongModel)
	}
	was := h.m.opts.Agent.Settings().Model
	if cfg.Model != "main-model" || was != "m" {
		t.Fatalf("the session's model moved: cfg=%q agent=%q", cfg.Model, was)
	}
	if got := h.m.opts.Agent.StrongModelName(); got != "qwen/qwen3.7-plus" {
		t.Fatalf("the live agent has strong = %q", got)
	}
	raw, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(raw), `"strong_model": "qwen/qwen3.7-plus"`) {
		t.Fatalf("saved config: %s %v", raw, err)
	}
	// The status says what the slot does not do on its own.
	if !strings.Contains(h.m.status, "the cascade is off") {
		t.Fatalf("status = %q", h.m.status)
	}

	// And off gives it back to the session's model.
	h.keys("/model strong off")
	h.enter()
	if cfg.StrongModel != "" || !cfg.SlotIsMain("strong") {
		t.Fatalf("strong slot = %q", cfg.StrongModel)
	}
	if !strings.Contains(h.m.status, "unset (main: fake/main-model)") {
		t.Fatalf("status = %q", h.m.status)
	}
}

// /model <slot> with no name opens the same dialog /model does, and the
// choice lands in the slot rather than in the session.
func TestModelSlotOpensThePickerAndWritesTheSlot(t *testing.T) {
	h, cfg, path := slotHarness(t)
	h.keys("/model cheap")
	h.updateCmd(tea.KeyMsg{Type: tea.KeyEnter}) // opens and runs the fetch
	if h.m.connect == nil || h.m.connect.slot != "cheap" || h.m.connect.step != stepModel {
		t.Fatalf("dialog: %+v", h.m.connect)
	}
	if v := plain(h.m.View()); !strings.Contains(v, "Choose the cheap model") || !strings.Contains(v, "cheap/one") {
		t.Fatalf("view:\n%s", v)
	}
	h.keys("cheap/")
	h.enter()
	if h.m.connect != nil {
		t.Fatal("the dialog stayed open")
	}
	if cfg.RouteCheap != "cheap/one" || cfg.Model != "main-model" {
		t.Fatalf("cheap=%q model=%q", cfg.RouteCheap, cfg.Model)
	}
	if got := h.m.opts.Agent.RouteCheapName(); got != "cheap/one" {
		t.Fatalf("the live agent has route_cheap = %q", got)
	}
	raw, _ := os.ReadFile(path)
	if !strings.Contains(string(raw), `"route_cheap": "cheap/one"`) {
		t.Fatalf("saved config: %s", raw)
	}
}

// An unset slot shows the main model it falls back to, everywhere it is
// shown: the status line and the argument menu.
func TestSlotStatusShowsTheFallback(t *testing.T) {
	h, cfg, _ := slotHarness(t)
	for _, s := range config.SlotNames {
		line := h.m.slotLine(s)
		if !strings.Contains(line, "unset (main: fake/main-model)") {
			t.Fatalf("%s: %q", s, line)
		}
	}
	cfg.VisionModel = "sees/things"
	if line := h.m.slotLine("vision"); !strings.Contains(line, "sees/things") || !strings.Contains(line, "nothing reads it yet") {
		t.Fatalf("vision: %q", line)
	}
	// Tab completion offers the slots by name, with that same line.
	h.keys("/model ")
	got := h.m.completions()
	var names []string
	for _, c := range got {
		names = append(names, c.name)
	}
	for _, want := range []string{"/model cheap", "/model strong", "/model advisor", "/model vision"} {
		if !slices.Contains(names, want) {
			t.Fatalf("completions %v missing %q", names, want)
		}
	}
}
