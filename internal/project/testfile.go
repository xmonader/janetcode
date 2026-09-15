package project

import (
	"path/filepath"
	"regexp"
)

// testFileRe matches a slash-separated path that holds tests, across the
// languages the harness deals with: a tests/, test/, spec/, testdata/ or
// __tests__/ directory; a _test, _spec, .test or .spec file name; a
// test_ prefix; pytest's conftest.py; a JUnit TestFoo.java.
var testFileRe = regexp.MustCompile(`(?i)((^|/)(tests?|spec|testdata|__tests__)/|_test\.[a-z]+$|_spec\.[a-z]+$|(^|/)test_[^/]+$|\.test\.[^/]+$|\.spec\.[^/]+$|(^|/)conftest\.py$|Test[A-Z][^/]*\.java$)`)

// IsTestFile reports whether a path holds tests rather than code. It is
// the one definition shared by the turn verdict (a green produced by a
// suite edited in the same turn is weaker evidence than one against tests
// that were already there), internal/impact (which files among a
// symbol's referencers are its tests), accept mode (which files the
// writing phase may touch) and internal/mutate (which files are not
// worth mutating).
func IsTestFile(path string) bool {
	return path != "" && testFileRe.MatchString(filepath.ToSlash(path))
}
