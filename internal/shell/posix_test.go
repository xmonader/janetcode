package shell

import (
	"runtime"
	"testing"
)

// posixOnly skips a test whose cases are written with POSIX paths (/tmp,
// /work/repo) and bash semantics. On Windows those paths are rooted on
// the current drive and the shell is PowerShell, so the expectations do
// not hold; safety_windows_test.go states the Windows ones.
func posixOnly(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("cases written with POSIX paths and bash semantics")
	}
}
