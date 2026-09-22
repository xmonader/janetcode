package tui

import (
	"os"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/xmonader/janetcode/internal/config"
	"github.com/xmonader/janetcode/internal/jev"
	"github.com/xmonader/janetcode/internal/settingsapply"
)

// /jev is the decision service in one command: whether it is on, which
// endpoint and which pinned model it asks, which consumers are on, and
// what the calls have cost so far.
//
// The client is rebuilt from the config whenever anything here changes
// (settingsapply.WireDecisions), so every one of these takes effect in
// the session that types it rather than the next one. The one thing that
// cannot be turned on from here is the kill switch: JANETCODE_OFF=decisions
// is an ablation for a whole run, and a command that could undo it would
// make the ablation a suggestion.

// jevCommand is /jev [on|off|status|model|provider|<consumer> on|off].
func (m *model) jevCommand(args []string) tea.Cmd {
	cfg := m.opts.Config
	if cfg == nil {
		m.transcript.errorf("/jev needs a config file")
		return nil
	}
	if len(args) == 0 || strings.EqualFold(args[0], "status") {
		m.transcript.info(m.jevStatus())
		return nil
	}
	head := strings.ToLower(strings.TrimSpace(args[0]))
	switch head {
	case "on", "off":
		m.jevSet("decisions.enabled", head)
		return nil
	case "model":
		if len(args) > 1 {
			m.jevSet("decisions.model", strings.TrimSpace(args[1]))
			return nil
		}
		return m.openJevModel()
	case "provider", "endpoint":
		if len(args) > 1 {
			m.jevSet("decisions.endpoint", strings.TrimSpace(args[1]))
			return nil
		}
		return m.openJevProvider()
	}
	// The rest are the consumers, which /features also toggles: one
	// table, two ways in.
	f, ok := findFeatureSwitch(head)
	if !ok || !strings.HasPrefix(f.key, "decisions.") {
		m.transcript.errorf("usage: /jev [on|off|model|provider|%s on|off]", strings.Join(config.DecisionConsumers, "|"))
		return nil
	}
	if len(args) < 2 {
		m.note(m.featureLine(f))
		return nil
	}
	on, ok := parseOnOff(args[1])
	if !ok {
		m.transcript.errorf("usage: /jev %s on|off", f.name)
		return nil
	}
	m.jevSet(f.key, f.value(cfg, on))
	return nil
}

// jevSet writes one decisions key and reports the whole state, because
// these keys are not independent: switching the feature on with no
// consumer on changes nothing, and saying so is the point of the line.
func (m *model) jevSet(key, val string) {
	if _, err := settingsapply.Apply(m.opts.Config, m.opts.Agent, m.opts.ConfigPath, key, val); err != nil {
		m.transcript.errorf("%v", err)
		return
	}
	m.note(m.jevStatusLine())
}

// jevStatusLine is the one-line state: on or off, and why when it is off
// for a reason the config alone does not explain.
func (m *model) jevStatusLine() string {
	cfg := m.opts.Config
	if cfg == nil {
		return "jev: no config"
	}
	d := cfg.Decisions
	if ag := m.opts.Agent; ag != nil && ag.Off["decisions"] {
		return "jev: off · JANETCODE_OFF=decisions, which no command here overrides"
	}
	switch {
	case d == nil:
		return "jev: not configured · /jev on"
	case !d.On():
		return "jev: off · " + d.Model + " on " + d.EndpointName() + " · /jev on"
	}
	line := "jev: on · " + d.Model + " on " + d.EndpointName()
	if os.Getenv(d.KeyEnv()) == "" {
		// The key itself is never read into a message; only whether the
		// variable has one.
		return line + " · " + d.KeyEnv() + " is empty, so nothing is asked"
	}
	var on []string
	for _, c := range config.DecisionConsumers {
		if d.ConsumerOn(c) {
			on = append(on, c)
		}
	}
	if len(on) == 0 {
		return line + " · no consumer is on (/jev triage on)"
	}
	return line + " · " + strings.Join(on, ", ")
}

// jevStatus is the panel: the line above, each consumer, and what the
// service has cost this session.
func (m *model) jevStatus() string {
	var sb strings.Builder
	sb.WriteString(m.jevStatusLine() + "\n")
	for _, f := range decisionSwitches() {
		sb.WriteString("  " + m.featureLine(f) + "\n")
	}
	if ag := m.opts.Agent; ag != nil {
		if line := ag.Stats().DecisionLine(); line != "" {
			sb.WriteString("  " + line + "\n")
		} else {
			sb.WriteString("  nothing asked yet this session\n")
		}
		if ag.DecisionsClient() {
			sb.WriteString("  changes here apply to this session, not only the next one")
		} else {
			sb.WriteString("  no client: /jev on, a key in the environment, and one consumer")
		}
	}
	return strings.TrimRight(sb.String(), "\n")
}

// openJevProvider picks the endpoint. There is one today; the list is
// what makes a second one (TypeSafe's native host) a row rather than a
// change to this command.
func (m *model) openJevProvider() tea.Cmd {
	cfg := m.opts.Config
	cur := ""
	if cfg.Decisions != nil {
		cur = cfg.Decisions.EndpointName()
	}
	var rows []row
	for _, e := range config.DecisionEndpoints {
		r := row{key: e.Name + " " + e.BaseURL, name: e.Name, cells: []string{e.Name, e.Label, e.BaseURL + " · key " + e.KeyEnv}}
		if e.Name == cur {
			r.tag = "current"
		}
		rows = append(rows, r)
	}
	return m.openPicker(&picker{
		title: "Decision service",
		hint:  "enter chooses the endpoint · esc closes",
		rows:  rows,
		choose: func(r row, typed string) tea.Cmd {
			name := r.name
			if name == "" {
				name = typed
			}
			m.closePicker()
			m.jevSet("decisions.endpoint", name)
			return nil
		},
	})
}

// openJevModel picks the pinned decision model. Nothing enumerates the
// service's versions — the endpoint has no model listing, and the chat
// provider's list does not carry them — so the list is the slug whose
// wire contract is verified plus whatever you type, judged by the same
// rule the config loader applies (jev.PinnedModel).
func (m *model) openJevModel() tea.Cmd {
	cfg := m.opts.Config
	cur := ""
	if cfg.Decisions != nil {
		cur = cfg.Decisions.Model
	}
	rows := []row{{
		key:   jev.DefaultModel,
		name:  jev.DefaultModel,
		cells: []string{jev.DefaultModel, "the slug the wire contract is verified against; every answer reports the version it ran"},
	}}
	if cur != "" && cur != jev.DefaultModel {
		rows = append(rows, row{key: cur, name: cur, cells: []string{cur, "configured now"}, tag: "current"})
	} else if cur != "" {
		rows[0].tag = "current"
	}
	return m.openPicker(&picker{
		title:    "Decision model",
		hint:     "enter chooses · or type a pinned id (jev-X.Y.Z, ~typesafe/jev-*) · esc closes",
		rows:     rows,
		freeText: true,
		choose: func(r row, typed string) tea.Cmd {
			name := strings.TrimSpace(r.name)
			if name == "" {
				name = strings.TrimSpace(typed)
			}
			m.closePicker()
			m.jevSet("decisions.model", name)
			return nil
		},
	})
}

// jevArgs completes /jev's own words.
func (m *model) completeJev(partial string) []command {
	words := append([]string{"on", "off", "status", "model", "provider"}, config.DecisionConsumers...)
	help := map[string]string{
		"on": "turn the decision service on", "off": "turn it off (the config keys are kept)",
		"status": "what is on, and what it has cost", "model": "choose the pinned decision model",
		"provider": "choose the endpoint",
	}
	var out []command
	for _, w := range words {
		if partial != "" && !strings.HasPrefix(w, strings.ToLower(partial)) {
			continue
		}
		h := help[w]
		if h == "" {
			if f, ok := findFeatureSwitch(w); ok {
				h = onOffWord(f.on(m.opts.Config)) + " · " + f.what
			}
		}
		out = append(out, command{name: "/jev " + w, help: h})
	}
	return out
}

// completeFeatures completes /features' switch names.
func (m *model) completeFeatures(partial string) []command {
	var out []command
	for _, f := range allFeatureSwitches() {
		if partial != "" && !strings.HasPrefix(f.name, strings.ToLower(partial)) {
			continue
		}
		out = append(out, command{name: "/features " + f.name, help: onOffWord(f.on(m.opts.Config)) + " · " + f.what})
	}
	return out
}
