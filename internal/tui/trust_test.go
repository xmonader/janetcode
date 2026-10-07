package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/xmonader/janetcode/internal/hook"
	"github.com/xmonader/janetcode/internal/session"
	"github.com/xmonader/janetcode/internal/trust"
)

func trustProject(t *testing.T, root string) {
	t.Helper()
	for path, body := range map[string]string{
		".janetcode/commands/ship.md": "# Ship it\nship $ARGUMENTS",
		".janetcode/hooks.json":       `{"pre_tool":[{"command":"true"}]}`,
	} {
		p := filepath.Join(root, path)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestTrustFirstRunAsksAndLevelsApply(t *testing.T) {
	opts := fakeOptions(t)
	trustProject(t, opts.Cwd)
	opts.TrustGate = &trust.Gate{Store: trust.Store{Path: filepath.Join(t.TempDir(), "trust.json")}}
	proj, hash, err := hook.LoadProject(opts.Cwd)
	if err != nil {
		t.Fatal(err)
	}
	opts.Hooks = &HookOptions{Project: proj, ProjectHash: hash, Trust: opts.TrustGate.Store}
	h := newHarnessFor(t, opts)

	if _, ok := h.m.custom["/ship"]; ok {
		t.Fatal("an undecided project's custom command was loaded")
	}
	h.m.Init()
	if h.m.picker == nil || !strings.Contains(h.m.picker.title, "Trust") {
		t.Fatal("the first run did not ask about trust")
	}
	h.update(tea.KeyMsg{Type: tea.KeyEsc})

	h.m.trustCommand([]string{"instructions"})
	if _, ok := h.m.custom["/ship"]; !ok {
		t.Fatal("instructions level did not bring in the project's command")
	}
	// A hook is executable: the instructions level refuses the per-file
	// approval rather than quietly running it.
	h.m.hooksCommand([]string{"trust"})
	if opts.Hooks.ProjectTrusted || h.m.opts.Agent.HookRunner() != nil && h.m.opts.Agent.HookRunner().Config.Len() > 0 {
		t.Fatal("/hooks trust enabled a hook under the instructions level")
	}

	h.m.trustCommand([]string{"full"})
	if !opts.Hooks.ProjectTrusted {
		t.Fatal("/trust full did not enable the project's hooks")
	}
	h.m.trustCommand([]string{"untrusted"})
	if opts.Hooks.ProjectTrusted || h.m.opts.Agent.HookRunner().Config.Len() != 0 {
		t.Fatal("/trust untrusted left the project's hooks running")
	}
	if _, ok := h.m.custom["/ship"]; ok {
		t.Fatal("/trust untrusted left the project's command in place")
	}
	var levels []string
	for _, e := range opts.Session.Events() {
		if e.Type == session.TypeTrust {
			levels = append(levels, e.Name)
			if !strings.Contains(e.Reason, opts.Cwd) || !strings.Contains(e.Reason, "hooks=.janetcode/hooks.json") {
				t.Errorf("trust event lacks the project or its files: %q", e.Reason)
			}
		}
	}
	if strings.Join(levels, ",") != "instructions,full,untrusted" {
		t.Fatalf("trust events = %v", levels)
	}
}
