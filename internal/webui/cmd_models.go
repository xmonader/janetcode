package webui

import (
	"net/http"
	"sort"
	"strings"

	"github.com/xmonader/janetcode/internal/config"
)

// The model switcher: what the header's model button opens. The list is
// what the configuration already knows — the models each provider serves
// plus the metas /connect learned — and the picker also accepts a name
// nobody listed, which /model routes by family or prefix. Listing live
// from every provider is the terminal wizard's job; the header answers
// in a beat.

type modelRow struct {
	Model    string `json:"model"`
	Context  int    `json:"context,omitempty"` // window in tokens, when known
	Current  bool   `json:"current,omitempty"`
}

type modelGroup struct {
	Provider string     `json:"provider"`
	Models   []modelRow `json:"models"`
}

func (s *server) modelsAPI(w http.ResponseWriter, _ *http.Request) {
	out := map[string]any{"provider": s.opts.Provider, "current": "", "groups": []modelGroup{}}
	ag := s.opts.Agent
	cfg := s.opts.Config
	if ag == nil || cfg == nil {
		writeJSON(w, out)
		return
	}
	current := ag.Settings().Model
	out["current"] = current
	seen := map[string]bool{}
	groups := []modelGroup{}
	add := func(provider, model string) {
		model = strings.TrimSpace(model)
		if model == "" || seen[model] {
			return
		}
		seen[model] = true
		for i := range groups {
			if groups[i].Provider == provider {
				groups[i].Models = append(groups[i].Models, modelRow{Model: model, Context: contextOf(cfg, model)})
				return
			}
		}
		groups = append(groups, modelGroup{Provider: provider, Models: []modelRow{{Model: model, Context: contextOf(cfg, model)}}})
	}
	names := make([]string, 0, len(cfg.Providers))
	for name := range cfg.Providers {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		p := cfg.Providers[name]
		add(name, p.Model)
		for _, m := range p.Models {
			add(name, m)
		}
	}
	// Learned metas may name models no provider lists anymore; they are
	// still switchable, under the provider the router would pick.
	learned := make([]string, 0, len(cfg.Models))
	for m := range cfg.Models {
		learned = append(learned, m)
	}
	sort.Strings(learned)
	for _, m := range learned {
		prov, _ := cfg.RouteFor(m)
		add(prov, m)
	}
	for _, m := range cfg.FallbackModels {
		prov, _ := cfg.RouteFor(m)
		add(prov, m)
	}
	// The model in use leads even when nothing lists it: it is the one
	// the reader most wants to see, and the ✓ has to land somewhere.
	if current != "" && !seen[current] {
		add(s.opts.Provider, current)
	}
	for i := range groups {
		sort.SliceStable(groups[i].Models, func(a, b int) bool { return groups[i].Models[a].Model < groups[i].Models[b].Model })
		for j := range groups[i].Models {
			groups[i].Models[j].Current = groups[i].Models[j].Model == current
		}
	}
	sort.SliceStable(groups, func(i, j int) bool {
		if groups[i].Provider == s.opts.Provider {
			return true
		}
		if groups[j].Provider == s.opts.Provider {
			return false
		}
		return groups[i].Provider < groups[j].Provider
	})
	out["groups"] = groups
	writeJSON(w, out)
}

// contextOf is the model's window in tokens when the config knows it.
func contextOf(cfg *config.Config, model string) int {
	if cfg == nil {
		return 0
	}
	return cfg.MetaFor(model).ContextWindow
}
