package tool

import "regexp"

// testFileRe matches a path that holds tests, across the languages the
// harness deals with. Shared by the turn verdict (a green produced by a
// suite edited in the same turn is weaker evidence than one against tests
// that were already there) and by internal/impact (which files among a
// symbol's referencers are its tests).
var testFileRe = regexp.MustCompile(`(?i)((^|/)tests?/|(^|/)spec/|_test\.[a-z]+$|_spec\.[a-z]+$|(^|/)test_[^/]+$|\.test\.[a-z]+$|\.spec\.[a-z]+$|(^|/)conftest\.py$|Test[A-Z][^/]*\.java$)`)

// IsTestFile reports whether a path holds tests.
func IsTestFile(path string) bool { return path != "" && testFileRe.MatchString(path) }
