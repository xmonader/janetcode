package tui

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/xmonader/janetcode/internal/config"
	"github.com/xmonader/janetcode/internal/llm"
)

// The connect dialog lives in the footer: a list above the input box that
// the input filters, walked with the arrow keys. It goes provider → base
// URL → API keys → model, and /model with no argument starts at the last
// step for the current provider.

type connectStep int

const (
	stepPick connectStep = iota
	stepName
	stepURL
	stepKeys
	stepModel
)

const dialogRows = 10

// connectRow is one choice in the provider list.
type connectRow struct {
	name, label, detail string
	prov                config.Provider
	configured          bool
	custom              bool // ask for a name
}

type connect struct {
	step      connectStep
	name      string
	prov      config.Provider
	rows      []connectRow
	models    []llm.ModelInfo
	loading   bool
	loadErr   string
	idx       int
	idxFor    string // input value the highlight belongs to
	draft     string // input text saved while the dialog is open
	modelOnly bool   // opened by /model: esc closes instead of stepping back
	problem   string // shown under the title until the next key
	gen       int    // ignore model lists from an abandoned dialog
}

// modelsMsg carries the asynchronously fetched model list.
type modelsMsg struct {
	gen    int
	models []llm.ModelInfo
	err    error
}

// openConnect starts the dialog at step and returns the command that
// fetches models when it starts at the model step. It refuses while a turn
// runs because the agent's provider is swapped in place.
func (m *model) openConnect(step connectStep) tea.Cmd {
	if m.opts.Config == nil || m.opts.Build == nil {
		m.transcript.errorf("provider switching is not available in this build")
		return nil
	}
	if m.running || m.pending != nil {
		m.transcript.errorf("wait for the current turn to finish first")
		return nil
	}
	c := &connect{step: step, draft: m.input.Value(), gen: m.connectGen + 1}
	m.connectGen = c.gen
	m.connect = c
	cfg := m.opts.Config
	if step == stepModel {
		c.name, c.prov, c.modelOnly = cfg.Provider, cfg.Active(), true
	}
	c.rows = m.connectRows()
	m.input.Reset()
	m.histIdx = len(m.history)
	m.enterStep(step)
	if step == stepModel {
		return m.fetchModels()
	}
	return nil
}

// connectRows lists configured providers, then presets, then custom.
func (m *model) connectRows() []connectRow {
	cfg := m.opts.Config
	var rows []connectRow
	for _, n := range cfg.ProviderNames() {
		p := cfg.Providers[n]
		if len(p.Keys()) == 0 && p.NeedsKey() {
			continue // a bare entry (or preset picked on the command line): not usable yet
		}
		label := n
		if pre, ok := config.FindPreset(n); ok {
			label = pre.Label
		}
		detail := p.Type
		if p.BaseURL != "" {
			detail += " · " + p.BaseURL
		}
		if p.Model != "" {
			detail += " · " + p.Model
		}
		if k := len(p.Keys()); k > 1 {
			detail += fmt.Sprintf(" · %d keys", k)
		}
		rows = append(rows, connectRow{name: n, label: label, detail: detail, prov: p, configured: true})
	}
	for _, pre := range config.Presets {
		if p, ok := cfg.Providers[pre.Name]; ok && (len(p.Keys()) > 0 || !p.NeedsKey()) {
			continue
		}
		rows = append(rows, connectRow{name: pre.Name, label: pre.Label, detail: pre.BaseURL, prov: config.Provider{Type: pre.Type, BaseURL: pre.BaseURL}})
	}
	rows = append(rows,
		connectRow{name: "custom-openai", label: "Custom OpenAI-compatible API", detail: "any /v1/chat/completions server", prov: config.Provider{Type: "openai"}, custom: true},
		connectRow{name: "custom-anthropic", label: "Custom Anthropic-compatible API", detail: "any /v1/messages server", prov: config.Provider{Type: "anthropic"}, custom: true},
	)
	return rows
}

// enterStep prepares the input for a step.
func (m *model) enterStep(step connectStep) {
	c := m.connect
	c.step, c.idx, c.idxFor, c.problem = step, 0, "", ""
	m.input.Reset()
	switch step {
	case stepPick:
		m.input.Placeholder = "type to filter providers · enter picks · esc cancels"
	case stepName:
		m.input.Placeholder = "a short name for this provider, e.g. myserver"
	case stepURL:
		m.input.SetValue(c.prov.BaseURL)
		if c.prov.Type == "anthropic" {
			m.input.Placeholder = "base URL · empty uses https://api.anthropic.com"
		} else {
			m.input.Placeholder = "base URL ending in /v1"
		}
	case stepKeys:
		n := len(c.prov.Keys())
		switch {
		case n > 0:
			m.input.Placeholder = fmt.Sprintf("API key(s) · empty keeps the %d configured · separate several with commas", n)
		case !c.prov.NeedsKey():
			m.input.Placeholder = "API key (optional for a local server) · separate several with commas"
		default:
			m.input.Placeholder = "API key · separate several with commas to rotate on rate limits"
		}
	case stepModel:
		m.input.Placeholder = "type to filter models, or a model id · enter picks · esc back"
		c.models, c.loadErr, c.loading = nil, "", true
	}
	m.layout()
	m.refresh(false)
}

// fetchModels lists the provider's models in the background.
func (m *model) fetchModels() tea.Cmd {
	c := m.connect
	cfg := m.connectConfig("")
	gen := c.gen
	build := m.opts.Build
	return func() tea.Msg {
		prov, err := build(cfg)
		if err != nil {
			return modelsMsg{gen: gen, err: err}
		}
		lister, ok := prov.(llm.ModelLister)
		if !ok {
			return modelsMsg{gen: gen}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		models, err := lister.ListModels(ctx)
		return modelsMsg{gen: gen, models: models, err: err}
	}
}

// connectConfig is a copy of the config with the dialog's provider selected.
func (m *model) connectConfig(model string) *config.Config {
	c := m.connect
	cfg := *m.opts.Config
	cfg.Providers = map[string]config.Provider{}
	for k, v := range m.opts.Config.Providers {
		cfg.Providers[k] = v
	}
	cfg.Provider, cfg.Providers[c.name], cfg.Model = c.name, c.prov, model
	return &cfg
}

func (m *model) handleModels(msg modelsMsg) {
	c := m.connect
	if c == nil || c.step != stepModel || msg.gen != c.gen {
		return
	}
	c.loading = false
	switch {
	case msg.err != nil:
		c.loadErr = "could not list models: " + msg.err.Error() + " · type a model id"
	case len(msg.models) == 0:
		c.loadErr = "the server lists no models · type a model id"
	default:
		sort.Slice(msg.models, func(i, j int) bool { return msg.models[i].ID < msg.models[j].ID })
		c.models = msg.models
	}
	m.layout()
	m.refresh(false)
}

// connectList returns the rows to show, filtered by the input.
func (m *model) connectList() ([]string, []string, int) {
	c := m.connect
	q := strings.ToLower(strings.TrimSpace(m.input.Value()))
	var names, details []string
	switch c.step {
	case stepPick:
		for _, r := range c.rows {
			if q != "" && !strings.Contains(strings.ToLower(r.name+" "+r.label+" "+r.detail), q) {
				continue
			}
			label := r.label
			if r.configured {
				label = "✓ " + r.name
				if r.label != r.name {
					label += "  " + r.label
				}
			}
			names, details = append(names, label), append(details, r.detail)
		}
	case stepModel:
		for _, mi := range c.models {
			if q != "" && !strings.Contains(strings.ToLower(mi.ID+" "+mi.Name), q) {
				continue
			}
			d := ""
			if mi.Name != "" && mi.Name != mi.ID {
				d = mi.Name
			}
			names, details = append(names, mi.ID), append(details, d)
		}
	default:
		return nil, nil, 0
	}
	if v := m.input.Value(); v != c.idxFor {
		c.idxFor, c.idx = v, 0
	}
	if c.idx >= len(names) {
		c.idx = max(len(names)-1, 0)
	}
	return names, details, c.idx
}

// pickedRow maps the highlighted list entry back to its source.
func (m *model) pickedRow() (connectRow, bool) {
	c := m.connect
	names, _, idx := m.connectList()
	if len(names) == 0 {
		return connectRow{}, false
	}
	// Re-run the filter to find the idx-th surviving row.
	q := strings.ToLower(strings.TrimSpace(m.input.Value()))
	i := 0
	for _, r := range c.rows {
		if q != "" && !strings.Contains(strings.ToLower(r.name+" "+r.label+" "+r.detail), q) {
			continue
		}
		if i == idx {
			return r, true
		}
		i++
	}
	return connectRow{}, false
}

// handleConnectKey drives the dialog; it reports whether it consumed the key.
func (m *model) handleConnectKey(msg tea.KeyMsg) (tea.Cmd, bool) {
	c := m.connect
	c.problem = ""
	switch msg.String() {
	case "ctrl+c":
		m.closeConnect("")
		return nil, true
	case "esc":
		switch c.step {
		case stepPick:
			m.closeConnect("")
		case stepName:
			m.enterStep(stepPick)
		case stepURL:
			m.enterStep(stepPick)
		case stepKeys:
			m.enterStep(stepURL)
		case stepModel:
			if c.modelOnly {
				m.closeConnect("")
			} else {
				m.enterStep(stepKeys)
			}
		}
		return nil, true
	case "up", "down":
		names, _, idx := m.connectList()
		if len(names) > 0 {
			d := 1
			if msg.String() == "up" {
				d = -1
			}
			c.idx = (idx + d + len(names)) % len(names)
			c.idxFor = m.input.Value()
			m.refresh(false)
		}
		return nil, true
	case "tab":
		if c.step == stepModel {
			names, _, idx := m.connectList()
			if len(names) > 0 {
				m.input.SetValue(names[idx])
				c.idxFor, c.idx = names[idx], 0
				m.refresh(false)
			}
		}
		return nil, true
	case "enter":
		return m.connectEnter(), true
	}
	return nil, false // the textarea takes it and filters the list
}

func (m *model) connectEnter() tea.Cmd {
	c := m.connect
	v := strings.TrimSpace(m.input.Value())
	switch c.step {
	case stepPick:
		r, ok := m.pickedRow()
		if !ok {
			c.problem = "nothing matches"
			m.refresh(false)
			return nil
		}
		c.name, c.prov = r.name, r.prov
		if r.custom {
			m.enterStep(stepName)
		} else {
			m.enterStep(stepURL)
		}
	case stepName:
		v = strings.ToLower(v)
		if v == "" || strings.ContainsAny(v, " \t/\\") {
			c.problem = "give a short name without spaces"
			m.refresh(false)
			return nil
		}
		if p, ok := m.opts.Config.Providers[v]; ok {
			c.prov = p
		}
		c.name = v
		m.enterStep(stepURL)
	case stepURL:
		if c.prov.Type == "openai" && v == "" {
			c.problem = "an OpenAI-compatible server needs a base URL"
			m.refresh(false)
			return nil
		}
		if v != "" && !strings.HasPrefix(v, "http://") && !strings.HasPrefix(v, "https://") {
			c.problem = "the base URL must start with http:// or https://"
			m.refresh(false)
			return nil
		}
		c.prov.BaseURL = strings.TrimRight(v, "/")
		m.enterStep(stepKeys)
	case stepKeys:
		keys := strings.FieldsFunc(v, func(r rune) bool { return r == ',' || r == ' ' || r == '\n' || r == '\t' })
		if len(keys) > 0 {
			c.prov.SetKeys(keys)
		} else if len(c.prov.Keys()) == 0 && c.prov.NeedsKey() {
			c.problem = "this provider needs an API key"
			m.refresh(false)
			return nil
		}
		m.enterStep(stepModel)
		return m.fetchModels()
	case stepModel:
		// The highlighted row wins while it still matches what was typed;
		// otherwise the typed text is the model id.
		names, _, idx := m.connectList()
		model := v
		if len(names) > 0 && strings.Contains(strings.ToLower(names[idx]), strings.ToLower(v)) {
			model = names[idx]
		}
		if model == "" {
			c.problem = "type a model id"
			m.refresh(false)
			return nil
		}
		m.applyConnect(model)
	}
	return nil
}

// applyConnect swaps the agent's provider, persists the choice and closes
// the dialog.
func (m *model) applyConnect(modelID string) {
	c := m.connect
	cfg := m.connectConfig(modelID)
	prov, err := m.opts.Build(cfg)
	if err != nil {
		m.enterStep(stepKeys)
		c.problem = err.Error()
		m.refresh(false)
		return
	}
	*m.opts.Config = *cfg
	m.opts.Agent.Provider = prov
	m.opts.Agent.Model = modelID
	m.opts.Agent.Price = cfg.PriceFor(modelID)
	m.opts.Provider = c.name
	note := "connected to " + c.name + " · model " + modelID
	if m.opts.ConfigPath != "" {
		if err := config.Update(m.opts.ConfigPath, c.name, c.prov, modelID); err != nil {
			m.transcript.errorf("saving config: %v", err)
		} else {
			note += " · saved to " + shortPath(m.opts.ConfigPath)
		}
	}
	m.closeConnect(note)
}

func (m *model) closeConnect(note string) {
	c := m.connect
	m.connect = nil
	m.input.Reset()
	m.input.SetValue(c.draft)
	m.input.Placeholder = defaultPlaceholder
	if note != "" {
		m.transcript.info(note)
	}
	m.layout()
	m.refresh(false)
}

// connectHeight is the footer space above the input box.
func (m *model) connectHeight() int {
	names, _, _ := m.connectList()
	h := 2 // title + hint
	if m.connect.problem != "" {
		h++
	}
	if n := len(names); n > 0 {
		h += min(n, dialogRows)
	} else if m.connect.step == stepModel {
		h++ // loading / error line
	}
	return h
}

func (m *model) connectView() []string {
	c := m.connect
	var parts []string
	title := map[connectStep]string{
		stepPick:  "connect · choose a provider",
		stepName:  "connect · name the provider",
		stepURL:   "connect · " + c.name + " · base URL",
		stepKeys:  "connect · " + c.name + " · API keys",
		stepModel: "connect · " + c.name + " · choose a model",
	}[c.step]
	parts = append(parts, "  "+styleKey.Render(title))
	if c.problem != "" {
		parts = append(parts, "  "+styleErr.Render(c.problem))
	}
	names, details, idx := m.connectList()
	if len(names) == 0 && c.step == stepModel {
		switch {
		case c.loading:
			parts = append(parts, "  "+styleDim.Render(m.spinner.View()+" listing models…"))
		case c.loadErr != "":
			parts = append(parts, "  "+styleWarn.Render(truncateLine(c.loadErr, m.width-4)))
		default:
			parts = append(parts, "  "+styleDim.Render("no model matches · enter uses what you typed"))
		}
	}
	start := 0
	if idx >= dialogRows {
		start = idx - dialogRows + 1
	}
	for i := start; i < min(len(names), start+dialogRows); i++ {
		row := "  " + styleKey.Render(names[i]) + "  " + styleFaint.Render(details[i])
		if i == idx {
			row = styleAccent.Render("▸ ") + styleKey.Bold(true).Render(names[i]) + "  " + styleDim.Render(details[i])
		}
		parts = append(parts, truncateLine(row, m.width-1))
	}
	hint := "enter continues · esc back"
	switch c.step {
	case stepPick, stepModel:
		hint = fmt.Sprintf("↑↓ choose · %d of %d · enter picks · esc back", min(idx+1, len(names)), len(names))
		if c.step == stepModel {
			hint += " · tab copies the highlighted id"
		}
	case stepKeys:
		hint = "keys are stored in the config file · enter continues · esc back"
	}
	parts = append(parts, "  "+styleFaint.Render(hint))
	return parts
}

// maskedInput renders the input box contents with every key hidden.
func maskedInput(v string) string {
	if v == "" {
		return ""
	}
	var b strings.Builder
	for _, r := range v {
		if r == ',' || r == ' ' || r == '\n' {
			b.WriteRune(r)
		} else {
			b.WriteRune('•')
		}
	}
	return b.String()
}

// headerModel labels the active backend in the header.
func (m *model) headerModel() string {
	if m.opts.Agent.Provider == nil {
		return "not connected · /connect"
	}
	return m.opts.Provider + "/" + m.opts.Agent.Model
}
