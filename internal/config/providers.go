package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/xmonader/janetcode/internal/fsutil"
	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/opref"
	"github.com/xmonader/janetcode/internal/perm"
)

// The wire protocols a provider entry can speak.
const (
	TypeAnthropic = "anthropic"
	// TypeOpenAI is any OpenAI-compatible chat-completions server.
	TypeOpenAI = "openai"
	// TypeGemini is Google's Gemini API, spoken natively.
	TypeGemini = "gemini"
	// TypeResponses is OpenAI's Responses API.
	TypeResponses = "responses"
)

// Types lists the protocols, in the order a user is offered them.
var Types = []string{TypeAnthropic, TypeOpenAI, TypeGemini, TypeResponses}

// KnownType reports whether t is one of Types.
func KnownType(t string) bool {
	for _, k := range Types {
		if k == t {
			return true
		}
	}
	return false
}

// Provider is one configured backend. Type picks the wire protocol (see
// Types); the name in Config.Providers is how the user refers to it.
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
	// Models are the model names this provider serves, so one can be
	// asked for by name and routed here without a Routes entry.
	Models []string `json:"models,omitempty"`
	// Auth is how an Anthropic-type provider presents its key: "" or
	// "api_key" sends it as x-api-key (the Anthropic API), "bearer" as
	// Authorization: Bearer (gateways and proxies that speak the
	// Messages API, as ANTHROPIC_AUTH_TOKEN does for Claude Code).
	Auth string `json:"auth,omitempty"`
	// KeyEnv names the environment variable the keys are read from at
	// load, so the secret stays out of the config file.
	KeyEnv string `json:"key_env,omitempty"`

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

// ResolvedKeys is Keys with every 1Password reference (op://vault/item/field,
// in api_key, api_keys or the variable key_env names) replaced by the
// value `op read` returns. It is what a request is signed with; Keys stays
// the reference, so the config is saved with the reference and never with
// the value. The error names the reference that could not be read.
func (p Provider) ResolvedKeys() ([]string, error) {
	keys := p.Keys()
	out, err := opref.ResolveAll(keys)
	if err != nil {
		return nil, fmt.Errorf("provider key: %w", err)
	}
	return out, nil
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

// Bearer reports whether the key goes in an Authorization: Bearer header
// instead of x-api-key.
func (p Provider) Bearer() bool { return strings.EqualFold(strings.TrimSpace(p.Auth), "bearer") }

// KeysFromEnv reports whether the provider's keys came from an
// environment variable, which outranks the config file: advice to fix a
// key with /connect is wrong for one, since the variable wins again.
func (p Provider) KeysFromEnv() bool { return p.envKeys }

// Label is the name to show for a configured provider. The "openai"
// entry is whatever OPENAI_BASE_URL points at, and calling OpenRouter
// "openai" in the header and in error advice sent people to the wrong
// dashboard, so an "openai" entry aimed elsewhere is named after the
// preset with that host, or the host itself.
func (p Provider) Label(name string) string {
	if name != "openai" || p.BaseURL == "" {
		return name
	}
	u, err := url.Parse(p.BaseURL)
	if err != nil || u.Host == "" || strings.EqualFold(u.Host, "api.openai.com") {
		return name
	}
	for _, pre := range Presets {
		if pu, err := url.Parse(pre.BaseURL); err == nil && pu.Host != "" && strings.EqualFold(pu.Host, u.Host) {
			return pre.Name
		}
	}
	return u.Host
}

// Preset is a well-known backend the user can connect to by name; the
// base URL is a default and stays editable.
type Preset struct {
	Name    string
	Label   string
	Type    string
	BaseURL string
}

// SuggestedModels is the model /connect highlights first for a preset,
// instead of whatever sorts first alphabetically (on OpenRouter, one of
// 400-odd models nobody asked for). Only models verified live with
// janetcode are listed, and one is suggested only when the endpoint
// still lists it, so a stale entry costs nothing.
var SuggestedModels = map[string]string{
	"openrouter":       "deepseek/deepseek-v4-flash",
	"dashscope-coding": "qwen3.7-plus",
}

// Presets lists the known backends in menu order.
var Presets = []Preset{
	{"anthropic", "Anthropic", "anthropic", ""},
	{"openai", "OpenAI", "openai", "https://api.openai.com/v1"},
	{"openai-responses", "OpenAI (Responses API, keeps reasoning between calls)", "responses", "https://api.openai.com/v1"},
	{"openrouter", "OpenRouter", "openai", "https://openrouter.ai/api/v1"},
	{"opencode-go", "OpenCode Go", "openai", "https://opencode.ai/inference/go/v1"},
	{"opencode-zen", "OpenCode Zen", "openai", "https://opencode.ai/inference/v1"},
	{"dashscope-coding", "Alibaba Qwen coding plan", "openai", "https://coding-intl.dashscope.aliyuncs.com/v1"},
	{"dashscope", "Alibaba Model Studio (Qwen)", "openai", "https://dashscope-intl.aliyuncs.com/compatible-mode/v1"},
	{"deepseek", "DeepSeek", "openai", "https://api.deepseek.com/v1"},
	{"moonshot", "Moonshot (Kimi)", "openai", "https://api.moonshot.ai/v1"},
	{"zai-coding-plan", "Z.ai Coding Plan (GLM)", "openai", "https://api.z.ai/api/coding/paas/v4"},
	{"zhipu", "Zhipu (GLM)", "openai", "https://open.bigmodel.cn/api/paas/v4"},
	{"minimax", "MiniMax", "openai", "https://api.minimax.io/v1"},
	{"gemini", "Google Gemini", "gemini", "https://generativelanguage.googleapis.com/v1beta"},
	{"gemini-openai", "Google Gemini (OpenAI-compatible endpoint)", "openai", "https://generativelanguage.googleapis.com/v1beta/openai"},
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
	name = normalizeProviderName(name)
	for _, p := range Presets {
		if p.Name == name {
			return p, true
		}
	}
	return Preset{}, false
}

// Keep the documented uppercase provider spelling usable on the CLI
// and in environment/config selections, while storing one provider ID.
func normalizeProviderName(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	switch name {
	case "zai_coding_plan":
		return zaiCodingPlan
	case "opencode_go", "opencodego":
		return "opencode-go"
	case "opencode", "opencode_zen", "opencodezen", "zen":
		return "opencode-zen"
	}
	return name
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
	return update(path, name, p, model, true)
}

// UpdateProvider is Update that leaves the file's default provider and
// model alone: the provider is saved and ready, but not the one a new
// session starts on.
func UpdateProvider(path, name string, p Provider, model string) error {
	return update(path, name, p, model, false)
}

// UseEnvKey makes the provider read its keys from the environment
// variable name, resolved now through getenv and again at every load.
func (p *Provider) UseEnvKey(name string, getenv func(string) string) error {
	name = strings.TrimSpace(name)
	if name == "" || strings.IndexFunc(name, func(r rune) bool {
		return !(r == '_' || r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || r >= '0' && r <= '9')
	}) >= 0 || name[0] >= '0' && name[0] <= '9' {
		return errors.New("an environment variable name is letters, digits and underscores")
	}
	v := strings.TrimSpace(getenv(name))
	if v == "" {
		return errors.New("the environment variable " + name + " is not set where janetcode runs")
	}
	p.SetKeys(strings.Split(v, ","))
	p.KeyEnv, p.envKeys = name, true
	return nil
}

func update(path, name string, p Provider, model string, setDefault bool) error {
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
	if name == zaiCodingPlan || IsOpenCode(name) {
		for oldName, entry := range provs {
			if oldName != name && normalizeProviderName(oldName) == name {
				if _, exists := provs[name]; !exists {
					provs[name] = entry
				}
				delete(provs, oldName)
			}
		}
	}
	if p.envKeys {
		var old Provider
		if r, ok := provs[name]; ok {
			_ = json.Unmarshal(r, &old)
		}
		p.APIKey, p.APIKeys, p.Auth = old.APIKey, old.APIKeys, old.Auth
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
	if setDefault {
		set(raw, "provider", name)
		set(raw, "model", model)
	}
	if marshalErr != nil {
		return marshalErr
	}
	out, err := json.MarshalIndent(raw, "", "  ")
	if err != nil {
		return err
	}
	return writePrivate(path, append(out, '\n'))
}

// UpdateKey rewrites one top-level key of the config file at path, leaving
// every other key exactly as it was, the way Update touches only the
// provider keys. It is how /theme persists a choice: the theme name is
// written and nothing else in the file is reformatted or reordered beyond
// what re-marshalling the untouched keys does. A missing file is created.
func UpdateKey(path, key string, value any) error {
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
	b, err := json.Marshal(value)
	if err != nil {
		return err
	}
	raw[key] = b
	out, err := json.MarshalIndent(raw, "", "  ")
	if err != nil {
		return err
	}
	return writePrivate(path, append(out, '\n'))
}

// writePrivate atomically replaces path with data, readable by the owner
// only.
func writePrivate(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return fsutil.WriteFileAtomic(path, data, 0o600)
}

// AddPermission appends a rule under "permissions" in the config file,
// touching no other key. kind is "allow" or "deny".
func AddPermission(path, kind string, rule perm.Rule) error {
	if kind != "allow" && kind != "deny" && kind != "ask" {
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
	switch kind {
	case "deny":
		list = &f.Deny
	case "ask":
		list = &f.Ask
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
	if old, ok := models[model]; ok && sameMeta(old, meta) {
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

// MetaOf converts a listed model into stored metadata. A listed price
// of zero on every side is dropped: brokers list "0" for models they
// have not priced, and storing it made the cost read as a known $0.
func MetaOf(mi llm.ModelInfo) ModelMeta {
	price := mi.Price
	if zeroPrice(price) {
		price = nil
	}
	return ModelMeta{ContextWindow: mi.ContextWindow, MaxOutput: mi.MaxOutput, Price: price}
}

// zeroPrice reports a price that is present but says nothing.
func zeroPrice(p *llm.Price) bool {
	return p != nil && *p == (llm.Price{})
}

// sameMeta reports whether two model metadata records say the same thing.
// ModelMeta holds a *llm.Price, so == compares the pointers: two records
// with identical prices read from two different loads always differed,
// and the "nothing changed, do not rewrite the file" short-circuit in
// SaveModelMeta never fired -- every /connect and /model rewrote
// config.json for no change.
func sameMeta(a, b ModelMeta) bool {
	if a.ContextWindow != b.ContextWindow || a.MaxOutput != b.MaxOutput {
		return false
	}
	switch {
	case a.Price == nil && b.Price == nil:
		return true
	case a.Price == nil || b.Price == nil:
		return false
	}
	return *a.Price == *b.Price
}
