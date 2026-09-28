package main

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/xmonader/janetcode/internal/config"
	"github.com/xmonader/janetcode/internal/headless"
)

// pick reports which mode the table selects for a set of flags, without
// running it.
func pick(o *options) string {
	for _, m := range sessionModes {
		if m.when(o) {
			return m.name
		}
	}
	return ""
}

// The table is the specification of which flag wins, so it is checked
// directly: nested ifs used to encode this and nothing held them to it.
func TestSessionModeSelection(t *testing.T) {
	for _, c := range []struct {
		name string
		o    *options
		want string
	}{
		{"nothing given opens the terminal", &options{}, "tui"},
		{"a prompt is headless", &options{promptText: "hi"}, "headless"},
		{"--watch is headless too", &options{watchCmd: "go test ./..."}, "headless"},
		{"--web serves a browser", &options{web: true}, "web"},
		{"--acp serves an editor", &options{acpMode: true}, "acp"},
		{"--recipe replays tool calls", &options{recipeName: "ship"}, "recipe"},
		{"--inspect answers and exits", &options{inspectWhat: "config"}, "inspect"},
		// Precedence: --inspect is the one that answers a question about
		// the run itself, so it beats every mode that would start one.
		{"--inspect beats a prompt", &options{inspectWhat: "config", promptText: "hi"}, "inspect"},
		{"--inspect beats a recipe", &options{inspectWhat: "config", recipeName: "ship"}, "inspect"},
		{"--recipe beats the terminal", &options{recipeName: "ship", web: true}, "recipe"},
		{"--acp beats a prompt", &options{acpMode: true, promptText: "hi"}, "acp"},
	} {
		if got := pick(c.o); got != c.want {
			t.Errorf("%s: picked %q, want %q", c.name, got, c.want)
		}
	}
}

// The table must be total and unambiguous: every row named, every row
// able to run, and a last row that catches everything else.
func TestSessionModeTableIsWellFormed(t *testing.T) {
	seen := map[string]bool{}
	for i, m := range sessionModes {
		if m.name == "" {
			t.Errorf("mode %d has no name", i)
		}
		if seen[m.name] {
			t.Errorf("two modes are both called %q", m.name)
		}
		seen[m.name] = true
		if m.when == nil || m.run == nil {
			t.Errorf("mode %q is not fully wired", m.name)
		}
	}
	last := sessionModes[len(sessionModes)-1]
	if !last.when(&options{}) {
		t.Fatalf("the last mode (%q) does not catch a bare invocation; dispatch would fall through", last.name)
	}
}

// The two one-shot modes answer a question and exit. Opening the hint and
// feature-usage records for them would count a flag as a feature reached
// for in a session nobody is having.
func TestOnlySessionModesOpenTheUserRecords(t *testing.T) {
	want := map[string]bool{"inspect": false, "recipe": false, "acp": true, "headless": true, "web": true, "tui": true}
	for _, m := range sessionModes {
		w, ok := want[m.name]
		if !ok {
			t.Fatalf("mode %q is new: say whether it opens the hint and usage records", m.name)
		}
		if m.hints != w {
			t.Errorf("mode %q: hints = %v, want %v", m.name, m.hints, w)
		}
	}
}

// A malformed --allow or --deny rule is a usage error (exit 42), like any
// other bad flag value, not a failed run.
func TestBadPermissionFlagIsAUsageError(t *testing.T) {
	for _, o := range []*options{{allow: "bash(go test"}, {deny: "edit_file,rm -rf"}} {
		r := &root{cfg: &config.Config{}, opts: o}
		_, _, _, _, err := r.permissionRules(t.TempDir())
		if !errors.Is(err, headless.ErrBadInput) {
			t.Errorf("%+v: err = %v, want a usage error", *o, err)
		}
	}
}

// The combinations checkModes refuses are refused before any mode runs:
// the modes that print something and exit (--session-profile,
// --decisions-replay, --decisions-annotate) ran ahead of the checks, so
// their rows could never fire and -p, or the other run, was silently
// dropped.
func TestConflictingEarlyModesAreRefused(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	missing := filepath.Join(home, "nothing-here")
	for _, args := range [][]string{
		{"--session-profile", missing, "-p", "hi"},
		{"--decisions-replay", missing, "-p", "hi"},
		{"--decisions-annotate", missing, "-p", "hi"},
		{"--decisions-annotate", missing, "--decisions-replay", missing},
	} {
		if err := run(args); !errors.Is(err, headless.ErrBadInput) {
			t.Errorf("%v: err = %v, want a usage error", args, err)
		}
	}
	// --doctor takes --output-format json without -p.
	if err := checkModes(&options{runDoctor: true, outFormat: "json"}, "", headless.FormatJSON); err != nil {
		t.Errorf("--doctor --output-format json refused: %v", err)
	}
}

// The startup sequence is the other half of the same story: named steps,
// in one order, each able to end the run.
func TestStartupStagesAreWellFormed(t *testing.T) {
	seen := map[string]bool{}
	for i, st := range startupStages {
		if st.name == "" {
			t.Errorf("stage %d has no name", i)
		}
		if seen[st.name] {
			t.Errorf("two stages are both called %q", st.name)
		}
		seen[st.name] = true
		if st.fn == nil {
			t.Errorf("stage %q has no function", st.name)
		}
	}
	if got := startupStages[len(startupStages)-1].name; got != "composition root" {
		t.Fatalf("the last stage is %q; dispatch runs on what it builds", got)
	}
}

// Cleanups run newest first, the order the defers they replaced ran in:
// the agent closes before the worktree is reported, which closes before
// the price refresh is stopped.
func TestStartupCleanupsRunInReverse(t *testing.T) {
	var order []string
	s := &startup{}
	s.onClose(func() { order = append(order, "rates") })
	s.onClose(func() { order = append(order, "worktree") })
	s.onClose(func() { order = append(order, "root") })
	s.close()
	if len(order) != 3 || order[0] != "root" || order[1] != "worktree" || order[2] != "rates" {
		t.Fatalf("cleanup order = %v", order)
	}
	// Closing twice runs nothing twice.
	s.close()
	if len(order) != 3 {
		t.Fatalf("a second close re-ran cleanups: %v", order)
	}
}
