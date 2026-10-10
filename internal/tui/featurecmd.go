package tui

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/xmonader/janetcode/internal/config"
	"github.com/xmonader/janetcode/internal/settingsapply"
)

// The features that use the model slots, in one list you can read and
// change. Each of them was a key in the config file and nothing else,
// which made "is the cascade on?" a question only a restart could
// answer, and left a filled strong slot looking like a working cascade
// when escalate_after was still zero.
//
// Every switch here is the same config key /config already sets, so
// there is one way a setting is stored, one way it is saved and one
// way it reaches the running agent (settingsapply.Apply).

// featureSwitch is one on/off feature: which config key holds it, how to
// read it as a boolean, and what value each answer writes.
type featureSwitch struct {
	name string
	key  string
	what string
	on   func(cfg *config.Config) bool
	// value is what is written for on and for off. It takes the config
	// so a switch can keep a number it already has: turning the cascade
	// off and on again should not forget that it was patient.
	value func(cfg *config.Config, on bool) string
	// caveat says what the switch still needs to do anything.
	caveat func(cfg *config.Config) string
}

var featureSwitches = []featureSwitch{
	{
		name: "escalate",
		key:  "escalate_after",
		what: "a turn whose verification gate keeps failing finishes on the strong slot",
		on:   func(cfg *config.Config) bool { return cfg.EscalateAfter > 0 },
		value: func(cfg *config.Config, on bool) string {
			if !on {
				return "0"
			}
			if cfg.EscalateAfter > 0 {
				return strconv.Itoa(cfg.EscalateAfter)
			}
			return "1"
		},
		caveat: func(cfg *config.Config) string {
			if cfg.EscalateAfter > 0 && cfg.SlotIsMain("strong") {
				return "the strong slot is the session's own model (/model strong)"
			}
			return ""
		},
	},
	{
		name: "advisor",
		key:  "advisor_mode",
		what: "the harness buys advisor consults instead of offering the tool",
		on:   func(cfg *config.Config) bool { return cfg.AdvisorMode == "always" },
		value: func(_ *config.Config, on bool) string {
			if on {
				return "always"
			}
			return "off"
		},
		caveat: func(cfg *config.Config) string {
			if cfg.AdvisorMode == "always" && cfg.SlotIsMain("advisor") {
				return "the advisor slot is the session's own model (/model advisor)"
			}
			return ""
		},
	},
	{
		name:  "stale-nudge",
		key:   "stale_check_nudge",
		what:  "a turn that edited after its checks passed is sent back once",
		on:    func(cfg *config.Config) bool { return cfg.StaleCheckNudge },
		value: func(_ *config.Config, on bool) string { return onOffWord(on) },
	},
	{
		name:  "effort-ctl",
		key:   "effort_control",
		what:  "each call's reasoning tier follows the turn: low, up on trouble, halved on progress",
		on:    func(cfg *config.Config) bool { return cfg.EffortControl },
		value: func(_ *config.Config, on bool) string { return onOffWord(on) },
	},
}

// decisionSwitches builds the consumer switches from the config package's
// list, so a consumer added there appears here without being named twice.
func decisionSwitches() []featureSwitch {
	what := map[string]string{
		"triage":  "a verify failure is triaged before a repair round is spent",
		"intake":  "the session's first prompt is classified (logged, acted on by route)",
		"route":   "a small read-only first prompt starts on the cheap slot",
		"outcome": "one call at the end of the session classifies how it went",
	}
	out := make([]featureSwitch, 0, len(config.DecisionConsumers))
	for _, name := range config.DecisionConsumers {
		out = append(out, featureSwitch{
			name:  name,
			key:   config.DecisionConsumerKey(name),
			what:  what[name],
			on:    func(cfg *config.Config) bool { return cfg.Decisions.ConsumerOn(name) },
			value: func(_ *config.Config, on bool) string { return onOffWord(on) },
			caveat: func(cfg *config.Config) string {
				if !cfg.Decisions.ConsumerOn(name) {
					return ""
				}
				if name == "route" {
					switch {
					case !cfg.Decisions.ConsumerOn("intake"):
						return "needs intake on (/jev intake on)"
					case cfg.SlotIsMain("cheap"):
						return "needs a cheap slot (/model cheap)"
					}
				}
				return ""
			},
		})
	}
	return out
}

// allFeatureSwitches is every switch /features knows, the three that use
// the slots and the decision consumers /jev also toggles.
func allFeatureSwitches() []featureSwitch {
	return append(append([]featureSwitch{}, featureSwitches...), decisionSwitches()...)
}

func findFeatureSwitch(name string) (featureSwitch, bool) {
	name = strings.ToLower(strings.TrimSpace(name))
	for _, f := range allFeatureSwitches() {
		if f.name == name {
			return f, true
		}
	}
	return featureSwitch{}, false
}

func onOffWord(on bool) string {
	if on {
		return "on"
	}
	return "off"
}

// parseOnOff reads the word a toggle command takes.
func parseOnOff(s string) (bool, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "on", "true", "yes", "1":
		return true, true
	case "off", "false", "no", "0":
		return false, true
	}
	return false, false
}

// featuresCommand is /features: the list, or one switch changed.
func (m *model) featuresCommand(args []string) {
	cfg := m.opts.Config
	if cfg == nil {
		m.transcript.errorf("features need a config file")
		return
	}
	if len(args) == 0 {
		m.transcript.info(m.featuresList())
		return
	}
	f, ok := findFeatureSwitch(args[0])
	if !ok {
		m.transcript.errorf("no feature %q (%s)", args[0], strings.Join(featureNames(), ", "))
		return
	}
	if len(args) < 2 {
		m.note(m.featureLine(f))
		return
	}
	on, ok := parseOnOff(args[1])
	if !ok {
		m.transcript.errorf("usage: /features %s on|off", f.name)
		return
	}
	if _, err := settingsapply.Apply(cfg, m.opts.Agent, m.opts.ConfigPath, f.key, f.value(cfg, on)); err != nil {
		m.transcript.errorf("%v", err)
		return
	}
	m.note(m.featureLine(f))
}

func featureNames() []string {
	all := allFeatureSwitches()
	out := make([]string, 0, len(all))
	for _, f := range all {
		out = append(out, f.name)
	}
	return out
}

// featureLine is one switch's state, with what still stands in its way.
func (m *model) featureLine(f featureSwitch) string {
	cfg := m.opts.Config
	line := fmt.Sprintf("%-12s %-3s  %s", f.name, onOffWord(f.on(cfg)), f.what)
	if f.caveat != nil {
		if c := f.caveat(cfg); c != "" {
			line += " · " + c
		}
	}
	return line
}

// featuresList is the whole panel: the slots first, because every switch
// below them is about which model does what.
func (m *model) featuresList() string {
	var sb strings.Builder
	sb.WriteString("model slots · /model <slot> [name|off]\n")
	for _, l := range m.slotLines() {
		sb.WriteString("  " + l + "\n")
	}
	sb.WriteString("\nfeatures · /features <name> on|off\n")
	for _, f := range featureSwitches {
		sb.WriteString("  " + m.featureLine(f) + "\n")
	}
	sb.WriteString("\ndecisions (jev) · /jev <name> on|off\n")
	for _, f := range decisionSwitches() {
		sb.WriteString("  " + m.featureLine(f) + "\n")
	}
	sb.WriteString("  " + m.jevStatusLine())
	return strings.TrimRight(sb.String(), "\n")
}
