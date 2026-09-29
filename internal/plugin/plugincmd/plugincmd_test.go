package plugincmd

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/cmdresult"
	"github.com/xmonader/janetcode/internal/plugin"
)

func write(t *testing.T, path, body string) {
	t.Helper()
	os.MkdirAll(filepath.Dir(path), 0o755)
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func run(t *testing.T, cfg string, args ...string) cmdresult.Result {
	t.Helper()
	res, err := Run(context.Background(), cfg, args)
	if err != nil {
		t.Fatalf("/plugin %s: %v", strings.Join(args, " "), err)
	}
	return res
}

func text(r cmdresult.Result) string {
	return strings.Join(append(append([]string{r.Sheet, r.Error}, r.Notes...), r.Tips...), "\n")
}

func TestPluginCommandFlow(t *testing.T) {
	cfg := t.TempDir()
	src := filepath.Join(t.TempDir(), "kit")
	write(t, filepath.Join(src, ".claude-plugin", "plugin.json"), `{"name":"kit","version":"0.2.0","description":"a kit"}`)
	write(t, filepath.Join(src, "commands", "lint.md"), "Lint it")
	write(t, filepath.Join(src, "hooks", "hooks.json"), `{"hooks":{"PostToolUse":[{"matcher":"Write","hooks":[{"type":"command","command":"${CLAUDE_PLUGIN_ROOT}/fmt.sh"}]}]}}`)
	write(t, filepath.Join(src, ".mcp.json"), `{"mcpServers":{"lint":{"command":"node","args":["${CLAUDE_PLUGIN_ROOT}/server.js"]}}}`)

	if got := text(run(t, cfg)); !strings.Contains(got, "no plugins installed") {
		t.Fatalf("empty list = %q", got)
	}
	got := text(run(t, cfg, "install", src))
	for _, want := range []string{"installed kit 0.2.0", "1 command", "1 hook", "1 MCP server", "/plugin trust kit", "fmt.sh", "server.js", "NOT trusted"} {
		if !strings.Contains(got, want) {
			t.Fatalf("install output lacks %q:\n%s", want, got)
		}
	}
	if got := text(run(t, cfg, "list")); !strings.Contains(got, "✓ kit 0.2.0 · a kit") || !strings.Contains(got, "not trusted") {
		t.Fatalf("list = %q", got)
	}
	if got := text(run(t, cfg, "trust", "kit")); !strings.Contains(got, "trusted kit's hooks and MCP servers") {
		t.Fatalf("trust = %q", got)
	}
	if got := text(run(t, cfg, "list")); strings.Contains(got, "not trusted") {
		t.Fatalf("list after trust = %q", got)
	}
	if got := text(run(t, cfg, "disable", "kit")); !strings.Contains(got, "disabled kit") {
		t.Fatalf("disable = %q", got)
	}
	if got := text(run(t, cfg, "list")); !strings.Contains(got, "○ kit") {
		t.Fatalf("list after disable = %q", got)
	}
	if got := text(run(t, cfg, "remove", "kit")); !strings.Contains(got, "removed kit") {
		t.Fatalf("remove = %q", got)
	}
	if _, err := Run(context.Background(), cfg, []string{"show", "kit"}); err == nil {
		t.Fatal("show of a removed plugin succeeded")
	}
	if _, err := Run(context.Background(), cfg, []string{"frobnicate"}); err == nil || !strings.Contains(err.Error(), "usage") {
		t.Fatalf("unknown verb = %v", err)
	}
}

func TestMarketplaceBrowse(t *testing.T) {
	cfg := t.TempDir()
	mk := filepath.Join(t.TempDir(), "mk")
	write(t, filepath.Join(mk, "plugins", "tiny", "commands", "hi.md"), "hi")
	write(t, filepath.Join(mk, ".claude-plugin", "marketplace.json"), `{"name":"team","plugins":[{"name":"tiny","source":"./plugins/tiny","description":"says hi"}]}`)
	got := text(run(t, cfg, "marketplace", "add", mk))
	if !strings.Contains(got, "added marketplace team · 1 plugin") || !strings.Contains(got, "/plugin install tiny@team") {
		t.Fatalf("marketplace add = %q", got)
	}
	run(t, cfg, "install", "tiny@team")
	if got := text(run(t, cfg, "browse")); !strings.Contains(got, "tiny · says hi (installed)") {
		t.Fatalf("browse = %q", got)
	}
	if got := text(run(t, cfg, "marketplace", "list")); !strings.Contains(got, "team · 1 plugin") {
		t.Fatalf("marketplace list = %q", got)
	}
}

// Live: a real marketplace published for Claude Code, over the network.
// JANETCODE_PLUGIN_LIVE=<repo url> [JANETCODE_PLUGIN_LIVE_CFG=<dir>] runs it.
func TestLiveMarketplace(t *testing.T) {
	repo := os.Getenv("JANETCODE_PLUGIN_LIVE")
	if repo == "" {
		t.Skip("set JANETCODE_PLUGIN_LIVE to a marketplace repository URL")
	}
	cfg := os.Getenv("JANETCODE_PLUGIN_LIVE_CFG")
	if cfg == "" {
		cfg = t.TempDir()
	}
	add := run(t, cfg, "marketplace", "add", repo)
	t.Logf("%s", text(add))
	ms, idx, _ := plugin.Open(cfg).Markets()
	if len(ms) != 1 || len(idx[ms[0].Name].Plugins) == 0 {
		t.Fatalf("markets = %+v", ms)
	}
	// Install every plugin the index lists in its own tree, and show one.
	installed := 0
	for _, e := range idx[ms[0].Name].Plugins {
		res, err := Run(context.Background(), cfg, []string{"install", e.Name + "@" + ms[0].Name})
		if err != nil {
			t.Logf("%s: %v", e.Name, err)
			continue
		}
		installed++
		t.Logf("%s", res.Notes[0])
	}
	if installed == 0 {
		t.Fatal("nothing installed")
	}
	t.Logf("%s", text(run(t, cfg, "list")))
}

func TestSlow(t *testing.T) {
	for args, want := range map[string]bool{"install x": true, "update": true, "marketplace add x": true, "marketplace list": false, "list": false, "": false, "trust x": false} {
		if got := Slow(strings.Fields(args)); got != want {
			t.Errorf("Slow(%q) = %v", args, got)
		}
	}
}
