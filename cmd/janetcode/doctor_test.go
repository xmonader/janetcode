package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xmonader/janetcode/internal/config"
	"github.com/xmonader/janetcode/internal/mcp"
)

// TestDoctorReportsProjectFeatures runs doctor in a project with hooks,
// verification commands, permission rules and a skill but no credentials:
// the credential check fails, and every project row still reports.
func TestDoctorReportsProjectFeatures(t *testing.T) {
	work := t.TempDir()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("OPENAI_API_KEY", "")
	os.MkdirAll(filepath.Join(work, ".janetcode", "skills", "review"), 0o755)
	files := map[string]string{
		".janetcode/hooks.json":             `{"pre_tool": [{"command": "echo hi"}], "stop": [{"command": "true"}]}`,
		".janetcode/verify.json":            `{"commands": ["go build ./..."]}`,
		".janetcode/permissions.json":       `{"allow": ["bash(go test *)"], "deny": ["bash(rm -rf *)"]}`,
		".janetcode/skills/review/SKILL.md": "---\ndescription: review code\n---\nlook carefully\n",
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(work, filepath.FromSlash(name)), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	old, _ := os.Getwd()
	if err := os.Chdir(work); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chdir(old) })

	var out bytes.Buffer
	err := doctor(context.Background(), &out, config.Overrides{}, false)
	if err == nil || !strings.Contains(err.Error(), "checks failed") {
		t.Fatalf("err = %v; want a credential failure", err)
	}
	got := out.String()
	for _, want := range []string{
		"[FAIL] credentials",
		"[ok  ] hooks        2 hooks in .janetcode/hooks.json · not trusted yet (/trust full, or /hooks trust in the TUI) · 0 global",
		"[ok  ] verify       1 command in .janetcode/verify.json · not trusted yet (/trust full, or /verify trust in the TUI)",
		"[ok  ] permissions  1 allow / 1 deny in .janetcode/permissions.json · allow rules not trusted yet",
		"[ok  ] skills       1 skill (1 project, 0 global, 0 claude) · the project's are not used until it is trusted",
		"[warn] trust        undecided (none) · governs skills, hooks, verify commands, allow rules",
		"[ok  ] memory       0 memory entries (0 project, 0 global)",
		"lexical recall only",
		"lsp          gopls",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}

	// A broken hooks file is a failure, not a crash.
	os.WriteFile(filepath.Join(work, ".janetcode", "hooks.json"), []byte(`{"bogus_event": []}`), 0o644)
	out.Reset()
	doctor(context.Background(), &out, config.Overrides{}, false)
	if !strings.Contains(out.String(), "[FAIL] hooks") {
		t.Fatalf("broken hooks not reported:\n%s", out.String())
	}
}

// TestDoctorFixListsThenRepairs is the two-step the flag exists for: the
// plain checkup says what it would repair and touches nothing, and
// --fix carries out exactly those repairs and says what changed. An
// untrusted verify.json is not one of them: it is listed as a step for
// the user and stays untrusted however often --fix runs.
func TestDoctorFixListsThenRepairs(t *testing.T) {
	work := t.TempDir()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("OPENAI_API_KEY", "")
	if err := os.MkdirAll(filepath.Join(work, ".janetcode"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(work, ".janetcode", "verify.json"), []byte(`{"commands":["go build ./..."]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	old, _ := os.Getwd()
	if err := os.Chdir(work); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chdir(old) })
	cfgDir := filepath.Join(home, ".config", "janetcode")
	if err := os.MkdirAll(cfgDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfgDir, "config.json"), []byte(`{}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(cfgDir, "config.json"), 0o644); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	doctor(context.Background(), &out, config.Overrides{}, false)
	if !strings.Contains(out.String(), "would fix: make ") || !strings.Contains(out.String(), "needs you: .janetcode/verify.json is not trusted") {
		t.Fatalf("the checkup did not offer the repair and the manual step:\n%s", out.String())
	}
	out.Reset()
	doctor(context.Background(), &out, config.Overrides{}, true)
	if !strings.Contains(out.String(), "fixed: make ") || strings.Contains(out.String(), "fixed: trust") {
		t.Fatalf("--fix did not report exactly the repair:\n%s", out.String())
	}
	out.Reset()
	doctor(context.Background(), &out, config.Overrides{}, true)
	if strings.Contains(out.String(), "fixed: ") || !strings.Contains(out.String(), "nothing to fix") {
		t.Fatalf("--fix repeated itself:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "verify       1 command in .janetcode/verify.json · not trusted yet") ||
		!strings.Contains(out.String(), "needs you: .janetcode/verify.json is not trusted") {
		t.Errorf("--fix trusted the repository's verify.json for the user:\n%s", out.String())
	}
}

// The checkup reads the whole machine and calls the model. A caller that
// no longer wants the answer -- a quit TUI, a ctrl+c'd terminal -- gets
// out of it instead of waiting for the provider's own timeout.
func TestDoctorHonoursCancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var out bytes.Buffer
	if err := doctor(ctx, &out, config.Overrides{}, false); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if out.Len() != 0 {
		t.Fatalf("wrote a report for a cancelled checkup:\n%s", out.String())
	}
}

// Only the provider in use has to work: an example entry with no key is
// information, not a failure, and a route to it says what will go wrong.
func TestDoctorFailsOnlyTheProviderInUse(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("OPENAI_API_KEY", "")
	cfgDir := filepath.Join(home, ".config", "janetcode")
	os.MkdirAll(cfgDir, 0o755)
	os.WriteFile(filepath.Join(cfgDir, "config.json"), []byte(`{
		"provider": "local", "model": "m",
		"providers": {
			"local": {"type": "openai", "base_url": "http://127.0.0.1:1/v1"},
			"anthropic": {"type": "anthropic"}
		}
	}`), 0o644)
	t.Chdir(t.TempDir())

	var out bytes.Buffer
	doctor(context.Background(), &out, config.Overrides{}, false)
	got := out.String()
	if !strings.Contains(got, "[info] provider     anthropic · default URL · 0 keys · no key, not in use") {
		t.Errorf("an unused keyless provider is not reported as info:\n%s", got)
	}
	if strings.Contains(got, "[FAIL] provider") {
		t.Errorf("a provider failed:\n%s", got)
	}
}

// A repair --fix could not carry out fails the run: every check passing
// and a failed repair used to end in "all checks passed" and exit 0.
func TestDoctorFixFailsWhenARepairFails(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root writes into a read-only directory")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("OPENAI_API_KEY", "")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "text/event-stream")
		io.WriteString(w, `data: {"choices":[{"delta":{"content":"ok"},"finish_reason":"stop"}]}`+"\n\ndata: [DONE]\n\n")
	}))
	defer srv.Close()
	cfgDir := filepath.Join(home, ".config", "janetcode")
	os.MkdirAll(cfgDir, 0o700)
	os.WriteFile(filepath.Join(cfgDir, "config.json"), []byte(`{
		"provider": "local", "model": "m",
		"providers": {"local": {"type": "openai", "base_url": "`+srv.URL+`/v1", "api_key": "k"}}
	}`), 0o600)
	// An expired login with no refresh token is a repair; the read-only
	// directory makes it fail.
	tokens := &mcp.TokenStore{Path: filepath.Join(cfgDir, "mcp-auth.json")}
	if err := tokens.Put("", "https://mcp.example.com", mcp.Token{AccessToken: "a", Expiry: time.Now().Add(-time.Hour)}); err != nil {
		t.Fatal(err)
	}
	os.Chmod(cfgDir, 0o500)
	t.Cleanup(func() { os.Chmod(cfgDir, 0o700) })
	t.Chdir(t.TempDir())

	var out bytes.Buffer
	if err := doctor(context.Background(), &out, config.Overrides{}, false); err != nil {
		t.Fatalf("the checkup itself should pass here: %v\n%s", err, out.String())
	}
	out.Reset()
	err := doctor(context.Background(), &out, config.Overrides{}, true)
	if err == nil || strings.Contains(out.String(), "all checks passed") {
		t.Fatalf("a failed repair passed the run: err=%v\n%s", err, out.String())
	}
	out.Reset()
	err = doctorJSON(context.Background(), &out, config.Overrides{}, true)
	var got struct{ OK bool }
	if jerr := json.Unmarshal(out.Bytes(), &got); jerr != nil || got.OK || err == nil {
		t.Fatalf("json: ok=%v err=%v jerr=%v\n%s", got.OK, err, jerr, out.String())
	}
}

// --doctor --output-format json is the same checkup as one object a script
// can read: every line with its status, the failure count, and the same
// error (so the same exit status) as the text form.
func TestDoctorJSON(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("OPENAI_API_KEY", "")
	cfgDir := filepath.Join(home, ".config", "janetcode")
	os.MkdirAll(cfgDir, 0o755)
	os.WriteFile(filepath.Join(cfgDir, "config.json"), []byte(`{
		"provider": "local", "model": "m",
		"providers": {"local": {"type": "openai", "base_url": "http://127.0.0.1:1/v1"}}
	}`), 0o644)
	t.Chdir(t.TempDir())

	var out bytes.Buffer
	err := doctorJSON(context.Background(), &out, config.Overrides{}, false)
	var got struct {
		OK      bool
		Failed  int
		Checks  []doctorCheck
		Repairs *struct{ Sheet string }
	}
	if jerr := json.Unmarshal(out.Bytes(), &got); jerr != nil {
		t.Fatalf("not one JSON object: %v\n%s", jerr, out.String())
	}
	byName := map[string]doctorCheck{}
	for _, c := range got.Checks {
		byName[c.Check] = c
	}
	if c := byName["model call"]; c.Status != "fail" {
		t.Errorf("the unreachable model should fail: %+v", c)
	}
	if c := byName["provider local"]; c.Status != "ok" {
		t.Errorf("provider row = %+v", c)
	}
	if got.OK || got.Failed == 0 || err == nil {
		t.Errorf("ok=%v failed=%d err=%v; a failed check must fail the run", got.OK, got.Failed, err)
	}
	if got.Repairs == nil || !strings.Contains(got.Repairs.Sheet, "config-readable") && !strings.Contains(got.Repairs.Sheet, "private") {
		t.Errorf("the 0644 config should be listed as a repair: %+v", got.Repairs)
	}
}
