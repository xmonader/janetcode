package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/xmonader/janetcode/internal/agent"
	"github.com/xmonader/janetcode/internal/config"
	"github.com/xmonader/janetcode/internal/llm"
)

func TestModelPickerNamesAndSwitchesProvider(t *testing.T) {
	h := newHarness(t)
	cfg := &config.Config{Provider: "opencode-go", Model: "kimi-k3", Providers: map[string]config.Provider{
		"opencode-go":  {Type: "openai", APIKey: "go-key", Model: "kimi-k3"},
		"opencode-zen": {Type: "openai", APIKey: "zen-key", Model: "kimi-k3"},
	}}
	goModels := &listing{models: []llm.ModelInfo{{ID: "kimi-k3", ContextWindow: 128000}}}
	zenModels := &listing{models: []llm.ModelInfo{{ID: "kimi-k3", ContextWindow: 256000}}}
	h.withConfig(t, cfg, func(c *config.Config) (llm.Provider, error) {
		if c.Provider == "opencode-zen" {
			return zenModels, nil
		}
		return goModels, nil
	})
	h.m.opts.Provider = "opencode-go"
	h.m.opts.Agent.UpdateSettings(func(s *agent.Settings) { s.Model = "kimi-k3" })
	h.keys("/model")
	h.updateCmd(tea.KeyMsg{Type: tea.KeyEnter})
	v := plain(h.m.View())
	if !strings.Contains(v, "all configured providers") || !strings.Contains(v, "opencode-go") || !strings.Contains(v, "opencode-zen") {
		t.Fatalf("provider is hidden:\n%s", v)
	}
	h.keys("opencode-zen")
	h.enter()
	if cfg.Provider != "opencode-zen" || cfg.Model != "kimi-k3" || h.m.opts.Agent.Settings().Provider != zenModels {
		t.Fatal("picked model was routed through another provider")
	}
	if h.m.opts.Agent.Settings().ContextWindow != 256000 {
		t.Fatal("selection used metadata from the other provider's identical model")
	}
}

func TestModelPickerShowsSavedProvidersBeforeNetworkResults(t *testing.T) {
	h := newHarness(t)
	cfg := &config.Config{Provider: "first", Model: "shared", Providers: map[string]config.Provider{
		"first":           {Type: "openai", APIKey: "first-key", Model: "shared"},
		"zai-coding-plan": {Type: "openai", APIKey: "env-key", Model: "glm-5"},
	}}
	h.withConfig(t, cfg, func(*config.Config) (llm.Provider, error) {
		t.Fatal("network command ran before saved models were shown")
		return nil, nil
	})
	h.m.openConnect(stepModel)
	h.keys("zai")
	rows, _ := h.m.visible()
	if len(rows) != 1 || rows[0].provider != "zai-coding-plan" || rows[0].name != "glm-5" {
		t.Fatalf("saved provider is missing: %+v", rows)
	}
	if !h.m.connect.loading {
		t.Fatal("fixture did not leave network listings pending")
	}
}

func TestModelPickerCanAddOpenCodeWithoutAnExistingKey(t *testing.T) {
	h := newHarness(t)
	cfg := &config.Config{Provider: "first", Model: "shared", Providers: map[string]config.Provider{"first": {Type: "openai", APIKey: "first-key", Model: "shared"}}}
	h.withConfig(t, cfg, func(*config.Config) (llm.Provider, error) { return &listing{}, nil })
	h.m.openConnect(stepModel)
	h.update(tea.KeyMsg{Type: tea.KeyCtrlP})
	h.keys("opencode")
	rows, _ := h.m.visible()
	if len(rows) != 2 {
		t.Fatalf("OpenCode presets missing: %+v", rows)
	}
	h.enter()
	if h.m.connect.modelOnly || h.m.connect.step != stepURL || h.m.connect.name != "opencode-go" {
		t.Fatal("unconfigured OpenCode did not start the connection flow")
	}
}

func TestExplicitModelCommandPinsDuplicateProvider(t *testing.T) {
	h := newHarness(t)
	cfg := &config.Config{Provider: "first", Model: "shared", Providers: map[string]config.Provider{"first": {Type: "openai", Model: "shared"}, "second": {Type: "openai", Model: "shared"}}}
	chosen := &listing{}
	h.withConfig(t, cfg, func(c *config.Config) (llm.Provider, error) {
		if c.Provider != "second" || c.Model != "shared" {
			t.Errorf("built %s/%s", c.Provider, c.Model)
		}
		return chosen, nil
	})
	completions := h.m.completeModel("shared")
	if len(completions) != 2 || completions[0].name != "/model first shared" || completions[1].name != "/model second shared" {
		t.Fatalf("completion lost a provider: %+v", completions)
	}
	h.keys("/model second shared")
	h.enter()
	if cfg.Provider != "second" || cfg.Model != "shared" || h.m.opts.Agent.Settings().Provider != chosen {
		t.Fatal("explicit provider selection was ignored")
	}
}

func TestModelPickerNamesTheGatewayBehindOpenAI(t *testing.T) {
	c := &connect{name: "openai", prov: config.Provider{Type: "openai", BaseURL: "https://openrouter.ai/api/v1"}}
	if got := c.providerLabel(); got != "openrouter (openai)" {
		t.Fatalf("provider label=%q", got)
	}
}
