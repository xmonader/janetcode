package config

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

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
func stringKey(field func(*Config) *string) setter {
	return func(c *Config, val string) (any, error) {
		*field(c) = val
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
	return set(cfg, strings.TrimSpace(val))
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
