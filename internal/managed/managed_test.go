package managed

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/perm"
)

func withSystem(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "managed.json")
	if content != "" {
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	old := systemPath
	systemPath = func() string { return p }
	t.Cleanup(func() { systemPath = old })
	return p
}

func noEnv(string) string { return "" }

func TestNoFileIsNoPolicy(t *testing.T) {
	withSystem(t, "")
	p, err := Load(noEnv)
	if err != nil || p != nil {
		t.Fatalf("Load = %v, %v; want nil, nil", p, err)
	}
	// Every method of the nil policy answers "not managed".
	if p.Locked("model") || p.ModeDisabled(ModeYolo) || p.ToolDisabled("bash") || !p.ModelAllowed("x") ||
		!p.ProviderAllowed("x", "openai") || !p.MCPAllowed("x") || p.Summary() != nil {
		t.Error("the nil policy managed something")
	}
}

func TestBadFileIsAnError(t *testing.T) {
	for _, content := range []string{`{`, `{"nope": 1}`, `{"disabled_modes": ["fast"]}`, `{"permissions": {"deny": ["bash("]}}`, `{} {}`} {
		withSystem(t, content)
		if _, err := Load(noEnv); err == nil {
			t.Errorf("%s: no error", content)
		}
	}
}

func TestPolicyAnswers(t *testing.T) {
	withSystem(t, `{
		"message": "ask IT",
		"settings": {"effort": "high", "decisions": {"enabled": false}},
		"permissions": {"deny": ["bash(cmd:curl *)"], "ask": ["*(touches:**/.env)"]},
		"disabled_tools": ["web_search", "mcp__*__delete*"],
		"allowed_providers": ["corp"],
		"allowed_models": ["claude-*"],
		"sandbox": "workspace",
		"disabled_modes": ["yolo"],
		"allowed_mcp_servers": ["github"]
	}`)
	p, err := Load(noEnv)
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"effort", "decisions.enabled", "sandbox", "auto_approve"} {
		if !p.Locked(k) {
			t.Errorf("%s not locked", k)
		}
	}
	if p.Locked("model") {
		t.Error("model locked")
	}
	if !p.ToolDisabled("web_search") || !p.ToolDisabled("mcp__fs__delete_file") || p.ToolDisabled("bash") {
		t.Error("ToolDisabled wrong")
	}
	if !p.ProviderAllowed("corp", "anthropic") || p.ProviderAllowed("openrouter", "openai") {
		t.Error("ProviderAllowed wrong")
	}
	if !p.ModelAllowed("claude-opus-5") || p.ModelAllowed("gpt-5") {
		t.Error("ModelAllowed wrong")
	}
	if !p.MCPAllowed("github") || p.MCPAllowed("fs") {
		t.Error("MCPAllowed wrong")
	}
	err = p.Refusal("--yolo")
	if !strings.Contains(err.Error(), "managed policy") || !strings.Contains(err.Error(), "ask IT") {
		t.Errorf("Refusal = %v", err)
	}
	if s := strings.Join(p.Summary(), "\n"); !strings.Contains(s, "sandbox forced: workspace") || !strings.Contains(s, "disabled modes: yolo") {
		t.Errorf("Summary = %s", s)
	}
}

// managed_permission_rules_only drops every allow rule not the policy's,
// and the policy's deny rules apply whatever was saved.
func TestApplyRules(t *testing.T) {
	p := &Policy{Permissions: perm.File{Allow: []string{"bash(cmd:go test *)"}, Deny: []string{"bash(cmd:curl *)"}}, ManagedRulesOnly: true}
	user, _ := perm.File{Allow: []string{"bash(*)"}}.Rules()
	rs := p.Apply(user)
	if len(rs.Allow) != 1 || rs.Allow[0].String() != "bash(cmd:go test *)" || len(rs.Deny) != 1 {
		t.Fatalf("rules = %+v", rs)
	}
	p.ManagedRulesOnly = false
	if rs := p.Apply(user); len(rs.Allow) != 2 {
		t.Fatalf("without managed-only, rules = %+v", rs)
	}
}

// A file named in the environment can only make the policy stricter: it
// cannot change a value the system file fixes, and an allow-list it
// names is intersected with the system file's.
func TestEnvFileCannotLoosen(t *testing.T) {
	withSystem(t, `{"settings": {"effort": "high"}, "sandbox": "strict", "allowed_models": ["a", "b"], "disabled_modes": ["yolo"]}`)
	extra := filepath.Join(t.TempDir(), "extra.json")
	os.WriteFile(extra, []byte(`{"settings": {"effort": "low", "memory": false}, "sandbox": "workspace", "allowed_models": ["b", "c"], "disabled_modes": ["auto"]}`), 0o644)
	p, err := Load(func(k string) string {
		if k == EnvFile {
			return extra
		}
		return ""
	})
	if err != nil {
		t.Fatal(err)
	}
	if string(p.Settings["effort"]) != `"high"` || p.Sandbox != "strict" {
		t.Errorf("env file loosened: effort %s sandbox %s", p.Settings["effort"], p.Sandbox)
	}
	if !p.Locked("memory") {
		t.Error("env file's own setting not applied")
	}
	if p.ModelAllowed("a") || !p.ModelAllowed("b") || p.ModelAllowed("c") {
		t.Errorf("allowed models = %v, want the intersection", p.AllowedModels)
	}
	if !p.ModeDisabled(ModeYolo) || !p.ModeDisabled(ModeAuto) {
		t.Error("disabled modes not joined")
	}
	if len(p.Sources) != 2 {
		t.Errorf("sources = %v", p.Sources)
	}
	// A named file that is missing is an error, not no policy.
	if _, err := Load(func(k string) string { return "/nonexistent/managed.json" }); err == nil {
		t.Error("missing env file: no error")
	}
}
