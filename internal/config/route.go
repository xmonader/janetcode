package config

import (
	"sort"
	"strings"
)

// Routing a model name to the backend that serves it. Asking for "explore
// this with glm-5" only works if the name alone is enough to find the
// endpoint: the model is not going to know which of the configured
// providers has it, and neither is the user in the moment.
//
// A name is resolved in three steps: an explicit route in the config
// wins, then a provider that lists the model, then the family the name
// belongs to (glm-* is Zhipu, claude-* is Anthropic) matched against
// what is actually configured. Nothing is invented — a family with no
// configured provider is simply not routable, and the caller says so.

// families maps a model-name prefix to the preset that serves it. Only
// prefixes that identify a vendor unambiguously belong here.
var families = []struct{ prefix, preset string }{
	{"claude", "anthropic"},
	{"gpt-", "openai"},
	{"o1", "openai"},
	{"o3", "openai"},
	{"o4", "openai"},
	{"glm", "zhipu"},
	{"qwen", "dashscope"},
	{"deepseek", "deepseek"},
	{"kimi", "moonshot"},
	{"moonshot", "moonshot"},
	{"minimax", "minimax"},
	{"gemini", "gemini"},
	{"grok", "xai"},
	{"mistral", "mistral"},
	{"codestral", "mistral"},
	{"llama", "groq"},
}

// Family is the preset that usually serves a model name, or "".
func Family(model string) string {
	m := strings.ToLower(strings.TrimSpace(model))
	best := ""
	bestLen := 0
	for _, f := range families {
		if strings.HasPrefix(m, f.prefix) && len(f.prefix) > bestLen {
			best, bestLen = f.preset, len(f.prefix)
		}
	}
	return best
}

// RouteFor names the configured provider that serves a model, and reports
// whether one was found. An empty model, or one already served by the
// active provider's own configuration, routes to the active provider.
func (c *Config) RouteFor(model string) (string, bool) {
	model = strings.TrimSpace(model)
	if model == "" || strings.EqualFold(model, c.Model) {
		return c.Provider, c.Provider != ""
	}
	if name, ok := c.Routes[model]; ok {
		if _, configured := c.Providers[name]; configured {
			return name, true
		}
	}
	// A route may name a prefix, the way prices and model metadata do.
	best, bestLen := "", 0
	for prefix, name := range c.Routes {
		if strings.HasPrefix(model, prefix) && len(prefix) > bestLen {
			if _, configured := c.Providers[name]; configured {
				best, bestLen = name, len(prefix)
			}
		}
	}
	if best != "" {
		return best, true
	}
	// A provider that lists the model claims it.
	for _, name := range c.ProviderNames() {
		for _, m := range c.Providers[name].Models {
			if strings.EqualFold(m, model) {
				return name, true
			}
		}
	}
	// Otherwise the family, when that provider is configured.
	if fam := Family(model); fam != "" {
		if _, ok := c.Providers[fam]; ok {
			return fam, true
		}
		// The same endpoint may be configured under another name.
		if p, ok := FindPreset(fam); ok && p.BaseURL != "" {
			for _, name := range c.ProviderNames() {
				if strings.EqualFold(strings.TrimRight(c.Providers[name].BaseURL, "/"), strings.TrimRight(p.BaseURL, "/")) {
					return name, true
				}
			}
		}
	}
	// The active provider is the fallback: many endpoints serve models
	// this table has never heard of.
	if c.Provider != "" {
		return c.Provider, true
	}
	return "", false
}

// RoutableModels lists the models the configuration can reach by name,
// with the provider that would serve each. It is what an "available
// models" question is answered with.
func (c *Config) RoutableModels() []ModelRoute {
	seen := map[string]ModelRoute{}
	add := func(model, provider, why string) {
		if model == "" || provider == "" {
			return
		}
		if _, ok := seen[model]; !ok {
			seen[model] = ModelRoute{Model: model, Provider: provider, Why: why}
		}
	}
	add(c.Model, c.Provider, "the session's model")
	for model, name := range c.Routes {
		add(model, name, "routed in the config")
	}
	for _, name := range c.ProviderNames() {
		p := c.Providers[name]
		for _, m := range p.Models {
			add(m, name, "listed by the provider")
		}
		if p.Model != "" {
			add(p.Model, name, "last used with this provider")
		}
	}
	out := make([]ModelRoute, 0, len(seen))
	for _, r := range seen {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Model < out[j].Model })
	return out
}

// ModelRoute is one model and the provider that serves it.
type ModelRoute struct {
	Model    string
	Provider string
	Why      string
}
