package tool

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSelfPath(t *testing.T) {
	root := t.TempDir()
	cfg := t.TempDir()
	data := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfg)
	t.Setenv("XDG_DATA_HOME", data)
	cases := []struct {
		abs  string
		want string
	}{
		{filepath.Join(root, ".mcp.json"), "MCP"},
		{filepath.Join(root, ".janetcode"), "project settings"},
		{filepath.Join(root, ".janetcode", "hooks.json"), "project settings"},
		{filepath.Join(root, ".janetcode", "skills", "x", "SKILL.md"), "project settings"},
		{filepath.Join(cfg, "janetcode", "config.json"), "configuration"},
		{filepath.Join(data, "janetcode", "memory", "p", "x.md"), "memory"},
		{filepath.Join(root, "main.go"), ""},
		{filepath.Join(root, ".janetcodeish", "x"), ""},
	}
	for _, c := range cases {
		got := SelfPath(root, c.abs)
		if c.want == "" && got != "" {
			t.Errorf("SelfPath(%q) = %q, want none", c.abs, got)
		}
		if c.want != "" && !strings.Contains(got, c.want) {
			t.Errorf("SelfPath(%q) = %q, want it to mention %q", c.abs, got, c.want)
		}
	}
}

func TestWriteAndEditGuardTheirOwnSettings(t *testing.T) {
	root := t.TempDir()
	w := WriteFile{Root: root}
	e := EditFile{Root: root}
	ask, why := w.Guard(json.RawMessage(`{"path":".janetcode/permissions.json","content":"x"}`))
	if !ask || !strings.Contains(why, "without asking") {
		t.Fatalf("write guard = %v %q", ask, why)
	}
	if ask, _ := e.Guard(json.RawMessage(`{"path":".mcp.json","old_string":"a","new_string":"b"}`)); !ask {
		t.Fatal("an edit of .mcp.json must ask")
	}
	if ask, _ := w.Guard(json.RawMessage(`{"path":"main.go","content":"x"}`)); ask {
		t.Fatal("an ordinary file must not be guarded")
	}
}

// The guard judges where a write lands: a symlink inside the workspace
// that points into .janetcode is an ordinary-looking path the file tools
// follow, and confinement allows it because the target is still inside
// the workspace.
func TestWriteGuardFollowsSymlinks(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".janetcode"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(".janetcode", filepath.Join(root, "conf")); err != nil {
		t.Skip("symlinks unavailable:", err)
	}
	if err := os.WriteFile(filepath.Join(root, ".mcp.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(".mcp.json", filepath.Join(root, "servers.json")); err != nil {
		t.Fatal(err)
	}
	w := WriteFile{Root: root}
	for _, p := range []string{"conf/hooks.json", "servers.json"} {
		in, _ := json.Marshal(map[string]string{"path": p, "content": "x"})
		if ask, _ := w.Guard(in); !ask {
			t.Errorf("a write to %s, a link into janetcode's settings, did not ask", p)
		}
	}
}

func TestShellFormsThatTouchTheSettingsAsk(t *testing.T) {
	root := t.TempDir()
	cfg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfg)
	b := Bash{Root: root}
	yes := []string{
		`echo '{}' > .janetcode/hooks.json`,
		`echo '{}' >> .mcp.json`,
		`cp /tmp/x .janetcode/permissions.json`,
		`mv /tmp/x .mcp.json`,
		`sed -i s/deny/allow/ .janetcode/permissions.json`,
		`tee .janetcode/hooks.json < /tmp/x`,
		`rm -f .janetcode/permissions.json`,
		`cat x | tee .janetcode/verify.json`,
		`cp /tmp/x ` + filepath.Join(cfg, "janetcode", "config.json"),
		`rm -rf .janetcode/`,
		`echo x > .janetcode/rules/a.md`,
	}
	for _, c := range yes {
		in, _ := json.Marshal(map[string]string{"command": c})
		ask, why := b.Guard(in)
		if !ask {
			t.Errorf("%q did not ask", c)
			continue
		}
		if !strings.Contains(why, "without asking") {
			t.Errorf("%q: reason %q", c, why)
		}
	}
	no := []string{
		`go test ./...`,
		`cat .janetcode/hooks.json`,
		`grep allow .janetcode/permissions.json`,
		`echo x > out.txt`,
		`cp a.go b.go`,
	}
	for _, c := range no {
		in, _ := json.Marshal(map[string]string{"command": c})
		if ask, why := b.Guard(in); ask {
			t.Errorf("%q asked unnecessarily: %s", c, why)
		}
	}
}
