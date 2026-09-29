package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPluginCommandInstallsAndLists(t *testing.T) {
	h := newHarness(t)
	h.m.opts.ConfigDir = t.TempDir()
	src := filepath.Join(t.TempDir(), "kit")
	os.MkdirAll(filepath.Join(src, "commands"), 0o755)
	os.WriteFile(filepath.Join(src, "commands", "lint.md"), []byte("Lint"), 0o644)

	h.keys("/plugin")
	h.enter()
	if v := plain(h.m.View()); !strings.Contains(v, "no plugins installed") {
		t.Fatalf("view:\n%s", v)
	}
	h.keys("/plugin install " + src)
	h.enter()
	h.pump(func() bool { return strings.Contains(plain(h.m.View()), "installed kit") })
	h.keys("/plugin")
	h.enter()
	if v := plain(h.m.View()); !strings.Contains(v, "✓ kit") || !strings.Contains(v, "1 command") {
		t.Fatalf("listing:\n%s", v)
	}
}
