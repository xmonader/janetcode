package claudeimport

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func put(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestConvertRule(t *testing.T) {
	for _, c := range []struct {
		in   string
		want []string
		skip string
	}{
		{"Bash(npm test:*)", []string{"bash(npm test)", "bash(npm test *)"}, ""},
		{"Bash(git status)", []string{"bash(git status)"}, ""},
		{"Bash", []string{"bash"}, ""},
		{"Bash(*)", []string{"bash"}, ""},
		{"Read(./secrets/**)", []string{"read_file(secrets/**)"}, ""},
		{"Read(//etc/hosts)", []string{"read_file(/etc/hosts)"}, ""},
		{"Read(/src/**)", []string{"read_file(src/**)"}, ""},
		{"Read(~/.ssh/*)", []string{"read_file(HOME/.ssh/*)"}, ""},
		{"Edit(src/**)", []string{"edit_file(src/**)", "write_file(src/**)"}, ""},
		{"WebFetch(domain:example.com)", []string{"fetch_url(domain:example.com)"}, ""},
		{"WebFetch(https://x.test/a)", nil, "not a domain"},
		{"WebSearch", []string{"web_search"}, ""},
		{"mcp__github__create_issue", []string{"mcp__github__create_issue"}, ""},
		{"mcp__github", nil, "server-wide"},
		{"Task(Explore)", nil, "no janetcode tool"},
	} {
		got, why := ConvertRule(c.in, "HOME")
		var names []string
		for _, r := range got {
			names = append(names, r.String())
		}
		if !reflect.DeepEqual(names, c.want) || (c.skip != "" && !strings.Contains(why, c.skip)) {
			t.Errorf("%q: got %v (%q), want %v (%q)", c.in, names, why, c.want, c.skip)
		}
	}
}

func fixture(t *testing.T) Env {
	home, cwd, cfg := t.TempDir(), t.TempDir(), t.TempDir()
	put(t, filepath.Join(home, ".claude.json"), `{"mcpServers":{
	  "files":{"command":"/usr/bin/npx","args":["x"],"env":{"TOKEN":"sk-SECRET-VALUE"}},
	  "remote":{"type":"http","url":"https://mcp.example/path?key=SECRETKEY","headers":{"Authorization":"Bearer SECRET-HDR"}},
	  "dup":{"command":"dup"}},
	  "projects":{"`+cwd+`":{"mcpServers":{"local":{"command":"loc"}}}}}`)
	put(t, filepath.Join(home, ".claude/settings.json"), `{"permissions":{"allow":["Bash(go test:*)","Task"],"deny":["Read(./.env)"]},"hooks":{},"env":{"A":"b"}}`)
	put(t, filepath.Join(cwd, ".claude/settings.json"), `{"permissions":{"ask":["Bash(git push:*)"]}}`)
	put(t, filepath.Join(home, ".claude/skills/lint/SKILL.md"), "---\nname: lint\n---\nrun lint")
	put(t, filepath.Join(home, ".claude/skills/lint/ref/a.txt"), "ref")
	put(t, filepath.Join(home, ".claude/agents/rev.md"), "---\nname: rev\n---\nreview")
	cfgPath := filepath.Join(cfg, "config.json")
	put(t, cfgPath, `{"mcp_servers":{"dup":{"command":"mine"}},"permissions":{"deny":["read_file(.env)"]}}`)
	return Env{Home: home, Cwd: cwd, ConfigPath: cfgPath, ConfigDir: cfg}
}

func TestPreviewListsPlanWithoutSecretsOrWrites(t *testing.T) {
	env := fixture(t)
	before, _ := os.ReadFile(env.ConfigPath)
	res := Exec(env, "")
	for _, secret := range []string{"sk-SECRET-VALUE", "SECRETKEY", "SECRET-HDR"} {
		if strings.Contains(res.Sheet, secret) {
			t.Fatalf("preview leaks %q:\n%s", secret, res.Sheet)
		}
	}
	for _, want := range []string{"+ files  npx, 1 env value(s)", "https://mcp.example, 1 header(s)", "- dup", "already in the config file",
		"+ allow bash(go test)", "+ allow bash(go test *)", "- deny read_file(.env)", "already a rule", "no janetcode tool for Task",
		"+ ask bash(git push *)", "+ lint", "+ rev", "hooks in", "env in", "/import-claude apply "} {
		if !strings.Contains(res.Sheet, want) {
			t.Errorf("preview lacks %q:\n%s", want, res.Sheet)
		}
	}
	if after, _ := os.ReadFile(env.ConfigPath); string(after) != string(before) {
		t.Error("the preview wrote the config")
	}
	if _, err := os.Stat(filepath.Join(env.ConfigDir, "skills")); err == nil {
		t.Error("the preview copied a skill")
	}
}

func TestApplyNeedsTheIDAndNeverOverwrites(t *testing.T) {
	env := fixture(t)
	if r := Exec(env, "apply"); r.Error == "" {
		t.Error("apply without an id wrote")
	}
	if r := Exec(env, "apply deadbeef"); r.Error == "" || !strings.Contains(r.Error, "changed") {
		t.Errorf("wrong id accepted: %+v", r)
	}
	id := Scan(env).ID()
	// A later edit of the Claude side voids the id.
	put(t, filepath.Join(env.Home, ".claude/agents/new.md"), "x")
	if r := Exec(env, "apply "+id); r.Error == "" {
		t.Error("a stale id applied")
	}
	id = Scan(env).ID()
	res := Exec(env, "apply "+id)
	if res.Error != "" {
		t.Fatalf("apply: %+v", res)
	}
	var cfg struct {
		MCP  map[string]json.RawMessage          `json:"mcp_servers"`
		Perm struct{ Allow, Deny, Ask []string } `json:"permissions"`
	}
	b, _ := os.ReadFile(env.ConfigPath)
	if err := json.Unmarshal(b, &cfg); err != nil {
		t.Fatal(err)
	}
	if strings.Join(strings.Fields(string(cfg.MCP["dup"])), "") != `{"command":"mine"}` {
		t.Errorf("an existing server was replaced: %s", cfg.MCP["dup"])
	}
	if _, ok := cfg.MCP["files"]; !ok || len(cfg.MCP) != 3 {
		t.Errorf("servers %v", cfg.MCP)
	}
	if !reflect.DeepEqual(cfg.Perm.Allow, []string{"bash(go test)", "bash(go test *)"}) || !reflect.DeepEqual(cfg.Perm.Deny, []string{"read_file(.env)"}) {
		t.Errorf("perm %+v", cfg.Perm)
	}
	pb, _ := os.ReadFile(filepath.Join(env.Cwd, ".janetcode/permissions.json"))
	if !strings.Contains(string(pb), "bash(git push)") || !strings.Contains(string(pb), `"ask"`) {
		t.Errorf("project rules: %s", pb)
	}
	if mb, _ := os.ReadFile(filepath.Join(env.Cwd, ".mcp.json")); !strings.Contains(string(mb), `"local"`) {
		t.Errorf("project-scoped server not written to .mcp.json: %s", mb)
	}
	if b, _ := os.ReadFile(filepath.Join(env.ConfigDir, "skills/lint/ref/a.txt")); string(b) != "ref" {
		t.Error("skill tree not copied")
	}
	if _, err := os.Stat(filepath.Join(env.ConfigDir, "agents/rev.md")); err != nil {
		t.Error("agent not copied")
	}
	// Running it again finds nothing new and does not touch anything.
	again := Scan(env)
	if again.Pending() != 0 {
		t.Errorf("second scan still has %d pending", again.Pending())
	}
}

func TestKindFilterAndUsage(t *testing.T) {
	env := fixture(t)
	res := Exec(env, "agents")
	if strings.Contains(res.Sheet, "mcp") || !strings.Contains(res.Sheet, "rev") {
		t.Errorf("filter:\n%s", res.Sheet)
	}
	if r := Exec(env, "bogus"); !strings.Contains(r.Error, "usage") {
		t.Errorf("bogus kind: %+v", r)
	}
}

func TestSkillCopyRefusesSymlinks(t *testing.T) {
	env := fixture(t)
	if err := os.Symlink("/etc/passwd", filepath.Join(env.Home, ".claude/skills/lint/link")); err != nil {
		t.Skip(err)
	}
	for _, it := range Scan(env, "skills").Items {
		if it.Name == "lint" && !strings.Contains(it.Skip, "symlink") {
			t.Errorf("skill with a symlink not refused: %+v", it)
		}
	}
}

func TestNothingToImport(t *testing.T) {
	env := Env{Home: t.TempDir(), Cwd: t.TempDir(), ConfigPath: filepath.Join(t.TempDir(), "c.json"), ConfigDir: t.TempDir()}
	if r := Exec(env, ""); !strings.Contains(r.Sheet, "nothing to import") {
		t.Errorf("%+v", r)
	}
}
