package config

import (
	"fmt"
	"strings"
)

// blind_tester turns on the blind tester of a headless run: at the end of
// a green turn, a model that sees only the task writes checks for the
// rules it states, and the turn goes back once with the rules whose checks
// failed (internal/agent/blindtest.go). The value names the model slot it
// runs on: "cheap", "strong", "advisor" or "main", or "on" for the cheap
// slot; an unset slot is the session's own model, as everywhere. Empty or
// "off" is off. Off by default: it is a model call nobody asked for, it
// runs model-written commands, and it can send a finished turn back.

// BlindTesterModel is the model the blind tester runs on, and whether it
// is on. A value that names no slot is off: the key fails closed.
func (c *Config) BlindTesterModel() (string, bool) {
	slot, ok := blindTesterSlot(c.BlindTester)
	if !ok {
		return "", false
	}
	_, model := c.Slot(slot)
	return model, true
}

// blindTesterSlot reads a blind_tester value as a slot name.
func blindTesterSlot(v string) (string, bool) {
	switch v = strings.ToLower(strings.TrimSpace(v)); v {
	case "on", "true", "cheap":
		return "cheap", true
	case "strong", "advisor", "main":
		return v, true
	case "model":
		return "main", true
	}
	return "", false
}

// blindTesterKey is /config's setter for blind_tester.
func blindTesterKey(c *Config, val string) (any, error) {
	v := strings.ToLower(strings.TrimSpace(val))
	if v == "" || v == "off" || v == "false" {
		c.BlindTester = ""
		return "off", nil
	}
	if _, ok := blindTesterSlot(v); !ok {
		return nil, fmt.Errorf("blind_tester: want off, on, cheap, strong, advisor or main, not %q", val)
	}
	c.BlindTester = v
	return v, nil
}
