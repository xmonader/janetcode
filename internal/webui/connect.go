package webui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/xmonader/janetcode/internal/config"
	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/perm"
	"github.com/xmonader/janetcode/internal/settingsapply"
)

// /connect in the browser: the terminal's connect dialog as one form.
// The page lists the providers, asks the chosen one what it serves with
// the key typed in, and connects: the agent switches onto the new client
// and the choice is written to the config file, as the TUI does. A first
// run with no key at all opens here instead of refusing to start.

// connectRemedies word the advice for the form the reader is looking at:
// the key field is right above the message.
var connectRemedies = llm.Remedies{Key: "above", Model: "from the list"}

type connectProvider struct {
	Name  string `json:"name"`
	Label string `json:"label"`
	Type  string `json:"type"`
	URL   string `json:"url"`
	// HasKey says a key is already configured, so the key field may be
	// left empty. The key itself never goes to the page.
	HasKey     bool `json:"has_key"`
	NeedsKey   bool `json:"needs_key"`
	Configured bool `json:"configured"`
}

type connectRequest struct {
	Action string `json:"action"` // "models" or "connect"
	Name   string `json:"name"`
	Type   string `json:"type"`
	URL    string `json:"url"`
	Keys   string `json:"keys"`
	Model  string `json:"model"`
}

type connectModel struct {
	ID      string `json:"id"`
	Name    string `json:"name,omitempty"`
	Context int    `json:"context,omitempty"`
}

func (s *server) connectAPI(w http.ResponseWriter, r *http.Request) {
	cfg := s.opts.Config
	if cfg == nil || s.opts.Build == nil {
		http.Error(w, "connecting is not available in this session", http.StatusNotImplemented)
		return
	}
	if r.Method == http.MethodGet {
		writeJSON(w, map[string]any{"providers": s.connectProviders(), "current": s.opts.Provider, "config": perm.ShortenHome(s.opts.ConfigPath)})
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "GET or POST", http.StatusMethodNotAllowed)
		return
	}
	var req connectRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&req); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	next, p, err := s.connectConfig(req)
	if err != nil {
		writeJSON(w, map[string]any{"error": llm.Explanation{Reason: err.Error()}})
		return
	}
	switch req.Action {
	case "models":
		models, err := s.listConnectModels(r.Context(), next)
		if err != nil {
			writeJSON(w, map[string]any{"error": llm.Explain(err, req.Name, connectRemedies)})
			return
		}
		writeJSON(w, map[string]any{"models": models})
	case "connect":
		note, err := s.applyConnect(next, req.Name, p, strings.TrimSpace(req.Model))
		if err != nil {
			writeJSON(w, map[string]any{"error": llm.Explain(err, req.Name, connectRemedies)})
			return
		}
		writeJSON(w, map[string]any{"ok": true, "note": note})
	default:
		http.Error(w, "action is models or connect", http.StatusBadRequest)
	}
}

// connectProviders is the providers that have a key first, then every
// other preset. The page adds the two custom kinds.
func (s *server) connectProviders() []connectProvider {
	cfg := s.opts.Config
	var out []connectProvider
	var names []string
	for n := range cfg.Providers {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		p := cfg.Providers[n]
		// The loader puts a keyless default in Providers on a machine
		// with no config; that is a preset, not something the user set up.
		if _, preset := config.FindPreset(n); preset && len(p.Keys()) == 0 {
			continue
		}
		label := n
		if pre, ok := config.FindPreset(n); ok {
			label = pre.Label
		}
		out = append(out, connectProvider{Name: n, Label: label, Type: p.Type, URL: p.BaseURL,
			HasKey: len(p.Keys()) > 0, NeedsKey: p.NeedsKey(), Configured: true})
	}
	for _, pre := range config.Presets {
		p, ok := cfg.Providers[pre.Name]
		if ok && len(p.Keys()) > 0 {
			continue
		}
		if !ok {
			p = config.Provider{Type: pre.Type, BaseURL: pre.BaseURL}
		}
		out = append(out, connectProvider{Name: pre.Name, Label: pre.Label, Type: p.Type, URL: p.BaseURL, NeedsKey: p.NeedsKey()})
	}
	return out
}

// connectConfig is a copy of the config with the form's provider selected,
// and that provider as it will be saved. An empty key field keeps the
// keys the provider already has.
func (s *server) connectConfig(req connectRequest) (*config.Config, config.Provider, error) {
	cfg := s.opts.Config
	name := strings.ToLower(strings.TrimSpace(req.Name))
	if name == "" || strings.ContainsAny(name, " \t/\\") {
		return nil, config.Provider{}, errors.New("give the provider a short name without spaces")
	}
	p, ok := cfg.Providers[name]
	if !ok {
		if pre, found := config.FindPreset(name); found {
			p = config.Provider{Type: pre.Type, BaseURL: pre.BaseURL}
		}
	}
	if t := strings.TrimSpace(req.Type); t != "" {
		p.Type = t
	}
	if p.Type != "openai" && p.Type != "anthropic" {
		return nil, p, errors.New("pick a provider, or say whether a custom server is OpenAI- or Anthropic-compatible")
	}
	u := strings.TrimRight(strings.TrimSpace(req.URL), "/")
	if u != "" && !strings.HasPrefix(u, "http://") && !strings.HasPrefix(u, "https://") {
		return nil, p, errors.New("the base URL must start with http:// or https://")
	}
	if p.Type == "openai" && u == "" {
		return nil, p, errors.New("an OpenAI-compatible server needs a base URL")
	}
	p.BaseURL = u
	if keys := strings.FieldsFunc(req.Keys, func(r rune) bool { return strings.ContainsRune(", \n\t;", r) }); len(keys) > 0 {
		p.SetKeys(keys)
	} else if len(p.Keys()) == 0 && p.NeedsKey() {
		return nil, p, errors.New("this provider needs an API key")
	}
	next := *cfg
	next.Providers = map[string]config.Provider{}
	for k, v := range cfg.Providers {
		next.Providers[k] = v
	}
	next.Provider, next.Providers[name] = name, p
	next.Model = strings.TrimSpace(req.Model)
	return &next, p, nil
}

func (s *server) listConnectModels(ctx context.Context, cfg *config.Config) ([]connectModel, error) {
	prov, err := s.opts.Build(cfg)
	if err != nil {
		return nil, err
	}
	lister, ok := prov.(llm.ModelLister)
	if !ok {
		return []connectModel{}, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	models, err := lister.ListModels(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]connectModel, 0, len(models))
	s.mu.Lock()
	s.connectMeta = map[string]llm.ModelInfo{}
	for _, mi := range models {
		s.connectMeta[mi.ID] = mi
		name := ""
		if mi.Name != mi.ID {
			name = mi.Name
		}
		out = append(out, connectModel{ID: mi.ID, Name: name, Context: mi.ContextWindow})
	}
	s.mu.Unlock()
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// applyConnect switches the agent onto the new provider and saves the
// choice. It refuses while a turn runs: the turn holds the old client.
func (s *server) applyConnect(next *config.Config, name string, p config.Provider, model string) (string, error) {
	if model == "" {
		return "", errors.New("pick a model, or type its id")
	}
	s.mu.Lock()
	running := s.running
	mi, known := s.connectMeta[model]
	s.mu.Unlock()
	if running {
		return "", errors.New("wait for the current turn to finish, then connect")
	}
	prov, err := s.opts.Build(next)
	if err != nil {
		return "", err
	}
	*s.opts.Config = *next
	s.opts.Provider = name
	if known && s.opts.ConfigPath != "" {
		meta := config.MetaOf(mi)
		if s.opts.Config.Models == nil {
			s.opts.Config.Models = map[string]config.ModelMeta{}
		}
		s.opts.Config.Models[model] = meta
		if err := config.SaveModelMeta(s.opts.ConfigPath, model, meta); err != nil {
			s.errf("saving what %s said about %s: %v", name, model, err)
		}
	}
	settingsapply.SwitchModel(s.opts.Config, s.opts.Agent, model, prov)
	note := fmt.Sprintf("Connected to %s · %s", name, model)
	if s.opts.ConfigPath != "" {
		if err := config.Update(s.opts.ConfigPath, name, p, model); err != nil {
			s.errf("connected, but saving the config failed: %v", err)
		} else {
			note += " · saved to " + perm.ShortenHome(s.opts.ConfigPath)
		}
	}
	s.notice(note)
	s.emit(envelope{Type: "hello", Hello: ptr(s.hello())})
	s.emitStatus()
	return note, nil
}

func (s *server) connected() bool {
	return s.opts.Agent == nil || s.opts.Agent.Settings().Provider != nil
}
