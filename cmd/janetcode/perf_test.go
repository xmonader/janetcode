package main

import (
	"os/exec"
	"path/filepath"
	"sort"
	"testing"
	"time"
)

// Startup is a budget, not a hope: everything janetcode does before the
// first frame — flags, config, provider, session, the agent with its
// tools, the project's files — must stay under it, or a new feature that
// loads something eagerly shows up here instead of as "it feels slow".
// The budget is several times what it takes today, so a busy CI machine
// passes and a real regression does not.
const startupBudget = 100 * time.Millisecond

func TestStartupBudget(t *testing.T) {
	if raceEnabled {
		t.Skip("the race detector slows everything down several times over")
	}
	home := t.TempDir()
	work := filepath.Join(home, "w")
	if err := exec.Command("git", "init", "-q", work).Run(); err != nil {
		t.Skip("no git")
	}
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, ".local", "state"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	t.Setenv("OPENAI_API_KEY", "test-key")
	t.Setenv("OPENAI_BASE_URL", "http://127.0.0.1:9/v1")
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("JANETCODE_MODEL", "test-model")
	t.Chdir(work)

	var runs []time.Duration
	for range 5 {
		s := &startup{args: []string{"--provider", "openai"}}
		start := time.Now()
		for _, stage := range startupStages {
			done, err := stage.fn(s)
			if err != nil {
				s.close()
				t.Fatalf("%s: %v", stage.name, err)
			}
			if done {
				s.close()
				t.Fatalf("%s ended the run before the interface would start", stage.name)
			}
		}
		runs = append(runs, time.Since(start))
		s.close()
	}
	sort.Slice(runs, func(i, j int) bool { return runs[i] < runs[j] })
	median := runs[len(runs)/2]
	t.Logf("startup to the first frame: median %v of %v", median, runs)
	if median > startupBudget {
		t.Errorf("startup took %v (median of 5), over the %v budget", median, startupBudget)
	}
}

// raceEnabled is set by race_on_test.go.
var raceEnabled bool
