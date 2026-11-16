package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xmonader/janetcode/internal/config"
	"github.com/xmonader/janetcode/internal/extension"
	"github.com/xmonader/janetcode/internal/inspect"
	"github.com/xmonader/janetcode/internal/mcp"
	"github.com/xmonader/janetcode/internal/mcp/mcptest"
	"github.com/xmonader/janetcode/internal/mcp/mcptool"
	"github.com/xmonader/janetcode/internal/tool"
	"github.com/xmonader/janetcode/internal/trust"
)

// TestMain makes the test binary an MCP server when MCP_FAKE_SERVER is
// set, so the extension report probes a real server over a pipe.
func TestMain(m *testing.M) {
	if os.Getenv("MCP_FAKE_SERVER") == "1" {
		mcptest.FakeServer()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// extProject is a config directory and a project directory: a user
// server with a secret in its environment, a project server, and project
// hooks, none of the project's approved yet.
func extProject(t *testing.T) (cwd, cfgDir string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
	t.Setenv("JANETCODE_DOCTOR_NO_PROBE", "")
	for _, k := range []string{"ANTHROPIC_API_KEY", "OPENAI_API_KEY", "ZAI_CODING_PLAN_API_KEY", "ZAI_API_KEY", "ZAI_CODING_PLAN", "OPENCODE_API_KEY", "OPENCODE_ZEN_API_KEY", "OPENCODE_GO_API_KEY"} {
		t.Setenv(k, "")
	}
	cfgDir = filepath.Join(home, ".config", "janetcode")
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := map[string]any{"mcp_servers": map[string]any{
		"userfake": map[string]any{"command": os.Args[0], "env": map[string]string{"MCP_FAKE_SERVER": "1", "API_KEY": "sekret-value-123456789"}},
	}}
	data, _ := json.Marshal(cfg)
	if err := os.WriteFile(filepath.Join(cfgDir, "config.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	cwd = t.TempDir()
	proj := map[string]any{"mcpServers": map[string]any{
		"projfake": map[string]any{"command": os.Args[0], "env": map[string]string{"MCP_FAKE_SERVER": "1"}},
	}}
	data, _ = json.Marshal(proj)
	if err := os.WriteFile(filepath.Join(cwd, ".mcp.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(cwd, ".janetcode"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cwd, ".janetcode", "hooks.json"), []byte(`{"pre_tool":[{"command":"echo hi","network":false}]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(cwd)
	return cwd, cfgDir
}

func decodeExt(t *testing.T, out string) extension.Report {
	t.Helper()
	var rep extension.Report
	if err := json.Unmarshal([]byte(out), &rep); err != nil {
		t.Fatalf("not a report: %v\n%s", err, out)
	}
	return rep
}

func rowOf(rep extension.Report, name string) extension.MCPRow {
	for _, m := range rep.MCP {
		if m.Name == name {
			return m
		}
	}
	return extension.MCPRow{}
}

// janetcode --doctor mcp: user-global and project-local servers are
// told apart, the untrusted project file is not started, a secret in a
// server's environment never appears, and approving the file changes
// the report.
func TestDoctorMCPSeparatesScopesAndHonoursTrust(t *testing.T) {
	cwd, cfgDir := extProject(t)

	var out bytes.Buffer
	if err := extensionsDoctorMode(context.Background(), &out, config.Overrides{}, "", true); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "sekret-value") {
		t.Fatalf("the report leaked an environment secret:\n%s", out.String())
	}
	rep := decodeExt(t, out.String())
	u, p := rowOf(rep, "userfake"), rowOf(rep, "projfake")
	if u.Scope != extension.ScopeUser || u.State != "probed" || len(u.Tools) == 0 || len(u.Prompts) == 0 || u.Resources == nil {
		t.Fatalf("user row = %+v", u)
	}
	if u.Auth.Kind != "env" || !strings.Contains(u.Auth.Detail, "API_KEY") {
		t.Fatalf("auth = %+v", u.Auth)
	}
	if p.Scope != extension.ScopeProject || p.Trust.Status != extension.StatusUntrusted || p.State != "not started" || len(p.Tools) != 0 {
		t.Fatalf("project row = %+v", p)
	}
	if len(rep.Hooks) != 1 || rep.Hooks[0].Active || rep.Hooks[0].Trust.Status != extension.StatusUntrusted {
		t.Fatalf("hooks = %+v", rep.Hooks)
	}

	// The text form says the same.
	out.Reset()
	if err := extensionsDoctorMode(context.Background(), &out, config.Overrides{}, "", false); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"MCP servers, user-global", "MCP servers, project-local", "userfake", "projfake", "untrusted", "will not run"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("text lacks %q:\n%s", want, out.String())
		}
	}

	// Approve the project's .mcp.json: it is probed now.
	_, hash, err := mcp.LoadProject(cwd)
	if err != nil {
		t.Fatal(err)
	}
	ts := trust.Store{Path: filepath.Join(cfgDir, "mcp-trust.json")}
	if err := ts.Trust(cwd, hash); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := extensionsDoctorMode(context.Background(), &out, config.Overrides{}, "", true); err != nil {
		t.Fatal(err)
	}
	p = rowOf(decodeExt(t, out.String()), "projfake")
	if p.Trust.Status != extension.StatusTrusted || p.State != "probed" || len(p.Tools) == 0 {
		t.Fatalf("approved project row = %+v", p)
	}
}

// With JANETCODE_DOCTOR_NO_PROBE nothing is started at all.
func TestDoctorMCPNoProbe(t *testing.T) {
	extProject(t)
	t.Setenv("JANETCODE_DOCTOR_NO_PROBE", "1")
	var out bytes.Buffer
	if err := extensionsDoctorMode(context.Background(), &out, config.Overrides{}, "", true); err != nil {
		t.Fatal(err)
	}
	if u := rowOf(decodeExt(t, out.String()), "userfake"); u.State != "not started" || len(u.Tools) != 0 {
		t.Fatalf("row = %+v", u)
	}
}

// /inspect extensions in a running session describes the live server and
// reflects a restriction at once; /doctor mcp routes to the same subject.
func TestExtensionsSubjectInASession(t *testing.T) {
	cwd, cfgDir := extProject(t)
	cfg, err := config.Load(config.Overrides{})
	if err != nil && !errors.Is(err, config.ErrNoCredentials) {
		t.Fatal(err)
	}
	reg := tool.NewRegistry()
	mgr := &mcptool.Manager{Options: mcp.Options{Root: cwd, Version: "t", HandshakeTimeout: 20 * time.Second}, Registry: reg,
		Policy: extension.Policy{Store: extension.Store{Path: extension.StorePath(cfgDir)}, Cwd: cwd}}
	t.Cleanup(mgr.Close)
	done := make(chan error, 1)
	mgr.Start(context.Background(), "userfake", cfg.MCPServers["userfake"], func(_ string, _ *mcp.Client, err error) { done <- err })
	if err := <-done; err != nil {
		t.Fatal(err)
	}

	gate, err := newTrustGate(cfgDir, "", os.Getenv)
	if err != nil {
		t.Fatal(err)
	}
	reg2 := inspect.New()
	registerExtensionsSubject(reg2, extensionEnv{cfg: cfg, cfgPath: filepath.Join(cfgDir, "config.json"), cfgDir: cfgDir,
		cwd: func() string { return cwd }, gate: gate, ts: trust.Store{Path: filepath.Join(cfgDir, "mcp-trust.json")}, mgr: mgr})

	req, ok := inspect.DoctorSubject("mcp json")
	if !ok || req != "extensions json" {
		t.Fatalf("DoctorSubject = %q %v", req, ok)
	}
	if _, ok := inspect.DoctorSubject(""); ok {
		t.Fatal("a bare /doctor was routed to the extension report")
	}
	js, err := reg2.Report(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	u := rowOf(decodeExt(t, js), "userfake")
	if u.State != "running" || !u.Found || len(u.Tools) == 0 {
		t.Fatalf("live row = %+v", u)
	}
	for _, tl := range u.Tools {
		if !tl.Callable {
			t.Fatalf("an unrestricted user server has a blocked tool: %+v", tl)
		}
	}

	// Restrict it: the same report, text and JSON, shows the change.
	if err := (extension.Store{Path: extension.StorePath(cfgDir)}).Set(extension.UserKey("userfake"), extension.Restricted("test")); err != nil {
		t.Fatal(err)
	}
	text, err := reg2.Report(context.Background(), "extensions")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text, "restricted · test") || !strings.Contains(text, "BLOCKED") {
		t.Fatalf("text does not show the restriction:\n%s", text)
	}
	js, _ = reg2.Report(context.Background(), "extensions json")
	blocked := 0
	for _, tl := range rowOf(decodeExt(t, js), "userfake").Tools {
		if !tl.Callable {
			blocked++
		}
	}
	if blocked == 0 {
		t.Fatal("the JSON shows no blocked tool for a restricted server")
	}
	if _, err := reg2.Report(context.Background(), "extensions yaml"); err == nil {
		t.Fatal("an unknown format was accepted")
	}
}
