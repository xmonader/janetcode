package config

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/xmonader/janetcode/internal/jev"
	"github.com/xmonader/janetcode/internal/llm"
)

// setter parses one /config value and stores it on cfg, returning what
// was stored -- the typed value, so a caller can save it to the file and
// report it without re-parsing.
type setter func(*Config, string) (any, error)

// setters is the one table of what /config can set: key to how it is
// parsed and where it lands. It was a hundred-line switch in which nine
// of the nineteen arms were the same seven lines, and in which a key
// could be listed as settable and not actually handled.
//
// The live half of a change -- telling a running agent about it -- is
// settingsapply.Apply's, because it is the only part that needs an agent.
var setters = map[string]setter{
	// Tri-state booleans: unset in the file means "whatever the default
	// is", so these are *bool and the setter takes the address of one.
	"thinking":      boolPtr(func(c *Config) **bool { return &c.Thinking }),
	"show_thinking": boolPtr(func(c *Config) **bool { return &c.ShowThinking }),
	"lean_tools":    boolPtr(func(c *Config) **bool { return &c.LeanTools_ }),
	"intent":        boolPtr(func(c *Config) **bool { return &c.Intent }),
	"hints":         boolPtr(func(c *Config) **bool { return &c.Hints }),
	"followup":      boolPtr(func(c *Config) **bool { return &c.Followup }),
	"computer_use":  boolPtr(func(c *Config) **bool { return &c.ComputerUse }),
	"memory":        boolPtr(func(c *Config) **bool { return &c.Memory }),
	"proof_notes":   boolPtr(func(c *Config) **bool { return &c.ProofNotes }),

	// auto_approve has no "unset": off is a decision, not an absence.
	"auto_approve": func(c *Config, val string) (any, error) {
		b, err := parseBool(val)
		if err != nil {
			return nil, err
		}
		c.AutoApprove = b
		return b, nil
	},

	"compact_at": intKey("compact_at", false, func(c *Config) *int { return &c.CompactAt }),
	// A zero or negative cap is not a smaller budget, it is a session
	// that cannot make a call, so these two refuse it.
	"max_tokens":     intKey("max_tokens", true, func(c *Config) *int { return &c.MaxTokens }),
	"max_iterations": intKey("max_iterations", true, func(c *Config) *int { return &c.MaxIterations }),

	"model": func(c *Config, val string) (any, error) {
		if val == "" {
			return nil, fmt.Errorf("model is empty")
		}
		c.Model = val
		return val, nil
	},
	"effort": func(c *Config, val string) (any, error) {
		// Refused here as well as at load: a tier that only fails on the
		// next start would be written to the file and silently ignored
		// for the rest of the session that set it.
		if !llm.ValidEffort(val) {
			return nil, fmt.Errorf("effort wants one of %s (or empty for the provider's own)", llm.EffortList())
		}
		c.Effort = val
		return val, nil
	},
	// Empty is a valid value for these two: it is how the experimental
	// router is turned back off (see RouteCheap) and how the advisor
	// experiment is ended.
	"route_cheap":   stringKey(func(c *Config) *string { return &c.RouteCheap }),
	"advisor_model": stringKey(func(c *Config) *string { return &c.AdvisorModel }),
	"strong_model":  stringKey(func(c *Config) *string { return &c.StrongModel }),
	"vision_model":  stringKey(func(c *Config) *string { return &c.VisionModel }),

	// The features the slots exist for. Each was configurable in the
	// file and nowhere else, which made "is the cascade on?" a question
	// only a restart could answer.
	"vim_mode":               boolKey(func(c *Config) *bool { return &c.VimMode }),
	"ask_timeout":            askWaitKey("ask_timeout", func(c *Config) *string { return &c.AskTimeout }),
	"ask_timeout_unattended": askWaitKey("ask_timeout_unattended", func(c *Config) *string { return &c.AskTimeoutUnattended }),
	"stale_check_nudge":      boolKey(func(c *Config) *bool { return &c.StaleCheckNudge }),
	"effort_control":         boolKey(func(c *Config) *bool { return &c.EffortControl }),
	"elide_checks":           boolKey(func(c *Config) *bool { return &c.ElideChecks }),
	"localize_failures":      boolKey(func(c *Config) *bool { return &c.LocalizeFailures }),
	"conflict_exit":          boolKey(func(c *Config) *bool { return &c.ConflictExit }),
	"subagent_merge":         boolKey(func(c *Config) *bool { return &c.SubagentMerge }),
	"probe_ledger":           boolKey(func(c *Config) *bool { return &c.ProbeLedger }),
	"blind_tester":           blindTesterKey,
	"spec_coverage":          boolKey(func(c *Config) *bool { return &c.SpecCoverage }),
	"spec_examples":          boolKey(func(c *Config) *bool { return &c.SpecExamples }),
	"flake_quarantine":       boolKey(func(c *Config) *bool { return &c.FlakeQuarantine }),
	"fix_cache":              boolKey(func(c *Config) *bool { return &c.FixCache }),
	"speculate":              boolKey(func(c *Config) *bool { return &c.Speculate }),
	"plan_file":              boolKey(func(c *Config) *bool { return &c.PlanFile }),
	"hedge_provider":         hedgeProviderKey,
	"hedge_after_ms":         countKey("hedge_after_ms", func(c *Config) *int { return &c.HedgeAfterMs }),
	"stream_resume":          boolKey(func(c *Config) *bool { return &c.StreamResume }),
	"fingerprint":            boolKey(func(c *Config) *bool { return &c.Fingerprint }),
	"probe_memory":           boolKey(func(c *Config) *bool { return &c.ProbeMemory }),
	"read_blame":             boolKey(func(c *Config) *bool { return &c.ReadBlame }),
	"numeric_claims_nudge":   boolKey(func(c *Config) *bool { return &c.NumericClaimsNudge }),
	"diff_fuzz":              countKey("diff_fuzz", func(c *Config) *int { return &c.DiffFuzz }),
	"observed_purity":        boolKey(func(c *Config) *bool { return &c.ObservedPurity }),
	// Zero is off for both of these: a cascade that has not been asked
	// for is not a cascade with a budget of nothing.
	"escalate_after":       countKey("escalate_after", func(c *Config) *int { return &c.EscalateAfter }),
	"advisor_max_consults": countKey("advisor_max_consults", func(c *Config) *int { return &c.AdvisorMaxConsults }),
	"advisor_mode": func(c *Config, val string) (any, error) {
		switch val {
		case "", "off", "always":
			c.AdvisorMode = val
			return val, nil
		}
		return nil, fmt.Errorf("advisor_mode wants off or always")
	},
	"advisor_turn_start_style": func(c *Config, val string) (any, error) {
		switch val {
		case "", "plan", "orient":
			c.AdvisorTurnStartStyle = val
			return val, nil
		}
		return nil, fmt.Errorf("advisor_turn_start_style wants plan or orient")
	},
	"verify_baseline": boolPtr(func(c *Config) **bool { return &c.VerifyBaseline }),

	// The decisions block. Setting any of these on a config that has no
	// block writes one, pinned to the default slug, because a block
	// without a model is refused at the next load.
	"decisions.enabled": decisionsKey(func(d *DecisionsConfig, val string) (any, error) {
		b, err := parseBool(val)
		if err != nil {
			return nil, err
		}
		d.Enabled = &b
		return b, nil
	}),
	"decisions.model": decisionsKey(func(d *DecisionsConfig, val string) (any, error) {
		// Refused here as well as at load, for the reason effort and
		// theme are: a model the loader will reject would be written to
		// the file and refuse to start the next session.
		if !jev.PinnedModel(val) {
			return nil, fmt.Errorf("decisions.model %q is not a pinned version (want jev-X.Y.Z or a ~typesafe/jev-* slug)", val)
		}
		d.Model = val
		return val, nil
	}),
	"decisions.endpoint": decisionsKey(func(d *DecisionsConfig, val string) (any, error) {
		e, ok := FindDecisionEndpoint(val)
		if !ok {
			return nil, fmt.Errorf("decisions.endpoint wants one of %s", strings.Join(DecisionEndpointNames(), ", "))
		}
		d.BaseURL, d.APIKeyEnv = e.BaseURL, e.KeyEnv
		return e.Name, nil
	}),
	"decisions.timeout_ms": decisionsKey(func(d *DecisionsConfig, val string) (any, error) {
		n, err := strconv.Atoi(val)
		if err != nil || n < 0 || n > int(jev.MaxTimeout/time.Millisecond) {
			return nil, fmt.Errorf("decisions.timeout_ms wants 0 for the default, or at most %d", jev.MaxTimeout/time.Millisecond)
		}
		d.TimeoutMs = n
		return n, nil
	}),
	"decisions.triage":          consumerKey("triage"),
	"decisions.intake":          consumerKey("intake"),
	"decisions.intake_route":    consumerKey("route"),
	"decisions.session_outcome": consumerKey("outcome"),

	// Any language may be named: the model is told it as given, and the
	// interface falls back to English when its catalogue lacks it.
	"language": func(c *Config, val string) (any, error) {
		if err := ValidateLanguage(val); err != nil {
			return nil, err
		}
		c.Language = val
		return val, nil
	},
	"screen_reader": boolPtr(func(c *Config) **bool { return &c.ScreenReader }),

	// The voice block, written whole the way the decisions block is.
	"voice.model": voiceKey(nil, func(v *VoiceConfig) *string { return &v.Model }),
	"voice.endpoint": voiceKey(func(s string) error {
		if s == "" || strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://") {
			return nil
		}
		return fmt.Errorf("voice.endpoint wants an http(s) base URL such as https://api.openai.com/v1, or empty for the provider's")
	}, func(v *VoiceConfig) *string { return &v.Endpoint }),
	"voice.api_key_env": voiceKey(func(s string) error {
		if s == "" || envName.MatchString(s) {
			return nil
		}
		return fmt.Errorf("voice.api_key_env wants the name of an environment variable, such as OPENAI_API_KEY")
	}, func(v *VoiceConfig) *string { return &v.APIKeyEnv }),
	"voice.recorder": voiceKey(nil, func(v *VoiceConfig) *string { return &v.Recorder }),

	"theme": func(c *Config, val string) (any, error) {
		// Refused here as well as at load, for the same reason effort is:
		// a name nothing can resolve would be written to the file, ignored
		// for the rest of this session, and then refuse to start at all
		// on the next one (load treats an unknown theme as ErrBadConfig).
		if err := validateTheme(c, val); err != nil {
			return nil, err
		}
		c.Theme = val
		return val, nil
	},

	"sandbox": func(c *Config, val string) (any, error) {
		if val == "" {
			val = "off"
		}
		if err := validateSandbox(val); err != nil {
			return nil, err
		}
		c.Sandbox = val
		return val, nil
	},
}

// SettableKeys are the catalogued config.json keys /config can set. It is
// derived from the table, so a key cannot be advertised without being
// implemented or implemented without being advertised.
var SettableKeys = settableKeys()

func settableKeys() []string {
	out := make([]string, 0, len(setters))
	for k := range setters {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// boolPtr builds a setter for a tri-state *bool field.
func boolPtr(field func(*Config) **bool) setter {
	return func(c *Config, val string) (any, error) {
		b, err := parseBool(val)
		if err != nil {
			return nil, err
		}
		*field(c) = &b
		return b, nil
	}
}

// boolKey builds a setter for a plain bool field: one where off is a
// decision rather than the absence of one, so there is no third state.
func boolKey(field func(*Config) *bool) setter {
	return func(c *Config, val string) (any, error) {
		b, err := parseBool(val)
		if err != nil {
			return nil, err
		}
		*field(c) = b
		return b, nil
	}
}

// countKey builds a setter for an int field where zero means off and a
// negative number means nothing at all.
func countKey(name string, field func(*Config) *int) setter {
	return func(c *Config, val string) (any, error) {
		n, err := strconv.Atoi(val)
		if err != nil || n < 0 {
			return nil, fmt.Errorf("%s wants zero (off) or a positive integer", name)
		}
		*field(c) = n
		return n, nil
	}
}

// decisionsKey builds a setter for a key inside the decisions block,
// creating the block when there is none. A block with no model is
// refused at load, so a block this makes is pinned to the default slug
// -- the same one a picker offers.
func decisionsKey(set func(*DecisionsConfig, string) (any, error)) setter {
	return func(c *Config, val string) (any, error) {
		if c.Decisions == nil {
			c.Decisions = &DecisionsConfig{Model: jev.DefaultModel}
		}
		return set(c.Decisions, val)
	}
}

// consumerKey builds a setter for one decisions consumer flag.
func consumerKey(name string) setter {
	return decisionsKey(func(d *DecisionsConfig, val string) (any, error) {
		b, err := parseBool(val)
		if err != nil {
			return nil, err
		}
		*decisionConsumers[name].field(d) = b
		return b, nil
	})
}

// intKey builds a setter for an int field. positive refuses zero and
// below for the keys where that is not a value but a broken session.
func intKey(name string, positive bool, field func(*Config) *int) setter {
	want := "an integer"
	if positive {
		want = "a positive integer"
	}
	return func(c *Config, val string) (any, error) {
		n, err := strconv.Atoi(val)
		if err != nil || (positive && n <= 0) {
			return nil, fmt.Errorf("%s wants %s", name, want)
		}
		*field(c) = n
		return n, nil
	}
}

// stringKey builds a setter for a string field that takes any value,
// empty included.
// askWaitKey refuses a value ParseAskWait cannot read here as well as at
// load, so a typo is not written to the file to stop the next start.
func askWaitKey(name string, field func(*Config) *string) setter {
	return func(c *Config, val string) (any, error) {
		if _, err := ParseAskWait(name, val, 0); err != nil {
			return nil, err
		}
		*field(c) = val
		return val, nil
	}
}

func stringKey(field func(*Config) *string) setter {
	return func(c *Config, val string) (any, error) {
		*field(c) = val
		return val, nil
	}
}

var envName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// ValidateLanguage accepts a language as a person would name it — "es",
// "pt-BR", "Spanish", "español", "Brazilian Portuguese" — and refuses
// anything else: the value is written into the system prompt, so it is
// held to letters, spaces, hyphens and underscores, at most 40 of them.
func ValidateLanguage(s string) error {
	if utf8.RuneCountInString(s) > 40 {
		return fmt.Errorf("language wants a language name or code, at most 40 characters")
	}
	for _, r := range s {
		if !unicode.IsLetter(r) && r != ' ' && r != '-' && r != '_' {
			return fmt.Errorf("language wants a language name or code such as es, pt-BR or Spanish (empty follows the user)")
		}
	}
	return nil
}

// voiceKey builds a setter for one string of the voice block, writing a
// block when the config has none. check, when set, refuses a value.
func voiceKey(check func(string) error, field func(*VoiceConfig) *string) setter {
	return func(c *Config, val string) (any, error) {
		if check != nil {
			if err := check(val); err != nil {
				return nil, err
			}
		}
		if c.Voice == nil {
			c.Voice = &VoiceConfig{}
		}
		*field(c.Voice) = val
		return val, nil
	}
}

// ApplyKV sets a catalogued config key on cfg and returns the stored value.
func ApplyKV(cfg *Config, key, val string) (any, error) {
	if cfg == nil {
		return nil, fmt.Errorf("no config")
	}
	key = strings.TrimSpace(key)
	set, ok := setters[key]
	if !ok {
		return nil, fmt.Errorf("unknown key %q", key)
	}
	if cfg.Managed.Locked(key) {
		return nil, cfg.Managed.Refusal("changing " + key)
	}
	val = strings.TrimSpace(val)
	if key == "model" && !cfg.Managed.ModelAllowed(val) {
		return nil, cfg.Managed.Refusal(fmt.Sprintf("model %q", val))
	}
	return set(cfg, val)
}

// validateTheme accepts the three modes and any theme the loaded set
// knows. A config that was never loaded has no set to check against, so
// only the modes are certain; the name is then left to load to judge.
func validateTheme(c *Config, name string) error {
	switch name {
	case "", "auto", "dark", "light":
		return nil
	}
	if c.ThemeSet == nil {
		return nil
	}
	if !c.ThemeSet.Has(name) {
		return fmt.Errorf("unknown theme %q (want auto, dark, light or one of: %s)", name, strings.Join(c.ThemeSet.Names(), ", "))
	}
	return nil
}

func parseBool(s string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "1", "true", "on", "yes":
		return true, nil
	case "0", "false", "off", "no":
		return false, nil
	}
	return false, fmt.Errorf("want true/false")
}

// hedgeProviderKey takes empty (off) or the name of a configured
// provider: a hedge onto a provider that does not exist would stop the
// next start with a config error, so it is refused here instead.
func hedgeProviderKey(c *Config, val string) (any, error) {
	if _, ok := c.Providers[val]; val != "" && !ok {
		return nil, fmt.Errorf("hedge_provider wants empty (off) or a configured provider's name, not %q", val)
	}
	c.HedgeProvider = val
	return val, nil
}
