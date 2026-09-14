package verify

import (
	"errors"
	"strings"
)

// Usage is the argument form /verify accepts.
const Usage = "usage: /verify [set <command>|add <command>|mutate <test command>|mutate off|clear|trust]"

// CleanCommand takes a command typed or stated with wrapping — `/verify
// set "go test ./..."`, a model's `go test ./...` in backticks — down to
// the literal shell command, or "" when nothing usable is left. Run as
// typed, the quoted form asks the shell for a program named
// "go test ./..." and fails with exit 127; the backticked form runs the
// command inside a command substitution and executes its output. One
// layer of wrapping comes off at a time, only when the wrapper spans the
// whole command and appears nowhere inside it, so a command whose own
// quotes open and close it (`"a" && "b"`) is left alone. A command left
// spanning more than one line is rejected rather than run.
func CleanCommand(s string) string {
	s = strings.TrimSpace(s)
	for {
		trimmed := strings.TrimSpace(stripWrap(s))
		if trimmed == s {
			break
		}
		s = trimmed
	}
	if s == "" || strings.ContainsAny(s, "\n\r") {
		return ""
	}
	return s
}

// stripWrap removes one wrapper spanning the whole of s: triple or
// single backticks, or a matching pair of double or single quotes that
// does not occur again inside.
func stripWrap(s string) string {
	for _, wrap := range []string{"```", "`", `"`, "'"} {
		if len(s) < 2*len(wrap) || !strings.HasPrefix(s, wrap) || !strings.HasSuffix(s, wrap) {
			continue
		}
		inner := s[len(wrap) : len(s)-len(wrap)]
		if strings.Contains(inner, wrap) {
			return s
		}
		return inner
	}
	return s
}

// Edit applies one /verify subcommand that changes the project file to c
// and returns the new config with a note saying what changed: set
// replaces the commands with one, add appends one, mutate sets or (with
// "off" or nothing) clears the mutation test command, and clear empties
// the file. Commands are cleaned with CleanCommand first. c itself is not
// modified.
func Edit(c Config, verb, arg string) (Config, string, error) {
	c.Commands = append([]string(nil), c.Commands...)
	switch verb {
	case "set", "add":
		cmd := CleanCommand(arg)
		if cmd == "" {
			return c, "", errors.New("usage: /verify " + verb + " <command>")
		}
		if verb == "set" {
			c.Commands = []string{cmd}
			return c, "set: " + cmd, nil
		}
		c.Commands = append(c.Commands, cmd)
		return c, "added: " + cmd, nil
	case "mutate":
		cmd := strings.TrimSpace(arg)
		if cmd == "" || cmd == "off" {
			c.Mutate = ""
			return c, "mutation testing off", nil
		}
		if cmd = CleanCommand(cmd); cmd == "" {
			return c, "", errors.New("usage: /verify mutate <test command>")
		}
		c.Mutate = cmd
		return c, "mutation testing with: " + cmd + " (small mutations of changed lines must make it fail)", nil
	case "clear":
		return Config{}, "cleared " + ProjectFile, nil
	}
	return c, "", errors.New(Usage)
}
