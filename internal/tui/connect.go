package tui

import (
	"context"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/xmonader/janetcode/internal/config"
	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/settingsapply"
	"github.com/xmonader/janetcode/internal/textfmt"
)

// The connect dialog is a modal drawn over the transcript with its own
// search field: provider → base URL → API keys → model. /model with no
// argument opens only the model step for the current provider.

type connectStep int

const (
	stepPick connectStep = iota
	stepName
	stepURL
	stepKeys
	stepModel
)

const (
	dialogRows  = 12
	dialogWidth = 88
	// minDialogRows is the shortest list worth drawing: below this the
	// modal is useless anyway, so it is allowed to be the whole body.
	minDialogRows = 3
)

// row is one selectable line; section groups rows under a heading.
type row struct {
	section  string
	key      string // matched by the search field
	cells    []string
	tag      string
	prov     config.Provider
	name     string
	custom   bool
	provider string
	info     llm.ModelInfo
}

type connect struct {
	step      connectStep
	modelOnly bool // opened by /model: only the last step, esc closes
	// slot names the model slot being filled (see slots.go) when the
	// dialog was opened by /model <slot>. Empty is the session's own
	// model, which is the only choice that switches the running agent.
	slot string
	name string
	prov config.Provider
	// custom is whether the provider was a custom entry, which is the
	// only kind that walks through stepName: esc from the URL goes back
	// to the list for every other one.
	custom    bool
	field     textinput.Model
	providers []row
	models    []row
	meta      map[string]llm.ModelInfo // listed models by id, for details on select
	loading   bool
	loadErr   string
	idx       int
	idxFor    string // search text the highlight belongs to
	problem   string
	// rows is the list budget the last frame fitted, so pgup and pgdown
	// move by a screenful of the window actually drawn.
	rows          int
	gen           int // ignores model lists from an abandoned dialog
	pendingModels int
}

func (c *connect) providerLabel() string {
	label := c.prov.Label(c.name)
	if label != c.name {
		return label + " (" + c.name + ")"
	}
	return label
}

// modelsMsg carries the asynchronously fetched model list.
type modelsMsg struct {
	gen      int
	provider string
	models   []llm.ModelInfo
	err      error
}

// openConnect starts the dialog at step. It refuses while a turn runs
// because the agent's provider is swapped in place.
func (m *model) openConnect(step connectStep) tea.Cmd {
	return m.openConnectFor(step, "")
}

func (m *model) openConnectFor(step connectStep, slot string) tea.Cmd {
	if m.opts.Config == nil || m.opts.Build == nil {
		m.transcript.errorf("provider switching is not available in this build")
		return nil
	}
	if m.turn.running || m.pending != nil {
		m.transcript.errorf("wait for the current turn to finish first")
		return nil
	}
	c := &connect{gen: m.connectGen + 1, field: textinput.New(), slot: slot}
	c.field.Prompt = ""
	c.field.Focus()
	m.connectGen = c.gen
	m.connect = c
	m.input.Blur()
	c.providers = m.providerRows()
	if step == stepModel {
		c.name, c.prov, c.modelOnly = m.opts.Config.Provider, m.opts.Config.Active(), true
	}
	return m.enterStep(step)
}

// providerRows lists usable configured providers, then presets, then the
// custom options.
func (m *model) providerRows() []row {
	cfg := m.opts.Config
	var rows []row
	for _, n := range cfg.ProviderNames() {
		p := cfg.Providers[n]
		if len(p.Keys()) == 0 && p.NeedsKey() {
			continue // an entry without credentials is not usable yet
		}
		label := p.Type + " compatible"
		if pre, ok := config.FindPreset(n); ok {
			label = pre.Label
		}
		detail := hostOf(p.BaseURL)
		if p.Model != "" {
			detail = p.Model
		}
		if k := len(p.Keys()); k > 1 {
			detail += fmt.Sprintf("  (%d keys)", k)
		}
		tag := ""
		if n == cfg.Provider && m.opts.Agent.Settings().Provider != nil {
			tag = "current"
		}
		rows = append(rows, row{section: "configured", key: n + " " + label, cells: []string{n, label, detail}, tag: tag, prov: p, name: n})
	}
	for _, pre := range config.Presets {
		if p, ok := cfg.Providers[pre.Name]; ok && (len(p.Keys()) > 0 || !p.NeedsKey()) {
			continue
		}
		rows = append(rows, row{section: "add a provider", key: pre.Name + " " + pre.Label, cells: []string{pre.Name, pre.Label, hostOf(pre.BaseURL)},
			prov: config.Provider{Type: pre.Type, BaseURL: pre.BaseURL}, name: pre.Name})
	}
	rows = append(rows,
		row{section: "add a provider", key: "custom openai compatible", cells: []string{"custom", "OpenAI-compatible API", "any /chat/completions server"}, prov: config.Provider{Type: "openai"}, custom: true},
		row{section: "add a provider", key: "custom anthropic compatible", cells: []string{"custom", "Anthropic-compatible API", "any /messages server"}, prov: config.Provider{Type: "anthropic"}, custom: true},
	)
	return rows
}

func hostOf(u string) string {
	if u == "" {
		return ""
	}
	if p, err := url.Parse(u); err == nil && p.Host != "" {
		return p.Host
	}
	return u
}

// enterStep prepares the field for a step and returns the command that
// loads its data.
func (m *model) enterStep(step connectStep) tea.Cmd {
	c := m.connect
	c.step, c.idx, c.idxFor, c.problem = step, 0, "", ""
	c.field.EchoMode = textinput.EchoNormal
	c.field.SetValue("")
	var cmd tea.Cmd
	switch step {
	case stepPick:
		c.field.Placeholder = "search providers"
	case stepName:
		c.field.Placeholder = "name, e.g. myserver"
	case stepURL:
		c.field.SetValue(c.prov.BaseURL)
		c.field.CursorEnd()
		if c.prov.Type == "anthropic" {
			c.field.Placeholder = "https://api.anthropic.com (default)"
		} else {
			c.field.Placeholder = "https://host/v1"
		}
	case stepKeys:
		c.field.EchoMode = textinput.EchoPassword
		c.field.EchoCharacter = '•'
		c.field.Placeholder = "paste one key, or several separated by commas"
		if len(c.prov.Keys()) > 0 {
			c.field.Placeholder = "leave empty to keep the configured keys"
		}
	case stepModel:
		c.field.Placeholder = "search models, or type a model id"
		c.models, c.meta, c.loadErr, c.loading = nil, nil, "", true
		c.pendingModels = 0
		// Each visit lists afresh, under a generation of its own: a list
		// still in flight from an earlier visit (a key since replaced, a
		// URL since changed) must not land in this one, least of all an
		// authentication failure, which sends the dialog back to the keys
		// and drops the ones just typed.
		m.connectGen++
		c.gen = m.connectGen
		if c.modelOnly && c.slot == "" {
			c.field.Placeholder = "search model or provider, or type a model id"
			cmd = tea.Batch(m.fetchConfiguredModels(), m.spinner.Tick)
		} else {
			cmd = tea.Batch(m.fetchModels(), m.spinner.Tick)
		}
	}
	m.refresh(false)
	return cmd
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
		ctx, cancel := context.WithTimeout(m.life, 20*time.Second)
		defer cancel()
		models, err := lister.ListModels(ctx)
		return modelsMsg{gen: gen, models: models, err: err}
	}
}

// rememberModel stores what the provider's list said about a model in
// the config, so prices and the context window are known next start.
func (m *model) rememberModel(mi llm.ModelInfo) {
	meta := config.MetaOf(mi)
	if m.opts.Config != nil {
		if m.opts.Config.Models == nil {
			m.opts.Config.Models = map[string]config.ModelMeta{}
		}
		m.opts.Config.Models[mi.ID] = meta
	}
	if m.opts.ConfigPath != "" {
		if err := config.SaveModelMeta(m.opts.ConfigPath, mi.ID, meta); err != nil {
			m.transcript.errorf("saving model details: %v", err)
		}
	}
}

// modelDetails renders "128k ctx · $0.15/$0.60 per M" for a listing row.
func modelDetails(mi llm.ModelInfo) string {
	var parts []string
	if mi.ContextWindow > 0 {
		parts = append(parts, textfmt.Tokens(mi.ContextWindow)+" context")
	}
	if mi.Price != nil && (mi.Price.Input > 0 || mi.Price.Output > 0) {
		parts = append(parts, fmt.Sprintf("$%s/$%s per M", trimFloat(mi.Price.Input), trimFloat(mi.Price.Output)))
	}
	return strings.Join(parts, " · ")
}

func trimFloat(f float64) string {
	s := strconv.FormatFloat(f, 'f', 2, 64)
	if strings.Contains(s, ".") {
		s = strings.TrimRight(strings.TrimRight(s, "0"), ".")
	}
	if s == "" {
		s = "0"
	}
	return s
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

// connectRemedies word a failure's advice for the dialog on screen: the
// key is the step before, the models are the list.
var connectRemedies = llm.Remedies{Key: "and paste it again", Model: "from the list"}

func (m *model) handleModels(msg modelsMsg) {
	c := m.connect
	if c == nil || c.step != stepModel || msg.gen != c.gen {
		return
	}
	if c.modelOnly && c.slot == "" && msg.provider != "" {
		m.handleConfiguredModels(msg)
		return
	}
	c.loading = false
	switch {
	case msg.err != nil:
		x := llm.Explain(msg.err, c.name, connectRemedies)
		if !x.Retry && x.Headline == "Authentication failed" {
			// The key was just refused. Letting the user go on to type a
			// model id only moves the same failure to their first prompt;
			// back to the key, with the provider's reason. The refused
			// key is dropped so the step does not offer to keep it.
			c.prov.SetKeys(nil)
			m.enterStep(stepKeys)
			m.fail(x.String())
			return
		}
		c.loadErr = "could not list models: " + x.String()
	case len(msg.models) == 0:
		c.loadErr = "the server lists no models"
	default:
		current := ""
		if c.name == m.opts.Config.Provider {
			current = m.opts.Agent.Settings().Model
		}
		suggested := config.SuggestedModels[c.name]
		// The current model first, then the suggested one, then the rest
		// by name: the highlight starts on the top row, and enter on it
		// should be a good answer.
		rank := func(id string) int {
			switch id {
			case current:
				return 0
			case suggested:
				return 1
			}
			return 2
		}
		sort.SliceStable(msg.models, func(i, j int) bool {
			if ri, rj := rank(msg.models[i].ID), rank(msg.models[j].ID); ri != rj {
				return ri < rj
			}
			return msg.models[i].ID < msg.models[j].ID
		})
		c.meta = map[string]llm.ModelInfo{}
		for _, mi := range msg.models {
			c.meta[mi.ID] = mi
			// Context and price lead and the display name trails: at
			// 80 columns the row is cut from the right, and the name
			// ("AionLabs: Aion-2.0") mostly restates the id while the
			// price ("$0…") was the part being cut.
			desc := modelDetails(mi)
			if mi.Name != "" && mi.Name != mi.ID {
				desc = strings.TrimSpace(desc + "  " + mi.Name)
			}
			r := row{section: "models · " + c.providerLabel(), key: mi.ID + " " + mi.Name + " " + c.name + " " + c.prov.Label(c.name), cells: []string{mi.ID, desc}, name: mi.ID}
			switch mi.ID {
			case current:
				r.tag = "current"
			case suggested:
				r.tag = "suggested"
			}
			c.models = append(c.models, r)
		}
	}
	m.refresh(false)
}

// visible returns the rows matching the search field and the highlight.
func (m *model) visible() ([]row, int) {
	c := m.connect
	var src []row
	switch c.step {
	case stepPick:
		src = c.providers
	case stepModel:
		src = c.models
	default:
		return nil, 0
	}
	out := matchRows(src, c.field.Value())
	if v := c.field.Value(); v != c.idxFor {
		c.idxFor, c.idx = v, 0
	}
	if c.idx >= len(out) {
		c.idx = max(len(out)-1, 0)
	}
	return out, c.idx
}

// handleConnectKey drives the dialog; every key is consumed.
func (m *model) handleConnectKey(msg tea.KeyMsg) (tea.Cmd, bool) {
	c := m.connect
	c.problem = ""
	switch keyName(msg) {
	case "ctrl+p":
		if c.modelOnly && c.slot == "" && c.step == stepModel {
			c.providers = m.providerRows()
			return m.enterStep(stepPick), true
		}
	case "ctrl+c":
		m.closeConnect("")
		return nil, true
	case "esc":
		switch {
		case c.step == stepPick, c.modelOnly:
			m.closeConnect("")
		case c.step == stepModel && !c.prov.NeedsKey():
			return m.enterStep(stepURL), true
		case c.step == stepURL && !c.custom:
			// A preset or configured provider went from the list straight
			// to its URL; one step back is the list, not a name step it
			// never showed, where typing renamed the provider.
			return m.enterStep(stepPick), true
		default:
			return m.enterStep(c.step - 1), true
		}
		return nil, true
	case "enter":
		return m.connectEnter(), true
	}
	if rows, idx := m.visible(); len(rows) > 0 {
		if next, ok := listMove(keyName(msg), idx, len(rows), m.dialogStep(c.rows)); ok {
			c.idx, c.idxFor = next, c.field.Value()
			m.refresh(false)
			return nil, true
		}
	}
	var cmd tea.Cmd
	c.field, cmd = c.field.Update(msg)
	m.refresh(false)
	return cmd, true
}

func (m *model) fail(msg string) tea.Cmd {
	m.connect.problem = msg
	m.refresh(false)
	return nil
}

func (m *model) connectEnter() tea.Cmd {
	c := m.connect
	v := strings.TrimSpace(c.field.Value())
	switch c.step {
	case stepPick:
		// The status line of a first run says "/connect", so people type
		// it here, into the dialog it already opened. A slash command is
		// a command, not a provider search: /connect is where you are,
		// anything else closes the dialog and runs.
		if strings.HasPrefix(v, "/") {
			c.field.Reset()
			if strings.EqualFold(v, "/connect") {
				m.refresh(false)
				return nil
			}
			m.closeConnect("")
			return m.command(v)
		}
		rows, idx := m.visible()
		if len(rows) == 0 {
			return m.fail("no provider matches")
		}
		r := rows[idx]
		c.name, c.prov, c.custom = r.name, r.prov, r.custom
		if c.modelOnly && !r.custom && r.section == "configured" {
			return m.enterStep(stepModel)
		}
		c.modelOnly = false
		if r.custom {
			return m.enterStep(stepName)
		}
		return m.enterStep(stepURL)
	case stepName:
		v = strings.ToLower(v)
		if v == "" || strings.ContainsAny(v, " \t/\\") {
			return m.fail("give a short name without spaces")
		}
		if p, ok := m.opts.Config.Providers[v]; ok {
			c.prov = p
		}
		c.name = v
		return m.enterStep(stepURL)
	case stepURL:
		if c.prov.Type == "openai" && v == "" {
			return m.fail("an OpenAI-compatible server needs a base URL")
		}
		if v != "" && !strings.HasPrefix(v, "http://") && !strings.HasPrefix(v, "https://") {
			return m.fail("the base URL must start with http:// or https://")
		}
		c.prov.BaseURL = strings.TrimRight(v, "/")
		if !c.prov.NeedsKey() && len(c.prov.Keys()) == 0 {
			return m.enterStep(stepModel) // local server: no key to ask for
		}
		return m.enterStep(stepKeys)
	case stepKeys:
		if keys := splitKeys(v); len(keys) > 0 {
			c.prov.SetKeys(keys)
		} else if len(c.prov.Keys()) == 0 && c.prov.NeedsKey() {
			return m.fail("this provider needs an API key")
		}
		return m.enterStep(stepModel)
	case stepModel:
		// The highlighted row wins while it still matches the search;
		// otherwise the typed text is the model id.
		rows, idx := m.visible()
		id := v
		if len(rows) > 0 {
			r := rows[idx]
			id = r.name
			if c.modelOnly && c.slot == "" && r.provider != "" {
				c.name, c.prov = r.provider, r.prov
				if c.meta == nil {
					c.meta = map[string]llm.ModelInfo{}
				}
				c.meta[id] = r.info
			}
		}
		if id == "" {
			return m.fail("type a model id")
		}
		if c.slot != "" {
			m.applySlotChoice(id)
			return nil
		}
		m.applyConnect(id)
	}
	return nil
}

func splitKeys(v string) []string {
	return strings.FieldsFunc(v, func(r rune) bool { return r == ',' || r == ' ' || r == '\n' || r == '\t' || r == ';' })
}

// applyConnect swaps the agent's provider, persists the choice and closes
// the dialog.
func (m *model) applyConnect(modelID string) {
	c := m.connect
	cfg := m.connectConfig(modelID)
	if err := settingsapply.Allowed(m.opts.Config, c.name, cfg.Providers[c.name].Type, modelID); err != nil {
		m.fail(err.Error())
		return
	}
	prov, err := m.opts.Build(cfg)
	if err != nil {
		m.enterStep(stepKeys)
		m.fail(err.Error())
		return
	}
	*m.opts.Config = *cfg
	m.opts.Provider = c.name
	note := "connected to " + c.name + " · " + modelID
	if mi, ok := c.meta[modelID]; ok {
		m.rememberModel(mi)
	}
	settingsapply.SwitchModel(m.opts.Config, m.opts.Agent, modelID, prov)
	m.refreshWelcome()
	s := m.opts.Agent.Settings()
	if d := modelDetails(llm.ModelInfo{ContextWindow: s.ContextWindow, Price: s.Price}); d != "" {
		note += " · " + d
	}
	if m.opts.ConfigPath != "" {
		if err := config.Update(m.opts.ConfigPath, c.name, c.prov, modelID); err != nil {
			m.transcript.errorf("saving config: %v", err)
		} else {
			note += " · saved to " + shortPath(m.opts.ConfigPath)
		}
	}
	m.closeConnect(note)
}

// refreshWelcome rewrites the opening banner in place once a first run
// has connected: it said "not connected yet", which is false from here
// on.
func (m *model) refreshWelcome() {
	for _, e := range m.transcript.entries {
		if e.kind == entryBanner {
			e.text = m.welcome("")
			e.rendered = "" // the banner is fixed text; force its re-render
			return
		}
	}
}

func (m *model) closeConnect(note string) {
	m.connect = nil
	m.input.Focus()
	if note != "" {
		m.note(note)
	}
	m.refresh(false)
}

// --- rendering ---

// headerModel labels the active backend in the header.
func (m *model) headerModel() string {
	if m.opts.Agent.Settings().Provider == nil {
		return "not connected · /connect"
	}
	return m.providerLabel() + "/" + m.opts.Agent.Settings().Model
}

// providerLabel is the active provider's name as a person would say it:
// "openrouter" for an openai entry OPENAI_BASE_URL aimed at OpenRouter.
func (m *model) providerLabel() string {
	if m.opts.Config == nil {
		return m.opts.Provider
	}
	return m.opts.Config.Providers[m.opts.Provider].Label(m.opts.Provider)
}

// remedies are the fixes a turn's failure can point at. A key from the
// environment outranks the config /connect writes, so for one the
// advice names the variable.
func (m *model) remedies() llm.Remedies {
	r := tuiRemedies
	if m.opts.Config == nil {
		return r
	}
	if p := m.opts.Config.Providers[m.opts.Provider]; p.KeysFromEnv() {
		switch m.opts.Provider {
		case "openai":
			r.Key = "in OPENAI_API_KEY / OPENAI_BASE_URL (they override /connect)"
		case "anthropic":
			r.Key = "in ANTHROPIC_API_KEY (it overrides /connect)"
		}
	}
	return r
}
