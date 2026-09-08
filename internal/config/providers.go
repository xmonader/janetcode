package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/perm"
)

// Provider is one configured backend. Type picks the wire protocol
// ("anthropic" or "openai" for any OpenAI-compatible server); the name in
// Config.Providers is how the user refers to it.
type Provider struct {
	Type    string `json:"type"`
	BaseURL string `json:"base_url,omitempty"`
	APIKey  string `json:"api_key,omitempty"`
	// APIKeys are extra keys tried in turn when one is rate limited or
	// rejected.
	APIKeys []string `json:"api_keys,omitempty"`
	// Model is the last model chosen for this provider; used when the
	// top-level model does not apply.
	Model string `json:"model,omitempty"`

	// envKeys is set when the keys came from the environment, so Update
	// does not copy them into the config file.
	envKeys bool
}

// Keys returns every configured key, first APIKey then APIKeys, without
// blanks or duplicates.
func (p Provider) Keys() []string {
	var out []string
	seen := map[string]bool{}
	for _, k := range append([]string{p.APIKey}, p.APIKeys...) {
		k = strings.TrimSpace(k)
		if k == "" || seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, k)
	}
	return out
}

// SetKeys replaces the credentials with keys.
func (p *Provider) SetKeys(keys []string) {
	p.APIKey, p.APIKeys, p.envKeys = "", nil, false
	if len(keys) > 0 {
		p.APIKey = keys[0]
	}
	if len(keys) > 1 {
		p.APIKeys = keys[1:]
	}
}

// NeedsKey reports whether the backend refuses unauthenticated requests:
// everything except OpenAI-compatible servers on this machine.
func (p Provider) NeedsKey() bool {
	if p.Type != "openai" {
		return true
	}
	u := strings.ToLower(p.BaseURL)
	for _, h := range []string{"://localhost", "://127.0.0.1", "://[::1]", "://0.0.0.0"} {
		if strings.Contains(u, h) {
			return false
		}
	}
	return true
}

// Preset is a well-known backend the user can connect to by name; the
// base URL is a default and stays editable.
type Preset struct {
	Name    string
	Label   string
	Type    string
	BaseURL string
}

// Presets lists the known backends in menu order.
var Presets = []Preset{
	{"anthropic", "Anthropic", "anthropic", ""},
	{"openai", "OpenAI", "openai", "https://api.openai.com/v1"},
	{"openrouter", "OpenRouter", "openai", "https://openrouter.ai/api/v1"},
	{"dashscope-coding", "Alibaba Qwen coding plan", "openai", "https://coding-intl.dashscope.aliyuncs.com/v1"},
	{"dashscope", "Alibaba Model Studio (Qwen)", "openai", "https://dashscope-intl.aliyuncs.com/compatible-mode/v1"},
	{"deepseek", "DeepSeek", "openai", "https://api.deepseek.com/v1"},
	{"moonshot", "Moonshot (Kimi)", "openai", "https://api.moonshot.ai/v1"},
	{"zhipu", "Zhipu (GLM)", "openai", "https://open.bigmodel.cn/api/paas/v4"},
	{"minimax", "MiniMax", "openai", "https://api.minimax.io/v1"},
	{"gemini", "Google Gemini", "openai", "https://generativelanguage.googleapis.com/v1beta/openai"},
	{"xai", "xAI (Grok)", "openai", "https://api.x.ai/v1"},
	{"mistral", "Mistral", "openai", "https://api.mistral.ai/v1"},
	{"groq", "Groq", "openai", "https://api.groq.com/openai/v1"},
	{"cerebras", "Cerebras", "openai", "https://api.cerebras.ai/v1"},
	{"together", "Together", "openai", "https://api.together.xyz/v1"},
	{"fireworks", "Fireworks", "openai", "https://api.fireworks.ai/inference/v1"},
	{"ollama", "Ollama (local, no key)", "openai", "http://localhost:11434/v1"},
	{"lmstudio", "LM Studio (local, no key)", "openai", "http://localhost:1234/v1"},
}

// FindPreset returns the preset called name.
func FindPreset(name string) (Preset, bool) {
	for _, p := range Presets {
		if p.Name == name {
			return p, true
		}
	}
	return Preset{}, false
}

// Active returns the selected provider entry.
func (c *Config) Active() Provider {
	return c.Providers[c.Provider]
}

// ProviderNames lists the configured providers, sorted.
func (c *Config) ProviderNames() []string {
	names := make([]string, 0, len(c.Providers))
	for n := range c.Providers {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// Update records provider p under name as the selected backend with model,
// touching nothing else in the config file at path (which is created when
// missing). Keys that came from the environment are not written; keys the
// file already holds for that entry are kept in that case.
func Update(path, name string, p Provider, model string) error {
	raw := map[string]json.RawMessage{}
	data, err := os.ReadFile(path)
	switch {
	case err == nil:
		if err := json.Unmarshal(data, &raw); err != nil {
			return fmt.Errorf("config %s: %w", path, err)
		}
	case errors.Is(err, os.ErrNotExist):
	default:
		return err
	}
	provs := map[string]json.RawMessage{}
	if r, ok := raw["providers"]; ok && len(r) > 0 {
		if err := json.Unmarshal(r, &provs); err != nil {
			return fmt.Errorf("config %s: providers: %w", path, err)
		}
	}
	if p.envKeys {
		var old Provider
		if r, ok := provs[name]; ok {
			_ = json.Unmarshal(r, &old)
		}
		p.APIKey, p.APIKeys = old.APIKey, old.APIKeys
	}
	p.Model = model
	var marshalErr error
	set := func(m map[string]json.RawMessage, k string, v any) {
		b, err := json.Marshal(v)
		if err != nil {
			marshalErr = err
			return
		}
		m[k] = b
	}
	set(provs, name, p)
	set(raw, "providers", provs)
	set(raw, "provider", name)
	set(raw, "model", model)
	if marshalErr != nil {
		return marshalErr
	}
	out, err := json.MarshalIndent(raw, "", "  ")
	if err != nil {
		return err
	}
	return writePrivate(path, append(out, '\n'))
}

// writePrivate atomically replaces path with data, readable by the owner
// only.
func writePrivate(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".config-*.json")
	if err != nil {
		return err
	}
	name := tmp.Name()
	cleanup := func(err error) error {
		tmp.Close()
		os.Remove(name)
		return err
	}
	if err := tmp.Chmod(0o600); err != nil {
		return cleanup(err)
	}
	if _, err := tmp.Write(data); err != nil {
		return cleanup(err)
	}
	if err := tmp.Close(); err != nil {
		return cleanup(err)
	}
	if err := os.Rename(name, path); err != nil {
		return cleanup(err)
	}
	return nil
}

// AddPermission appends a rule under "permissions" in the config file,
// touching no other key. kind is "allow" or "deny".
func AddPermission(path, kind string, rule perm.Rule) error {
	if kind != "allow" && kind != "deny" {
		return fmt.Errorf("bad permission kind %q", kind)
	}
	raw := map[string]json.RawMessage{}
	if data, err := os.ReadFile(path); err == nil {
		if err := json.Unmarshal(data, &raw); err != nil {
			return fmt.Errorf("config %s: %w", path, err)
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	var f perm.File
	if r, ok := raw["permissions"]; ok && len(r) > 0 {
		if err := json.Unmarshal(r, &f); err != nil {
			return fmt.Errorf("config %s: permissions: %w", path, err)
		}
	}
	list := &f.Allow
	if kind == "deny" {
		list = &f.Deny
	}
	for _, e := range *list {
		if e == rule.String() {
			return nil
		}
	}
	*list = append(*list, rule.String())
	enc, err := json.Marshal(f)
	if err != nil {
		return err
	}
	raw["permissions"] = enc
	out, err := json.MarshalIndent(raw, "", "  ")
	if err != nil {
		return err
	}
	return writePrivate(path, append(out, '\n'))
}

// SaveModelMeta records what a model list said about a model, rewriting
// only the "models" key of the config file.
func SaveModelMeta(path, model string, meta ModelMeta) error {
	if model == "" || (meta.ContextWindow == 0 && meta.MaxOutput == 0 && meta.Price == nil) {
		return nil
	}
	raw := map[string]json.RawMessage{}
	if data, err := os.ReadFile(path); err == nil {
		if err := json.Unmarshal(data, &raw); err != nil {
			return fmt.Errorf("config %s: %w", path, err)
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	models := map[string]ModelMeta{}
	if r, ok := raw["models"]; ok && len(r) > 0 {
		if err := json.Unmarshal(r, &models); err != nil {
			return fmt.Errorf("config %s: models: %w", path, err)
		}
	}
	if old, ok := models[model]; ok && old == meta {
		return nil
	}
	models[model] = meta
	enc, err := json.Marshal(models)
	if err != nil {
		return err
	}
	raw["models"] = enc
	out, err := json.MarshalIndent(raw, "", "  ")
	if err != nil {
		return err
	}
	return writePrivate(path, append(out, '\n'))
}

// MetaOf converts a listed model into stored metadata.
func MetaOf(mi llm.ModelInfo) ModelMeta {
	return ModelMeta{ContextWindow: mi.ContextWindow, MaxOutput: mi.MaxOutput, Price: mi.Price}
}
