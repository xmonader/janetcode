package present

import "testing"

func TestPhaseRunning(t *testing.T) {
	if got := PhaseRunning("bash", `{"command":"cd /p && go test ./..."}`, "/p"); got != "bash go test ./..." {
		t.Fatalf("bash = %q", got)
	}
	if got := PhaseRunning("task", `{"prompt":"look around"}`, "/p"); got != "waiting on subagent" {
		t.Fatalf("task = %q", got)
	}
	if got := PhaseRunning("read_file", `{"path":"/p/main.go"}`, "/p"); got != "read_file main.go" {
		t.Fatalf("read_file = %q", got)
	}
	if _, ok := PhaseStreaming("write_file", `{"pa`, "/p"); ok {
		t.Fatal("half a key is not readable yet")
	}
}
