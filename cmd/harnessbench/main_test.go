package main

import "testing"

// -h asks for help and gets it: not a failure. A flag the command does
// not have is a usage error, status 2, not the 1 of a run that failed.
func TestExitCodeOfTheCommandLine(t *testing.T) {
	for _, c := range []struct {
		args []string
		want int
	}{
		{[]string{"-h"}, 0},
		{[]string{"--help"}, 0},
		{[]string{"--no-such-flag"}, 2},
		{[]string{"-timeout", "soon"}, 2},
	} {
		if got := statusOf(run(c.args)); got != c.want {
			t.Errorf("harnessbench %v exits %d, want %d", c.args, got, c.want)
		}
	}
}
