package shell

import (
	"encoding/json"
	"regexp"
	"strings"
)

// This file recognises what a shell command is for, from its text alone.
// Several parts of the harness need the same answer — the verdict pass,
// the step router, accept mode's phase gate — and they have to agree: a
// command counted as a check in one place and not in another is a bug
// nobody can see from either side.

// checkRe recognises a command that checks something. It is looser than
// the trajectory features' pattern on purpose: that one matches whole
// words, so `python test_ini.py` — a real test run — slipped past it,
// because "_" is a word character and \btest\b never reached the "test"
// in "test_ini". A detector that misses a check accuses the model of not
// running one, which is worse than counting a stray command.
var checkRe = regexp.MustCompile(`(?i)(test|spec|pytest|jest|vitest|mocha|rspec|junit|check|verify|assert|vet\b|lint|tsc\b|mypy|ruff|clippy|build|make\b|ctest|tox|nox|gradle|mvn\b)`)

// IsCheck reports whether a shell command is checking something.
func IsCheck(cmd string) bool { return cmd != "" && checkRe.MatchString(cmd) }

// testRunRe recognises a command that runs tests, as opposed to one that
// only builds or lints: what a passing verify gate, which keeps no output,
// is taken to have exercised.
var testRunRe = regexp.MustCompile(`(?i)\b(test|tests|pytest|jest|vitest|mocha|rspec|ctest|tox|nox)\b`)

// IsTestRun reports whether a command runs tests, as opposed to one that
// only builds or lints.
func IsTestRun(cmd string) bool { return testRunRe.MatchString(cmd) }

// CommandOf is the shell command of a bash tool call, given that call's
// JSON arguments, for comparing one run with another.
func CommandOf(arguments string) string {
	var in struct {
		Command string `json:"command"`
	}
	if json.Unmarshal([]byte(arguments), &in) != nil {
		return ""
	}
	return strings.TrimSpace(in.Command)
}
