package webui

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/xmonader/janetcode/internal/agent"
	"github.com/xmonader/janetcode/internal/config"
	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/tool"
)

type pickerProvider string

func (p pickerProvider) Name() string { return string(p) }
func (p pickerProvider) Stream(context.Context, llm.Request, func(llm.Event)) (*llm.Response, error) {
	return nil, nil
}
func (p pickerProvider) ListModels(context.Context) ([]llm.ModelInfo, error) {
	return []llm.ModelInfo{{ID: "shared"}}, nil
}

func TestModelPickerKeepsDuplicateProvidersAndExactSelection(t *testing.T) {
	cfg := &config.Config{Provider: "first", Model: "shared", Providers: map[string]config.Provider{
		"first": {Type: "openai", Model: "shared"}, "second": {Type: "openai", Model: "shared"},
	}}
	ag := &agent.Agent{Provider: pickerProvider("first"), Model: "shared", Tools: tool.NewRegistry()}
	s := newServer(context.Background(), Options{Agent: ag, Provider: "first", Config: cfg, Cwd: t.TempDir(),
		BuildProvider: func(name string) (llm.Provider, error) { return pickerProvider(name), nil },
		Models: func(string) (llm.Provider, string, error) {
			t.Error("explicit selection used ambiguous model routing")
			return pickerProvider("first"), "shared", nil
		},
	})
	check := func(current string) {
		t.Helper()
		w := httptest.NewRecorder()
		s.modelsAPI(w, httptest.NewRequest("GET", "/api/models", nil))
		var body struct {
			Groups []modelGroup `json:"groups"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if len(body.Groups) == 0 || body.Groups[0].Provider != current {
			t.Fatal("current provider did not lead the cached list")
		}
		found := map[string]bool{}
		for _, g := range body.Groups {
			for _, m := range g.Models {
				if m.Model == "shared" {
					found[g.Provider] = true
					if m.Current != (g.Provider == current) {
						t.Errorf("current=%v for %s", m.Current, g.Provider)
					}
				}
			}
		}
		if !found["first"] || !found["second"] {
			t.Fatalf("provider variant was deduplicated: %+v", body.Groups)
		}
	}
	check("first")
	s.model("second shared")
	if cfg.Provider != "second" || ag.Settings().Provider.Name() != "second" || ag.Settings().Model != "shared" {
		t.Fatal("the wrong provider was selected")
	}
	check("second")
}
