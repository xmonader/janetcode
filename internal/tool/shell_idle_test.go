package tool

import (
	"context"
	"encoding/json"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestIsInlineCheck(t *testing.T) {
	for cmd, want := range map[string]bool{
		"cd /w && PYTHONPATH=. python3 - <<'EOF'\nassert 1\nEOF": true,
		"python - <<EOF\nprint(1)\nEOF":                          true,
		"/usr/bin/python3.12 <<'PY'\nprint(1)\nPY":               true,
		"node <<'JS'\nconsole.log(1)\nJS":                        true,
		"python3 -c 'print(1)'":                                  false,
		"node -e 'x'":                                            false,
		"echo 1 | python3 -":                                     true,
		"echo 1 | python3 script.py":                             false,
		"python3 - <<'EOF' 2>&1\nprint(1)\nEOF":                  true,
		"cat > check.py <<'EOF'\nprint(1)\nEOF":                  false,
		"python3 - <<'EOF' > out.txt\nprint(1)\nEOF":             false,
		"pytest -q":                         false,
		"cargo test":                        false,
		"sh -c 'make test'":                 false,
		"git commit -F - <<'EOF'\nmsg\nEOF": false,
		"python3 script.py":                 false,
	} {
		if got := isInlineCheck(cmd); got != want {
			t.Errorf("isInlineCheck(%q) = %v, want %v", cmd, got, want)
		}
	}
}

func TestSilentInlineCheckIsStopped(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip()
	}
	old := inlineIdle
	inlineIdle = 1500 * time.Millisecond
	defer func() { inlineIdle = old }()
	run := func(b Bash, cmd string, timeoutMS int) (string, time.Duration) {
		in, _ := json.Marshal(map[string]any{"command": cmd, "timeout_ms": timeoutMS})
		t0 := time.Now()
		out, err := b.Run(context.Background(), in)
		if err != nil {
			t.Fatal(err)
		}
		return out, time.Since(t0)
	}
	b := Bash{Root: t.TempDir()}
	out, took := run(b, "sh <<'EOF'\nsleep 20\nEOF", 0)
	if !strings.Contains(out, "inline check stopped") || took > 10*time.Second {
		t.Fatalf("%v %q", took, out)
	}
	// Printing keeps it alive.
	out, _ = run(b, "sh <<'EOF'\nfor i in 1 2 3 4; do echo $i; sleep 0.8; done\nEOF", 0)
	if strings.Contains(out, "stopped") {
		t.Fatal(out)
	}
	// An explicit timeout or the off switch disables the bound.
	out, _ = run(b, "sh <<'EOF'\nsleep 2.5\nEOF", 20000)
	if strings.Contains(out, "stopped") {
		t.Fatal(out)
	}
	out, _ = run(Bash{Root: t.TempDir(), NoIdleBound: true}, "sh <<'EOF'\nsleep 2.5\nEOF", 0)
	if strings.Contains(out, "stopped") {
		t.Fatal(out)
	}
}
