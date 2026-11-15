package config

import "strings"

const zaiCodingPlan = "zai-coding-plan"
const defaultZAIModel = "glm-5"

// Coding Plan keys must use the coding endpoint, rather than the
// general prepaid endpoint. Existing URLs and explicit key_env settings
// remain configurable just like those of the other providers.
func mergeZAI(cfg *Config, getenv func(string) string) {
	for name, p := range cfg.Providers {
		if name != zaiCodingPlan && normalizeProviderName(name) == zaiCodingPlan {
			if _, exists := cfg.Providers[zaiCodingPlan]; !exists {
				cfg.Providers[zaiCodingPlan] = p
			}
			delete(cfg.Providers, name)
		}
	}
	p, configured := cfg.Providers[zaiCodingPlan]
	keyEnv := ""
	for _, name := range []string{"ZAI_CODING_PLAN_API_KEY", "ZAI_API_KEY", "ZAI_CODING_PLAN"} {
		if strings.TrimSpace(getenv(name)) != "" {
			keyEnv = name
			break
		}
	}
	if !configured && keyEnv == "" {
		return
	}
	if keyEnv != "" {
		// The later key_env pass can still override this default lookup
		// when the user explicitly chose a different variable.
		explicit := p.KeyEnv
		_ = p.UseEnvKey(keyEnv, getenv)
		if explicit != "" {
			p.KeyEnv = explicit
		}
	}
	if p.Model == "" {
		p.Model = defaultZAIModel
	}
	cfg.Providers[zaiCodingPlan] = p
	for model, name := range cfg.Routes {
		if normalizeProviderName(name) == zaiCodingPlan {
			cfg.Routes[model] = zaiCodingPlan
		}
	}
}
