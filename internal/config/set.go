package config

import (
	"fmt"
	"strconv"
	"strings"
)

// SettableKeys are the catalogued config.json keys /config can set.
var SettableKeys = []string{
	"thinking", "show_thinking", "lean_tools", "intent", "auto_approve",
	"compact_at", "max_tokens", "max_iterations", "model", "effort",
	"theme", "hints", "memory",
}

// ApplyKV sets a catalogued config key on cfg and returns the stored value.
func ApplyKV(cfg *Config, key, val string) (any, error) {
	if cfg == nil {
		return nil, fmt.Errorf("no config")
	}
	key = strings.TrimSpace(key)
	val = strings.TrimSpace(val)
	switch key {
	case "thinking":
		b, err := parseBool(val)
		if err != nil {
			return nil, err
		}
		cfg.Thinking = &b
		return b, nil
	case "show_thinking":
		b, err := parseBool(val)
		if err != nil {
			return nil, err
		}
		cfg.ShowThinking = &b
		return b, nil
	case "lean_tools":
		b, err := parseBool(val)
		if err != nil {
			return nil, err
		}
		cfg.LeanTools_ = &b
		return b, nil
	case "intent":
		b, err := parseBool(val)
		if err != nil {
			return nil, err
		}
		cfg.Intent = &b
		return b, nil
	case "auto_approve":
		b, err := parseBool(val)
		if err != nil {
			return nil, err
		}
		cfg.AutoApprove = b
		return b, nil
	case "hints":
		b, err := parseBool(val)
		if err != nil {
			return nil, err
		}
		cfg.Hints = &b
		return b, nil
	case "memory":
		b, err := parseBool(val)
		if err != nil {
			return nil, err
		}
		cfg.Memory = &b
		return b, nil
	case "compact_at":
		n, err := strconv.Atoi(val)
		if err != nil {
			return nil, fmt.Errorf("compact_at wants an integer")
		}
		cfg.CompactAt = n
		return n, nil
	case "max_tokens":
		n, err := strconv.Atoi(val)
		if err != nil || n <= 0 {
			return nil, fmt.Errorf("max_tokens wants a positive integer")
		}
		cfg.MaxTokens = n
		return n, nil
	case "max_iterations":
		n, err := strconv.Atoi(val)
		if err != nil || n <= 0 {
			return nil, fmt.Errorf("max_iterations wants a positive integer")
		}
		cfg.MaxIterations = n
		return n, nil
	case "model":
		if val == "" {
			return nil, fmt.Errorf("model is empty")
		}
		cfg.Model = val
		return val, nil
	case "effort":
		cfg.Effort = val
		return val, nil
	case "theme":
		cfg.Theme = val
		return val, nil
	default:
		return nil, fmt.Errorf("unknown key %q", key)
	}
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
