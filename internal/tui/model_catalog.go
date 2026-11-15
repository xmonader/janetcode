package tui

import (
	"context"
	"maps"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/xmonader/janetcode/internal/config"
	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/settingsapply"
)

// Saved choices are visible immediately. Independent provider listings
// upgrade them as they arrive; a slow or unavailable provider cannot
// hide the other providers' models.
func (m *model) fetchConfiguredModels() tea.Cmd {
	c := m.connect
	var commands []tea.Cmd
	build, life, gen := m.opts.Build, m.life, c.gen
	sem := make(chan struct{}, 4)
	for _, name := range m.opts.Config.ProviderNames() {
		p := m.opts.Config.Providers[name]
		if settingsapply.Allowed(m.opts.Config, name, p.Type, "") != nil {
			continue
		}
		if name != c.name && len(p.Keys()) == 0 && p.NeedsKey() {
			continue
		}
		ids := append([]string(nil), p.Models...)
		ids = append(ids, p.Model)
		if name == m.opts.Config.Provider {
			ids = append(ids, m.opts.Agent.Settings().Model)
		}
		seen := map[string]bool{}
		var saved []llm.ModelInfo
		for _, id := range ids {
			if id == "" || seen[id] {
				continue
			}
			seen[id] = true
			meta := m.opts.Config.MetaFor(id)
			saved = append(saved, llm.ModelInfo{ID: id, ContextWindow: meta.ContextWindow, Price: meta.Price})
		}
		m.catalogRows(name, p, saved, false)
		cfg := *m.opts.Config
		cfg.Provider, cfg.Model = name, p.Model
		cfg.Providers, cfg.Models, cfg.Routes = maps.Clone(cfg.Providers), maps.Clone(cfg.Models), maps.Clone(cfg.Routes)
		c.pendingModels++
		commands = append(commands, func() tea.Msg {
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-life.Done():
				return modelsMsg{gen: gen, provider: name, err: life.Err()}
			}
			prov, err := build(&cfg)
			if err != nil {
				return modelsMsg{gen: gen, provider: name, err: err}
			}
			lister, ok := prov.(llm.ModelLister)
			if !ok {
				return modelsMsg{gen: gen, provider: name}
			}
			ctx, cancel := context.WithTimeout(life, 20*time.Second)
			defer cancel()
			models, err := lister.ListModels(ctx)
			return modelsMsg{gen: gen, provider: name, models: models, err: err}
		})
	}
	c.loading = c.pendingModels > 0
	return tea.Batch(commands...)
}

func (m *model) handleConfiguredModels(msg modelsMsg) {
	c := m.connect
	c.pendingModels = max(0, c.pendingModels-1)
	c.loading = c.pendingModels > 0
	if msg.err != nil {
		c.loadErr = msg.provider + ": " + llm.Explain(msg.err, msg.provider, connectRemedies).String()
		c.problem = "could not list models: " + llm.Explain(msg.err, msg.provider, connectRemedies).String() + " (" + msg.provider + ")"
	} else if len(msg.models) > 0 {
		seen := map[string]bool{}
		for _, mi := range msg.models {
			seen[mi.ID] = true
		}
		for _, r := range c.models {
			if r.provider == msg.provider && !seen[r.name] {
				msg.models = append(msg.models, r.info)
			}
		}
		m.catalogRows(msg.provider, m.opts.Config.Providers[msg.provider], msg.models, true)
	}
	m.refresh(false)
}

func (m *model) catalogRows(provider string, p config.Provider, infos []llm.ModelInfo, live bool) {
	c := m.connect
	selectedProvider, selectedModel := "", ""
	if rows, idx := m.visible(); len(rows) > 0 {
		selectedProvider, selectedModel = rows[idx].provider, rows[idx].name
	}
	kept := c.models[:0]
	for _, r := range c.models {
		if r.provider != provider {
			kept = append(kept, r)
		}
	}
	c.models = kept
	seen := map[string]bool{}
	for _, mi := range infos {
		if mi.ID == "" || seen[mi.ID] {
			continue
		}
		if settingsapply.Allowed(m.opts.Config, provider, p.Type, mi.ID) != nil {
			continue
		}
		seen[mi.ID] = true
		desc := modelDetails(mi)
		if mi.Name != "" && mi.Name != mi.ID {
			desc = strings.TrimSpace(desc + "  " + mi.Name)
		}
		tag := ""
		if !live {
			tag = "saved"
		}
		if provider == m.opts.Config.Provider && mi.ID == m.opts.Agent.Settings().Model {
			tag = "current"
		} else if mi.ID == config.SuggestedModels[provider] {
			tag = "suggested"
		}
		c.models = append(c.models, row{section: p.Label(provider), key: provider + " " + p.Label(provider) + " " + mi.ID + " " + mi.Name,
			cells: []string{mi.ID, provider, desc}, name: mi.ID, provider: provider, prov: p, info: mi, tag: tag})
	}
	rank := func(r row) int {
		if r.tag == "current" {
			return 0
		}
		if r.tag == "suggested" {
			return 1
		}
		return 2
	}
	sort.SliceStable(c.models, func(i, j int) bool {
		a, b := c.models[i], c.models[j]
		if a.provider != b.provider {
			if a.provider == c.name {
				return true
			}
			if b.provider == c.name {
				return false
			}
			return a.provider < b.provider
		}
		if rank(a) != rank(b) {
			return rank(a) < rank(b)
		}
		return a.name < b.name
	})
	if selectedModel != "" {
		for i, r := range matchRows(c.models, c.field.Value()) {
			if r.provider == selectedProvider && r.name == selectedModel {
				c.idx, c.idxFor = i, c.field.Value()
				break
			}
		}
	}
}
