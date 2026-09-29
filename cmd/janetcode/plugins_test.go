package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/config"
	"github.com/xmonader/janetcode/internal/hook"
	"github.com/xmonader/janetcode/internal/mcp"
	"github.com/xmonader/janetcode/internal/plugin"
)

// --plugin is /plugin from the shell.
func TestPluginFlag(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	src := filepath.Join(t.TempDir(), "kit")
	os.MkdirAll(filepath.Join(src, "commands"), 0o755)
	os.WriteFile(filepath.Join(src, "commands", "lint.md"), []byte("Lint"), 0o644)
	stdout, _ := captureStd(t)
	if err := run([]string{"--plugin", "install", src}); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"--plugin", "list"}); err != nil {
		t.Fatal(err)
	}
	out := stdout()
	if !strings.Contains(out, "installed kit") || !strings.Contains(out, "✓ kit") {
		t.Fatalf("stdout = %q", out)
	}
	if err := run([]string{"--plugin", "show nope"}); err == nil {
		t.Fatal("show of a missing plugin succeeded")
	}
}

// A plugin's hooks and servers join the configuration only once trusted,
// and only as they were when trusted.
func TestApplyPluginsRunsOnlyTrustedHooksAndServers(t *testing.T) {
	cfgDir := t.TempDir()
	src := filepath.Join(t.TempDir(), "kit")
	os.MkdirAll(filepath.Join(src, "hooks"), 0o755)
	os.WriteFile(filepath.Join(src, "hooks", "hooks.json"), []byte(`{"hooks":{"PostToolUse":[{"matcher":"Write","hooks":[{"type":"command","command":"${CLAUDE_PLUGIN_ROOT}/fmt.sh"}]}]}}`), 0o644)
	os.WriteFile(filepath.Join(src, ".mcp.json"), []byte(`{"mcpServers":{"lint":{"command":"./server"},"mine":{"command":"x"}}}`), 0o644)
	st := plugin.Open(cfgDir)
	rec, _, err := st.Install(context.Background(), src, plugin.InstallOptions{})
	if err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{MCPServers: map[string]mcp.ServerConfig{"mine": {Command: "users-own"}}}
	applyPlugins(cfgDir, cfg)
	if cfg.Hooks.Len() != 0 || len(cfg.MCPServers) != 1 {
		t.Fatalf("an untrusted plugin's parts were applied: hooks=%d servers=%v", cfg.Hooks.Len(), cfg.MCPServers)
	}
	if n := strings.Join(cfg.Notes, "\n"); !strings.Contains(n, "plugin kit has hooks or MCP servers you have not trusted") {
		t.Fatalf("notes = %q", n)
	}

	if _, err := st.Trust("kit"); err != nil {
		t.Fatal(err)
	}
	cfg = &config.Config{MCPServers: map[string]mcp.ServerConfig{"mine": {Command: "users-own"}}, Hooks: hook.Config{Limits: hook.Limits{Timeout: 7}}}
	applyPlugins(cfgDir, cfg)
	hs := cfg.Hooks.For(hook.PostTool)
	if len(hs) != 1 || hs[0].Command != rec.Dir+"/fmt.sh" {
		t.Fatalf("hooks = %+v", cfg.Hooks)
	}
	if cfg.Hooks.Limits.Timeout != 7 {
		t.Fatal("the user's hook limits were replaced")
	}
	if cfg.MCPServers["lint"].Cwd != rec.Dir || cfg.MCPServers["mine"].Command != "users-own" {
		t.Fatalf("servers = %+v", cfg.MCPServers)
	}
	if n := strings.Join(cfg.Notes, "\n"); !strings.Contains(n, "has the name of one in your config") {
		t.Fatalf("notes = %q", n)
	}

	// Disabled: nothing applies.
	st.SetEnabled("kit", false)
	cfg = &config.Config{}
	applyPlugins(cfgDir, cfg)
	if cfg.Hooks.Len() != 0 || len(cfg.MCPServers) != 0 {
		t.Fatal("a disabled plugin's parts were applied")
	}
}
