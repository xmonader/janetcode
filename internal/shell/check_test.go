package shell

import "testing"

// These commands are from two recorded sessions (a tcp proxy and a
// testing library called "mutest"). Every one of them was counted as a
// check because "test" appears somewhere in a path, which inflated the
// turn verdict's check count from two to five.
func TestIsCheckJudgesTheHeadNotThePath(t *testing.T) {
	for cmd, want := range map[string]bool{
		"mkdir -p /home/u/tmp/mutest/tests":         false,
		"ls -la /home/u/tmp/mutest":                 false,
		"cat tests/test_examples.py":                false,
		"grep -rn pytest tests/":                    false,
		"find . -name '*_test.go'":                  false,
		"cd /tmp/atcpproxy && python3 -m pytest -v": true,
		".venv/bin/mutest tests/":                   true,
		"python test_ini.py":                        true,
		"go test ./...":                             true,
		"make check":                                true,
		"CGO_ENABLED=0 go test ./internal/shell":    true,
		"sudo make test":                            true,
		"ls | xargs pytest":                         true,
		"npm run test:unit":                         true,
		"./run_tests.sh":                            true,
		"git status":                                false,
	} {
		if got := IsCheck(cmd); got != want {
			t.Errorf("IsCheck(%q) = %v, want %v", cmd, got, want)
		}
	}
}
