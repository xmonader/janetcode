package present

import (
	"testing"
	"time"
)

func TestEndOf(t *testing.T) {
	red := EndOf("bash", `{"command":"pytest -q"}`, "3 failed, 2 passed\n[exit code 1]", false, 1200*time.Millisecond)
	if !red.Failed || red.Output != "3 failed, 2 passed" || red.Label != "exit 1 · 1.2s" {
		t.Fatalf("red run = %+v", red)
	}
	none := EndOf("bash", `{"command":"grep -n nope a.go"}`, "\n[exit code 1] no lines matched; for this command that is an answer, not a failure", false, 0)
	if none.Failed || none.Output != "no lines matched" {
		t.Fatalf("a search that found nothing is not a failure: %+v", none)
	}
	green := EndOf("bash", `{"command":"go test ./..."}`, "ok\n[exit code 0]", false, 0)
	if green.Failed || green.Label != "" {
		t.Fatalf("green run = %+v", green)
	}
	other := EndOf("read_file", `{"path":"a"}`, "[exit code 1]", true, time.Second)
	if !other.Failed || other.Output != "[exit code 1]" || other.Label != "1.0s" {
		t.Fatalf("a non-bash tool keeps its output and its own verdict: %+v", other)
	}
}
