// Package gaming finds cheap, deterministic signs that a change made tests
// go green without implementing the behaviour. It reports; it never gates.
package gaming

import (
	"path/filepath"
	"strings"
)

// Signal is one suspicious pattern in a turn's edits.
type Signal struct {
	File   string
	Kind   string
	Detail string
}

func (s Signal) String() string {
	if s.File == "" {
		return s.Kind + ": " + s.Detail
	}
	return s.File + " — " + s.Detail + " (" + s.Kind + ")"
}

// Delta is one file's added and removed lines this turn.
type Delta struct {
	Path    string
	Added   []string
	Removed []string
	// Created reports that this file was written for the first time in
	// the same turn, so nothing in it predates the turn.
	Created bool
}

// Scan reports at most one signal per (file, kind).
func Scan(deltas []Delta) []Signal {
	var out []Signal
	seen := map[string]bool{}
	add := func(s Signal) {
		k := s.File + "\x00" + s.Kind
		if seen[k] {
			return
		}
		seen[k] = true
		out = append(out, s)
	}
	for _, d := range deltas {
		scanAlwaysTrueEq(d, add)
		scanCallCount(d, add)
		scanRewrittenExpect(d, add)
		scanSkip(d, add)
		scanAssertTrue(d, add)
	}
	return out
}

func scanAlwaysTrueEq(d Delta, add func(Signal)) {
	for i, line := range d.Added {
		t := strings.TrimSpace(line)
		if !strings.Contains(t, "def __eq__") && !strings.Contains(t, "def __ne__") && !strings.Contains(t, "fn eq(&self") {
			continue
		}
		// Only the first statement of the body matters, and the loop
		// breaks on it, so there is nothing more to bound. (It used to
		// carry an empty `if len(...) > 8 {}` and a comment claiming a
		// bound that was never applied.)
		for _, b := range d.Added[i+1:] {
			s := strings.TrimSpace(b)
			if s == "" || strings.HasPrefix(s, "#") || strings.HasPrefix(s, "//") {
				continue
			}
			if s == "return True" || s == "return true" || s == "true" {
				add(Signal{File: d.Path, Kind: "always-true-equality", Detail: "equality override returns true unconditionally"})
			} else if strings.HasPrefix(s, "return isinstance(") {
				add(Signal{File: d.Path, Kind: "always-true-equality", Detail: "equality override is a bare type check"})
			}
			break
		}
	}
}

func scanCallCount(d Delta, add func(Signal)) {
	for _, line := range d.Added {
		t := strings.TrimSpace(line)
		pos := strings.Index(t, "+= 1")
		if pos < 0 {
			continue
		}
		field := strings.TrimSpace(t[:pos])
		if !strings.HasPrefix(field, "self.") && !strings.HasPrefix(field, "this.") {
			continue
		}
		branches, returns := false, false
		for _, b := range d.Added {
			if strings.Contains(b, field) && strings.Contains(b, "if ") {
				branches = true
			}
			if strings.HasPrefix(strings.TrimSpace(b), "return ") {
				returns = true
			}
		}
		if branches && returns {
			add(Signal{File: d.Path, Kind: "call-count-dependent-answer", Detail: "a call counter decides what is returned"})
		}
	}
}

func scanRewrittenExpect(d Delta, add func(Signal)) {
	// A file written for the first time in this turn has no existing
	// expectation to rewrite. A recorded session was accused of gaming
	// for correcting TypeError to ValueError in an example suite it had
	// written four minutes earlier — the assertion was simply wrong.
	if !isTestPath(d.Path) || d.Created {
		return
	}
	for _, line := range d.Removed {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "assert") || strings.HasPrefix(t, "self.assert") || strings.HasPrefix(t, "expect(") || strings.Contains(t, ".should") || strings.HasPrefix(t, "XCTAssert") {
			add(Signal{File: d.Path, Kind: "rewritten-test-expectation", Detail: "an existing test's expectation was changed or removed"})
			return
		}
	}
}

func scanSkip(d Delta, add func(Signal)) {
	if !isTestPath(d.Path) {
		return
	}
	for _, line := range d.Added {
		s := stripStrings(strings.TrimSpace(line))
		if strings.Contains(s, "pytest.mark.skip") || strings.Contains(s, "@unittest.skip") || strings.Contains(s, "t.Skip(") || strings.Contains(s, "xfail") {
			add(Signal{File: d.Path, Kind: "new-test-skip", Detail: "a skip/xfail was added to a test file"})
			return
		}
	}
}

func scanAssertTrue(d Delta, add func(Signal)) {
	if !isTestPath(d.Path) {
		return
	}
	for _, line := range d.Added {
		s := strings.TrimSpace(stripStrings(line))
		if s == "assert True" || s == "assert true" || s == "self.assertTrue(True)" {
			add(Signal{File: d.Path, Kind: "vacuous-assert", Detail: "an assertion that cannot fail was added"})
			return
		}
	}
}

func isTestPath(p string) bool {
	base := filepath.Base(p)
	if strings.HasPrefix(base, "test_") || strings.HasSuffix(base, "_test.go") || strings.HasSuffix(base, "_test.py") || strings.HasSuffix(base, "_spec.js") {
		return true
	}
	return strings.Contains(filepath.ToSlash(p), "/test/") || strings.Contains(filepath.ToSlash(p), "/tests/")
}

func stripStrings(line string) string {
	var b strings.Builder
	var q rune
	esc := false
	for _, c := range line {
		if q != 0 {
			if esc {
				esc = false
				b.WriteByte(' ')
				continue
			}
			if c == '\\' {
				esc = true
				b.WriteByte(' ')
				continue
			}
			if c == q {
				q = 0
			}
			b.WriteByte(' ')
			continue
		}
		if c == '"' || c == '\'' {
			q = c
			b.WriteByte(' ')
			continue
		}
		b.WriteRune(c)
	}
	return b.String()
}
