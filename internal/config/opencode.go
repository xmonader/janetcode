package config

import "strings"

// IsOpenCode identifies the two gateways that select their wire protocol
// per model rather than per provider.
func IsOpenCode(name string) bool {
	n := normalizeProviderName(name)
	return n == "opencode-go" || n == "opencode-zen"
}

func mergeOpenCode(cfg *Config, getenv func(string) string) {
	for _, name := range []string{"opencode-go", "opencode-zen"} {
		for alias, p := range cfg.Providers {
			if alias != name && normalizeProviderName(alias) == name {
				if _, exists := cfg.Providers[name]; !exists {
					cfg.Providers[name] = p
				}
				delete(cfg.Providers, alias)
			}
		}
		p, exists := cfg.Providers[name]
		keys, urls := []string{"OPENCODE_API_KEY", "OPENCODE_ZEN_API_KEY"}, []string{"OPENCODE_ZEN_BASE_URL", "OPENCODE_BASE_URL"}
		if name == "opencode-go" {
			keys, urls = []string{"OPENCODE_GO_API_KEY", "OPENCODE_API_KEY"}, []string{"OPENCODE_GO_BASE_URL"}
		}
		keyEnv, base := "", ""
		for _, k := range keys {
			if strings.TrimSpace(getenv(k)) != "" {
				keyEnv = k
				break
			}
		}
		for _, u := range urls {
			if strings.TrimSpace(getenv(u)) != "" {
				base = strings.TrimSpace(getenv(u))
				break
			}
		}
		if !exists && keyEnv == "" && base == "" {
			continue
		}
		if keyEnv != "" {
			explicit := p.KeyEnv
			_ = p.UseEnvKey(keyEnv, getenv)
			if explicit != "" {
				p.KeyEnv = explicit
			}
		}
		if base != "" {
			p.BaseURL = base
		}
		if p.Model == "" {
			p.Model = defaultProviderModel(name)
		}
		cfg.Providers[name] = p
	}
	for model, name := range cfg.Routes {
		if IsOpenCode(name) {
			cfg.Routes[model] = normalizeProviderName(name)
		}
	}
}

func defaultProviderModel(name string) string {
	switch name {
	case zaiCodingPlan:
		return defaultZAIModel
	case "opencode-go", "opencode-zen":
		return "kimi-k3"
	}
	return ""
}

// OpenCodeModelRef splits the provider/model spelling used by OpenCode
// and qwengo. Other model IDs containing a slash remain untouched.
func OpenCodeModelRef(ref string) (provider, model string, ok bool) {
	p, m, found := strings.Cut(strings.TrimSpace(ref), "/")
	if !found || !IsOpenCode(p) || strings.TrimSpace(m) == "" {
		return "", "", false
	}
	return normalizeProviderName(p), m, true
}
