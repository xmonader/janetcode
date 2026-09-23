package tool

import "testing"

func TestCommandFailedReadsTheExitLine(t *testing.T) {
	for _, c := range []struct {
		cmd, out string
		code     int
		failed   bool
	}{
		{"pytest -q", "3 failed, 2 passed\n[exit code 1]", 1, true},
		{"pytest -q", "5 passed\n[exit code 0]", 0, false},
		{"go build ./...", "", 0, false},
		{"grep -n foo a.go", "\n[exit code 1] no lines matched; for this command that is an answer, not a failure", 1, false},
		{"make", "boom\n[exit code 2]", 2, true},
		// The last line wins: a script that printed an old marker first.
		{"sh run.sh", "[exit code 3]\nretrying\n[exit code 0]", 0, false},
	} {
		code, failed := CommandFailed(c.cmd, c.out)
		if code != c.code || failed != c.failed {
			t.Errorf("%q / %q = %d %v, want %d %v", c.cmd, c.out, code, failed, c.code, c.failed)
		}
	}
}

func TestWithoutExitLineDropsOnlyTheTrailingMarker(t *testing.T) {
	for in, want := range map[string]string{
		"5 passed\n[exit code 0]":   "5 passed",
		"3 failed\n[exit code 1]\n": "3 failed",
		"no marker":                 "no marker",
		"[exit code 3]\nstill here": "[exit code 3]\nstill here",
		"\n[exit code 1] no lines matched; for this command that is an answer, not a failure": "no lines matched",
	} {
		if got := WithoutExitLine(in); got != want {
			t.Errorf("%q → %q, want %q", in, got, want)
		}
	}
}
