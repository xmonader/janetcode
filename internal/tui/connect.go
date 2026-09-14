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
	"github.com/charmbracelet/lipgloss"

	"github.com/xmonader/janetcode/internal/agent"
	"github.com/xmonader/janetcode/internal/config"
	"github.com/xmonader/janetcode/internal/llm"
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

// fitRows is how many list lines a modal may draw when the body is
// height rows tall and chrome lines are spent on the title, the search
// field and the help line. A short terminal used to get the full
// twelve, which pushed the title and the search field off the top of
// the screen because lipgloss.Place centres what it is given.
// dialogStep is how far pgup and pgdown move in a modal list: the window
// the last frame actually fitted, never zero.
func (m *model) dialogStep(win int) int {
	if win < minDialogRows {
		return dialogRows
	}
	return win
}

func fitRows(height, chrome int) int {
	avail := height - chrome
	if avail < 1 {
		return 0
	}
	if avail > dialogRows {
		return dialogRows
	}
	return avail
}

// clipHeight keeps s to at most n lines so a modal never pushes the
// reserved footer off the terminal. lipgloss.Place centres a box and
// will emit more rows than height when the box is taller than the body.
func clipHeight(s string, n int) string {
	if n <= 0 || s == "" {
		return s
	}
	lines := strings.Split(s, "\n")
	if len(lines) <= n {
		return s
	}
	return strings.Join(lines[:n], "\n")
}

// row is one selectable line; section groups rows under a heading.
type row struct {
	section string
	key     string // matched by the search field
	cells   []string
	tag     string
	prov    config.Provider
	name    string
	custom  bool
}

type connect struct {
	step      connectStep
	modelOnly bool // opened by /model: only the last step, esc closes
	name      string
	prov      config.Provider
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
	rows int
	gen  int // ignores model lists from an abandoned dialog
}

// modelsMsg carries the asynchronously fetched model list.
type modelsMsg struct {
	gen    int
	models []llm.ModelInfo
	err    error
}

// openConnect starts the dialog at step. It refuses while a turn runs
// because the agent's provider is swapped in place.
func (m *model) openConnect(step connectStep) tea.Cmd {
	if m.opts.Config == nil || m.opts.Build == nil {
		m.transcript.errorf("provider switching is not available in this build")
		return nil
	}
	if m.running || m.pending != nil {
		m.transcript.errorf("wait for the current turn to finish first")
		return nil
	}
	c := &connect{gen: m.connectGen + 1, field: textinput.New()}
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
		if n == cfg.Provider && m.opts.Agent.Provider != nil {
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
		c.models, c.loadErr, c.loading = nil, "", true
		cmd = tea.Batch(m.fetchModels(), m.spinner.Tick)
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
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
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

// applyModelMeta sets the agent's price and context window for a model.
func (m *model) applyModelMeta(modelID string) {
	ag := m.opts.Agent
	if m.opts.Config == nil {
		ag.ContextWindow = llm.KnownWindow(modelID)
		return
	}
	ag.Price = m.opts.Config.PriceFor(modelID)
	ag.ContextWindow = m.opts.Config.MetaFor(modelID).ContextWindow
}

// modelDetails renders "128k ctx · $0.15/$0.60 per M" for a listing row.
func modelDetails(mi llm.ModelInfo) string {
	var parts []string
	if mi.ContextWindow > 0 {
		parts = append(parts, textfmt.Tokens(mi.ContextWindow)+" ctx")
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

func (m *model) handleModels(msg modelsMsg) {
	c := m.connect
	if c == nil || c.step != stepModel || msg.gen != c.gen {
		return
	}
	c.loading = false
	switch {
	case msg.err != nil:
		c.loadErr = "could not list models: " + msg.err.Error()
	case len(msg.models) == 0:
		c.loadErr = "the server lists no models"
	default:
		current := ""
		if c.name == m.opts.Config.Provider {
			current = m.opts.Agent.Settings().Model
		}
		sort.SliceStable(msg.models, func(i, j int) bool {
			if (msg.models[i].ID == current) != (msg.models[j].ID == current) {
				return msg.models[i].ID == current
			}
			return msg.models[i].ID < msg.models[j].ID
		})
		c.meta = map[string]llm.ModelInfo{}
		for _, mi := range msg.models {
			c.meta[mi.ID] = mi
			name := ""
			if mi.Name != "" && mi.Name != mi.ID {
				name = mi.Name
			}
			if d := modelDetails(mi); d != "" {
				name = strings.TrimSpace(name + "  " + d)
			}
			r := row{key: mi.ID + " " + mi.Name, cells: []string{mi.ID, name}, name: mi.ID}
			if mi.ID == current {
				r.tag = "current"
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
	case "ctrl+c":
		m.closeConnect("")
		return nil, true
	case "esc":
		switch {
		case c.step == stepPick, c.modelOnly:
			m.closeConnect("")
		case c.step == stepModel && !c.prov.NeedsKey():
			return m.enterStep(stepURL), true
		default:
			return m.enterStep(c.step - 1), true
		}
		return nil, true
	case "up", "down", "ctrl+p", "ctrl+n":
		rows, idx := m.visible()
		if len(rows) > 0 {
			d := 1
			if keyName(msg) == "up" || keyName(msg) == "ctrl+p" {
				d = -1
			}
			c.idx = (idx + d + len(rows)) % len(rows)
			c.idxFor = c.field.Value()
			m.refresh(false)
		}
		return nil, true
	case "pgup", "pgdown":
		rows, idx := m.visible()
		if len(rows) > 0 {
			if keyName(msg) == "pgup" {
				c.idx = max(idx-m.dialogStep(c.rows), 0)
			} else {
				c.idx = min(idx+m.dialogStep(c.rows), len(rows)-1)
			}
			c.idxFor = c.field.Value()
			m.refresh(false)
		}
		return nil, true
	case "enter":
		return m.connectEnter(), true
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
		rows, idx := m.visible()
		if len(rows) == 0 {
			return m.fail("no provider matches")
		}
		r := rows[idx]
		c.name, c.prov = r.name, r.prov
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
			id = rows[idx].name
		}
		if id == "" {
			return m.fail("type a model id")
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
	prov, err := m.opts.Build(cfg)
	if err != nil {
		m.enterStep(stepKeys)
		m.fail(err.Error())
		return
	}
	*m.opts.Config = *cfg
	m.opts.Agent.Provider = prov
	m.opts.Agent.UpdateSettings(func(s *agent.Settings) { s.Model = modelID })
	m.opts.Provider = c.name
	note := "connected to " + c.name + " · " + modelID
	if mi, ok := c.meta[modelID]; ok {
		m.rememberModel(mi)
	}
	m.applyModelMeta(modelID)
	if d := modelDetails(llm.ModelInfo{ContextWindow: m.opts.Agent.ContextWindow, Price: m.opts.Agent.Price}); d != "" {
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

func (m *model) closeConnect(note string) {
	m.connect = nil
	m.input.Focus()
	if note != "" {
		m.note(note)
	}
	m.refresh(false)
}

// --- rendering ---

// connectView draws the modal centred in the body area.
func (m *model) connectView(height int) string {
	c := m.connect
	w := min(dialogWidth, m.width-4)
	inner := w - 4 // border + padding
	c.field.Width = inner - 3
	var lines []string
	add := func(s string) { lines = append(lines, truncateLine(s, inner)) }

	add(styleKey.Render(m.dialogTitle()) + "  " + m.breadcrumb())
	if ctx := m.dialogContext(); ctx != "" {
		add(styleDim.Render(ctx))
	}
	add("")
	add(styleAccent.Render("› ") + c.field.View())
	add(styleFaint.Render(strings.Repeat("─", inner)))
	if c.problem != "" {
		add(styleErr.Render("✗ " + c.problem))
	}
	switch c.step {
	case stepPick, stepModel:
		// Two border rows, the lines added above, the blank and the help
		// line below, and the "n of m" counter.
		budget := fitRows(height, len(lines)+5)
		m.connect.rows = budget
		lines = append(lines, m.listLines(inner, budget)...)
	case stepName:
		add(styleDim.Render("Used as the provider's name in the config file and in --provider."))
	case stepURL:
		add(styleDim.Render("Edit the URL if your server lives elsewhere; enter keeps it."))
	case stepKeys:
		n := len(splitKeys(c.field.Value()))
		have := len(c.prov.Keys())
		switch {
		case n > 0:
			add(styleDim.Render(fmt.Sprintf("%s entered · several keys are rotated when one is rate limited", textfmt.Plural(n, "key"))))
		case have > 0:
			add(styleDim.Render(fmt.Sprintf("%s already configured · enter keeps them, or paste new ones", textfmt.Plural(have, "key"))))
		default:
			add(styleDim.Render("Keys are hidden while typing and stored in " + shortPath(m.opts.ConfigPath)))
		}
	}
	add("")
	add(styleFaint.Render(m.dialogHelp()))
	box := styleDialog.Width(w - 2).Render(strings.Join(lines, "\n"))
	return clipHeight(lipgloss.Place(m.width, height, lipgloss.Center, lipgloss.Center, box), height)
}

func (m *model) dialogTitle() string {
	c := m.connect
	switch {
	case c.modelOnly:
		return "Choose a model"
	case c.step == stepPick:
		return "Connect to a provider"
	default:
		return "Connect · " + c.name
	}
}

func (m *model) breadcrumb() string {
	c := m.connect
	if c.modelOnly {
		return ""
	}
	steps := []struct {
		s connectStep
		l string
	}{{stepPick, "provider"}, {stepURL, "url"}, {stepKeys, "keys"}, {stepModel, "model"}}
	var parts []string
	for _, st := range steps {
		cur := st.s == c.step || (st.s == stepPick && c.step == stepName)
		if cur {
			parts = append(parts, styleAccent.Bold(true).Render(st.l))
		} else {
			parts = append(parts, styleFaint.Render(st.l))
		}
	}
	return strings.Join(parts, styleFaint.Render(" › "))
}

// dialogContext summarises what has been chosen so far.
func (m *model) dialogContext() string {
	c := m.connect
	if c.step == stepPick || c.step == stepName {
		return ""
	}
	parts := []string{c.prov.Type + " API"}
	if c.step > stepURL && c.prov.BaseURL != "" {
		parts = append(parts, c.prov.BaseURL)
	}
	if c.step > stepKeys {
		switch n := len(c.prov.Keys()); {
		case n == 1:
			parts = append(parts, "1 key")
		case n > 1:
			parts = append(parts, fmt.Sprintf("%d keys", n))
		default:
			parts = append(parts, "no key")
		}
	}
	return strings.Join(parts, " · ")
}

func (m *model) listLines(inner, budget int) []string {
	c := m.connect
	rows, idx := m.visible()
	var out []string
	if len(rows) == 0 {
		switch {
		case c.step == stepModel && c.loading:
			out = append(out, "  "+styleDim.Render(m.spinner.View()+" fetching models from "+hostOf(c.prov.BaseURL)+"…"))
		case c.step == stepModel && c.loadErr != "":
			out = append(out, "  "+styleWarn.Render(truncateLine(c.loadErr, inner-2)))
			out = append(out, "  "+styleDim.Render("type the model id and press enter"))
		case c.field.Value() != "":
			out = append(out, "  "+styleDim.Render("nothing matches"))
		}
		return out
	}
	return renderRows(rows, idx, inner, budget)
}

// matchRows keeps the rows whose key contains every word of the query, so
// "gpt mini" finds a model however its parts are ordered.
func matchRows(src []row, query string) []row {
	words := strings.Fields(strings.ToLower(strings.TrimSpace(query)))
	var out []row
	for _, r := range src {
		key := strings.ToLower(r.key)
		ok := true
		for _, w := range words {
			if !strings.Contains(key, w) {
				ok = false
				break
			}
		}
		if ok {
			out = append(out, r)
		}
	}
	return out
}

// fits reports whether a window starting at start still draws row idx
// within budget lines, counting the section headings it must emit.
func fits(rows []row, start, idx, budget int) bool {
	used, section := 0, ""
	if start > 0 {
		section = rows[start-1].section
	}
	for i := start; i < len(rows); i++ {
		if rows[i].section != "" && rows[i].section != section {
			section = rows[i].section
			used++
		}
		used++
		if used > budget {
			return false
		}
		if i == idx {
			return true
		}
	}
	return false
}

// renderRows draws a window of a row list with its sections, columns and
// highlight. Every searchable list in the interface goes through it.
// budget is the number of screen lines the window may occupy, section
// headings included, so a short terminal gets a shorter list rather than
// a box taller than the screen.
func renderRows(rows []row, idx, inner, budget int) []string {
	if budget <= 0 {
		budget = dialogRows
	}
	widths := columnWidths(rows, inner-4)
	// Section headings take a line too, so the window that holds the
	// highlight is found by walking the start back until idx is drawn.
	start := 0
	if idx >= budget {
		start = idx - budget + 1
	}
	for start > 0 && !fits(rows, start, idx, budget) {
		start++
	}
	var out []string
	section := ""
	if start > 0 {
		section = rows[start-1].section
	}
	shown := 0
	for i := start; i < len(rows) && len(out) < budget; i++ {
		r := rows[i]
		if r.section != "" && r.section != section {
			if len(out)+2 > budget {
				break // a heading with no row under it says nothing
			}
			section = r.section
			out = append(out, styleSection.Render(strings.ToUpper(section)))
		}
		shown++
		// The last column takes what is left after the tag.
		last := inner - 2
		for _, w := range widths[:len(widths)-1] {
			last -= w + 2
		}
		if r.tag != "" {
			last -= lipgloss.Width(r.tag) + 2
		}
		var cells []string
		for j, cell := range r.cells {
			if j < len(widths)-1 {
				cell = pad(cell, widths[j])
			} else {
				cell = pad(cell, max(last, 4))
			}
			switch {
			case i == idx && j == 0:
				cell = styleSelected.Render(cell)
			case j == 0:
				cell = styleKey.Render(cell)
			case j == len(r.cells)-1:
				cell = styleFaint.Render(cell)
			}
			cells = append(cells, cell)
		}
		line := strings.Join(cells, "  ")
		if r.tag != "" {
			line += "  " + styleTag.Render(r.tag)
		}
		if i == idx {
			line = styleAccent.Render("▸ ") + line
		} else {
			line = "  " + line
		}
		out = append(out, truncateLine(line, inner))
	}
	if shown < len(rows) {
		out = append(out, styleFaint.Render(fmt.Sprintf("  %d of %d", idx+1, len(rows))))
	}
	return out
}

// columnWidths sizes every column but the last to its widest cell, capped
// so the row fits.
func columnWidths(rows []row, avail int) []int {
	if len(rows) == 0 {
		return nil
	}
	n := len(rows[0].cells)
	widths := make([]int, n)
	for _, r := range rows {
		for j, c := range r.cells {
			if j < n-1 {
				widths[j] = max(widths[j], lipgloss.Width(c))
			}
		}
	}
	cap := avail / max(n, 1)
	for j := range widths {
		widths[j] = min(widths[j], cap)
	}
	return widths
}

func pad(s string, w int) string {
	s = truncateLine(s, w)
	if d := w - lipgloss.Width(s); d > 0 {
		s += strings.Repeat(" ", d)
	}
	return s
}

func (m *model) dialogHelp() string {
	c := m.connect
	switch c.step {
	case stepPick:
		return "↑↓ move · enter select · esc close"
	case stepModel:
		rows, idx := m.visible()
		back := "esc back"
		if c.modelOnly {
			back = "esc close"
		}
		if len(rows) == 0 && strings.TrimSpace(c.field.Value()) != "" {
			return fmt.Sprintf("enter uses %q as the model id · %s", strings.TrimSpace(c.field.Value()), back)
		}
		if len(rows) > 0 {
			return "↑↓ move · enter uses " + rows[idx].name + " · " + back
		}
		return "↑↓ move · enter select · " + back
	default:
		return "enter continue · esc back"
	}
}

// headerModel labels the active backend in the header.
func (m *model) headerModel() string {
	if m.opts.Agent.Provider == nil {
		return "not connected · /connect"
	}
	return m.opts.Provider + "/" + m.opts.Agent.Settings().Model
}
