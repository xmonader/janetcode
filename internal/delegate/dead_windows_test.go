package delegate

import "testing"

// assertDead needs signal 0 to probe a pid, which Windows has not got; the
// process-group tests that call it are POSIX behaviour.
func assertDead(t *testing.T, pid int) {
	t.Helper()
	t.Skip("process-group probing is POSIX only")
}
