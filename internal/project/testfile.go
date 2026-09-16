package project

import (
	"path/filepath"
	"regexp"
	"strings"
)

// testFileRe matches a slash-separated path that holds tests, across the
// languages the harness deals with. It is written as one alternation per
// convention so that each can carry its own case sensitivity: a Java
// TestFoo.java is a convention about capitals, and folding case there
// would make Contestant.java a test file.
var testFileRe = regexp.MustCompile(strings.Join([]string{
	// a tests/, test/, spec/, testdata/ or __tests__/ directory
	`(^|/)(?i:tests?|spec|testdata|__tests__)/`,
	// x_test.go, x_spec.rb
	`(?i:_test|_spec)\.[a-zA-Z0-9]+$`,
	// test_x.py
	`(^|/)(?i:test_)[^/]+$`,
	// a.test.js, a.spec.ts, a.test.d.ts
	`(?i:\.(test|spec)\.)[^/]+$`,
	// pytest's conftest.py
	`(^|/)conftest\.py$`,
	// JUnit's TestFoo.java and FooTest.java / FooTests.java
	`(^|/)(Test[A-Z][^/]*|[A-Za-z0-9_$]+Tests?)\.java$`,
}, "|"))

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
