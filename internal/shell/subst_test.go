package shell

import (
	"reflect"
	"testing"
)

// SplitSubstitutions stands each outermost substitution in for one word
// and hands back what it runs, leaving single-quoted text alone.
func TestSplitSubstitutions(t *testing.T) {
	for _, tc := range []struct {
		in, outer string
		bodies    []string
	}{
		{"go test ./...", "go test ./...", nil},
		{"go test $(curl x)", "go test _", []string{"curl x"}},
		{"go test `curl x` -v", "go test _ -v", []string{"curl x"}},
		{`go test "$(curl x)"`, `go test "_"`, []string{"curl x"}},
		{"diff <(a) >(b)", "diff _ _", []string{"a", "b"}},
		{"go test $(go list $(curl x))", "go test _", []string{"go list $(curl x)"}},
		{"echo '$(not run)'", "echo '$(not run)'", nil},
		{"echo $((1+2))", "echo $((1+2))", nil},
		{"go test $(curl x", "go test _", []string{"curl x"}},
	} {
		outer, bodies := SplitSubstitutions(tc.in)
		if outer != tc.outer || !reflect.DeepEqual(bodies, tc.bodies) {
			t.Errorf("SplitSubstitutions(%q) = %q, %q; want %q, %q", tc.in, outer, bodies, tc.outer, tc.bodies)
		}
	}
}
