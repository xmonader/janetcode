package webui

import (
	"context"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/xmonader/janetcode/internal/config"
	"github.com/xmonader/janetcode/internal/llm"
)

// The model switcher: what the header's model button opens. The list is
// the union of everything the harness knows — the models each provider's
// config lists, the metas /connect learned, and, freshly asked, what
// every configured provider actually serves right now. A name nobody
// listed is still switchable: /model routes it by family or prefix.

type modelRow struct {
	Model   string `json:"model"`
	Context int    `json:"context,omitempty"` // window in tokens, when known
	Current bool   `json:"current,omitempty"`
	Live    bool   `json:"live,omitempty"` // listed from the provider just now
}

type modelGroup struct {
	Provider string `json:"provider"`
	// Label is the heading the menu shows: the provider's name, or for
	// an openai entry the service its URL points at ("openrouter"),
	// which "OPENAI" misnamed.
	Label  string     `json:"label,omitempty"`
	Models []modelRow `json:"models"`
}

// modelsFresh is how long a live listing is served from cache: long
// enough that opening the menu repeatedly never hammers the providers,
// short enough that a model released this morning shows up before the
// user thinks to ask.
const modelsFresh = 5 * time.Minute

func (s *server) modelsAPI(w http.ResponseWriter, _ *http.Request) {
	out := map[string]any{"provider": s.provider(), "current": "", "groups": []modelGroup{}}
	ag := s.opts.Agent
	cfg := s.opts.Config
	if ag == nil || cfg == nil {
		writeJSON(w, out)
		return
	}
	current := ag.Settings().Model
	out["current"] = current
	groups, ok := s.cachedOrLiveModels(current)
	s.cfgMu.RLock()
	defer s.cfgMu.RUnlock()
	if !ok {
		groups = s.configKnownModels(cfg, current)
	}
	labelled := make([]modelGroup, len(groups))
	for i, g := range groups {
		g.Label = g.Provider
		if p, ok := cfg.Providers[g.Provider]; ok {
			g.Label = p.Label(g.Provider)
		}
		labelled[i] = g
	}
	out["groups"] = labelled
	writeJSON(w, out)
}

// cachedOrLiveModels returns the merged list, fetching live ones when the
// cache is stale. The bool is false when nothing could be fetched and
// there is no cache to fall back on.
func (s *server) cachedOrLiveModels(current string) ([]modelGroup, bool) {
	s.modelsMu.Lock()
	if time.Since(s.modelsAt) < modelsFresh && s.modelsCache != nil {
		groups := s.modelsCache
		s.modelsMu.Unlock()
		return markCurrent(groups, current, s.provider()), true
	}
	if s.modelsFetching {
		// A fetch is in flight (another menu opened a moment ago); stale
		// beats two simultaneous provider sweeps.
		groups := s.modelsCache
		s.modelsMu.Unlock()
		return markCurrent(groups, current, s.provider()), groups != nil
	}
	s.modelsFetching = true
	s.modelsMu.Unlock()

	groups := s.fetchLiveModels()

	s.modelsMu.Lock()
	s.modelsFetching = false
	if groups != nil {
		s.modelsCache = groups
		s.modelsAt = time.Now()
	}
	s.modelsMu.Unlock()
	if groups == nil {
		return nil, false
	}
	return markCurrent(groups, current, s.provider()), true
}

// fetchLiveModels asks every configured provider what it serves, in
// parallel, and merges the answers with the config's static knowledge.
// Nil means nothing answered and there was nothing static to show.
func (s *server) fetchLiveModels() []modelGroup {
	cfg := s.opts.Config
	s.cfgMu.RLock()
	names := make([]string, 0, len(cfg.Providers))
	for name := range cfg.Providers {
		names = append(names, name)
	}
	s.cfgMu.RUnlock()
	sort.Strings(names)
	type answer struct {
		rows []modelRow
	}
	ans := make([]answer, len(names))
	if s.opts.BuildProvider != nil {
		var wg sync.WaitGroup
		for i, name := range names {
			wg.Add(1)
			go func(i int, name string) {
				defer wg.Done()
				s.cfgMu.RLock()
				prov, err := s.opts.BuildProvider(name)
				s.cfgMu.RUnlock()
				if err != nil {
					return
				}
				lister, ok := prov.(llm.ModelLister)
				if !ok {
					return
				}
				ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
				defer cancel()
				infos, err := lister.ListModels(ctx)
				if err != nil {
					return
				}
				for _, mi := range infos {
					ans[i].rows = append(ans[i].rows, modelRow{Model: mi.ID, Context: mi.ContextWindow, Live: true})
				}
			}(i, name)
		}
		wg.Wait()
	}
	// Merge: start from the static lists, upgrade rows the live sweep
	// confirmed (context, live mark), then add live rows that are new.
	s.cfgMu.RLock()
	groups := s.configKnownModels(cfg, "")
	s.cfgMu.RUnlock()
	byProvider := map[string]int{}
	for i, g := range groups {
		byProvider[g.Provider] = i
	}
	seen := map[string]bool{}
	for _, g := range groups {
		for _, m := range g.Models {
			seen[g.Provider+"\x00"+m.Model] = true
		}
	}
	for i, name := range names {
		gi, ok := byProvider[name]
		if !ok {
			groups = append(groups, modelGroup{Provider: name})
			gi = len(groups) - 1
			byProvider[name] = gi
		}
		liveIDs := map[string]bool{}
		for _, row := range ans[i].rows {
			liveIDs[row.Model] = true
		}
		merged := groups[gi].Models[:0]
		for _, m := range groups[gi].Models {
			if liveIDs[m.Model] {
				for _, live := range ans[i].rows {
					if live.Model == m.Model {
						live.Current = m.Current
						m = live
						break
					}
				}
			}
			merged = append(merged, m)
		}
		for _, live := range ans[i].rows {
			if !seen[name+"\x00"+live.Model] {
				merged = append(merged, live)
				seen[name+"\x00"+live.Model] = true
			}
		}
		groups[gi].Models = merged
	}
	for i := range groups {
		sort.SliceStable(groups[i].Models, func(a, b int) bool { return groups[i].Models[a].Model < groups[i].Models[b].Model })
	}
	// A provider that listed nothing and configures nothing has no row
	// to click; an empty group is a promise the menu cannot keep.
	kept := groups[:0]
	for _, g := range groups {
		if len(g.Models) > 0 {
			kept = append(kept, g)
		}
	}
	groups = kept
	sortGroups(groups, s.provider())
	return groups
}

// configKnownModels is the list without any live asking: what providers
// declare in the config plus the metas /connect learned. current may be
// empty to leave every Current unset (merging marks it itself).
func (s *server) configKnownModels(cfg *config.Config, current string) []modelGroup {
	seen := map[string]bool{}
	groups := []modelGroup{}
	add := func(provider, model string) {
		model = strings.TrimSpace(model)
		if model == "" || seen[provider+"\x00"+model] {
			return
		}
		seen[provider+"\x00"+model] = true
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
	if current != "" && !seen[s.provider()+"\x00"+current] {
		add(s.provider(), current)
	}
	for i := range groups {
		sort.SliceStable(groups[i].Models, func(a, b int) bool { return groups[i].Models[a].Model < groups[i].Models[b].Model })
	}
	sortGroups(groups, s.provider())
	return markCurrent(groups, current, s.provider())
}

// sortGroups puts the active provider first; the rest are alphabetical.
func sortGroups(groups []modelGroup, active string) {
	sort.SliceStable(groups, func(i, j int) bool {
		if groups[i].Provider == active {
			return true
		}
		if groups[j].Provider == active {
			return false
		}
		return groups[i].Provider < groups[j].Provider
	})
}

func markCurrent(groups []modelGroup, current, provider string) []modelGroup {
	if current == "" {
		return groups
	}
	// A copy, not the cache in place: the ✓ moves with the model, and
	// the cache must not remember who wore it last.
	out := make([]modelGroup, len(groups))
	for i, g := range groups {
		out[i] = modelGroup{Provider: g.Provider, Models: make([]modelRow, len(g.Models))}
		for j, m := range g.Models {
			m.Current = m.Model == current && g.Provider == provider
			out[i].Models[j] = m
		}
	}
	sortGroups(out, provider)
	return out
}

// contextOf is the model's window in tokens when the config knows it.
func contextOf(cfg *config.Config, model string) int {
	if cfg == nil {
		return 0
	}
	return cfg.MetaFor(model).ContextWindow
}
