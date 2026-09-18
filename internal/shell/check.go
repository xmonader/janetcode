package shell

import (
	"encoding/json"
	"path/filepath"
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
func IsCheck(cmd string) bool {
	if cmd == "" {
		return false
	}
	for _, seg := range Segments(cmd) {
		if segmentChecks(seg) {
			return true
		}
	}
	return false
}

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

// notCheckHead lists the commands whose arguments are paths, not work: a
// directory named `tests` or a project called `mutest` made `ls -la
// ~/tmp/mutest` and `mkdir -p ~/tmp/mutest/tests` count as checks on a
// real session, and the verdict then reported five checks where two had
// run. The head decides, because these utilities never check anything
// whatever their arguments say.
var notCheckHead = map[string]bool{
	"ls": true, "cat": true, "cd": true, "mkdir": true, "rmdir": true,
	"rm": true, "cp": true, "mv": true, "touch": true, "echo": true,
	"printf": true, "head": true, "tail": true, "less": true, "more": true,
	"pwd": true, "which": true, "find": true, "grep": true, "rg": true,
	"sed": true, "awk": true, "chmod": true, "chown": true, "ln": true,
	"wc": true, "sort": true, "uniq": true, "tr": true, "cut": true,
	"stat": true, "du": true, "df": true, "tree": true, "readlink": true,
	"basename": true, "dirname": true, "mktemp": true, "tar": true,
	"unzip": true, "curl": true, "wget": true, "export": true, "source": true,
}

// headWrappers prefix a command without being it: the head to judge is
// what follows them.
var headWrappers = map[string]bool{"sudo": true, "doas": true, "time": true, "env": true, "nice": true, "nohup": true, "command": true, "exec": true}

// segmentChecks reports whether one pipeline segment checks something.
func segmentChecks(seg string) bool {
	head := ""
	for _, w := range Words(seg) {
		if strings.Contains(w, "=") && !strings.HasPrefix(w, "-") && head == "" {
			continue // leading VAR=value assignment
		}
		if strings.HasPrefix(w, "-") {
			continue
		}
		head = w
		if headWrappers[head] {
			head = ""
			continue
		}
		break
	}
	if head == "" {
		return false
	}
	if notCheckHead[strings.TrimPrefix(filepath.Base(head), "./")] {
		return false
	}
	return checkRe.MatchString(seg)
}
