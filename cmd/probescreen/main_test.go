package main

import (
	"path/filepath"
	"testing"

	"github.com/xmonader/janetcode/internal/session"
)

// screen finds the session logs of a campaign's runs and lists each run's
// probes; a run with none is still counted.
func TestScreenCountsRunsAndProbes(t *testing.T) {
	campaign := t.TempDir()
	write := func(run string, events ...session.Event) {
		s, err := session.New(filepath.Join(campaign, run, "janetcode-sessions"), "/w", "p", "m")
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range events {
			if _, err := s.Append(e); err != nil {
				t.Fatal(err)
			}
		}
	}
	edit := session.Event{Type: session.TypeToolResult, Turn: 1, CallID: "e", Name: "edit_file", Arguments: `{"path":"m.py"}`, Output: "ok"}
	probe := session.Event{Type: session.TypeToolResult, Turn: 1, CallID: "p", Name: "bash", Arguments: `{"command":"cd /w && python3 -c \"import m; assert m.f()\""}`, Output: "ok"}
	write("a-task-s1", edit, probe)
	write("a-task-s2", edit)
	runs, err := screen(campaign)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 2 {
		t.Fatalf("%d runs, want 2", len(runs))
	}
	if runs[0].name != "a-task-s1" || len(runs[0].probes) != 1 || runs[0].probes[0].Key != `python3 -c "import m; assert m.f()"` {
		t.Fatalf("run 1 = %+v", runs[0])
	}
	if len(runs[1].probes) != 0 {
		t.Fatalf("run 2 probes = %+v", runs[1].probes)
	}
}
