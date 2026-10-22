package plugin

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// fixture is a plugin in Claude Code's layout, committed to a git repo.
func fixture(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := filepath.Join(t.TempDir(), "review-kit")
	write(t, filepath.Join(dir, ".claude-plugin", "plugin.json"), `{"name":"review-kit","version":"1.0.0","description":"Review helpers","author":{"name":"a"}}`)
	write(t, filepath.Join(dir, "commands", "review.md"), "# Review the diff\nReview $ARGUMENTS")
	write(t, filepath.Join(dir, "agents", "reviewer.md"), "---\nname: reviewer\ndescription: reviews\n---\nYou review.")
	write(t, filepath.Join(dir, "skills", "checklist", "SKILL.md"), "---\nname: checklist\ndescription: A review checklist\n---\nCheck things.")
	write(t, filepath.Join(dir, "hooks", "hooks.json"), `{"hooks":{"PostToolUse":[{"matcher":"Write","hooks":[{"type":"command","command":"${CLAUDE_PLUGIN_ROOT}/fmt.sh"}]}]}}`)
	write(t, filepath.Join(dir, ".mcp.json"), `{"mcpServers":{"lint":{"command":"${CLAUDE_PLUGIN_ROOT}/lint-server","args":["--stdio"]}}}`)
	git(t, dir, "init", "-q")
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-qm", "v1")
	return dir
}

func TestInstallFromGitReadsClaudeLayout(t *testing.T) {
	src := fixture(t)
	s := Open(t.TempDir())
	rec, parts, err := s.Install(context.Background(), src, InstallOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if rec.Name != "review-kit" || rec.Version != "1.0.0" || !rec.Enabled || rec.Trusted != "" {
		t.Fatalf("record = %+v", rec)
	}
	if _, err := os.Stat(filepath.Join(rec.Dir, ".git")); err != nil {
		t.Fatal("a git source was not cloned, so update cannot pull it")
	}
	if len(parts.CommandDirs) != 1 || len(parts.AgentDirs) != 1 || len(parts.SkillDirs) != 1 {
		t.Fatalf("parts = %+v", parts)
	}
	// The plugin root is substituted, so a hook can run the plugin's own
	// script wherever it was installed.
	if !strings.Contains(string(parts.Hooks), rec.Dir+"/fmt.sh") || strings.Contains(string(parts.Hooks), "CLAUDE_PLUGIN_ROOT") {
		t.Fatalf("hooks = %s", parts.Hooks)
	}
	var servers map[string]map[string]any
	if err := json.Unmarshal(parts.MCP, &servers); err != nil || servers["lint"]["command"] != rec.Dir+"/lint-server" {
		t.Fatalf("mcp = %s (%v)", parts.MCP, err)
	}
	// The loaders see its directories; its hooks and servers do not run
	// until trusted.
	if d := Dirs(filepath.Dir(s.Dir), "commands"); len(d) != 1 || d[0].Plugin != "review-kit" {
		t.Fatalf("command dirs = %+v", d)
	}
	runs := s.ActiveRuns()
	if len(runs) != 1 || runs[0].Runs {
		t.Fatalf("an untrusted plugin's hooks run: %+v", runs)
	}
	if _, err := s.Trust("review-kit"); err != nil {
		t.Fatal(err)
	}
	if runs := s.ActiveRuns(); len(runs) != 1 || !runs[0].Runs {
		t.Fatalf("trusted plugin not running: %+v", runs)
	}
	// A second install under the same name is refused without --force.
	if _, _, err := s.Install(context.Background(), src, InstallOptions{}); err == nil || !strings.Contains(err.Error(), "already installed") {
		t.Fatalf("second install: %v", err)
	}
}

// An update that changes what the plugin runs takes the trust back; one
// that changes only a command keeps it.
func TestUpdateKeepsTrustOnlyWhenWhatRunsIsUnchanged(t *testing.T) {
	src := fixture(t)
	s := Open(t.TempDir())
	if _, _, err := s.Install(context.Background(), src, InstallOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Trust("review-kit"); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(src, "commands", "review.md"), "# Review better\n$ARGUMENTS")
	git(t, src, "commit", "-qam", "wording")
	_, _, changed, err := s.Update(context.Background(), "review-kit")
	if err != nil || changed {
		t.Fatalf("wording update: changed=%v err=%v", changed, err)
	}
	if runs := s.ActiveRuns(); !runs[0].Runs {
		t.Fatal("a wording change took the trust away")
	}
	p, _ := s.Get("review-kit")
	if b, _ := os.ReadFile(filepath.Join(p.Dir, "commands", "review.md")); !strings.Contains(string(b), "Review better") {
		t.Fatalf("update did not fetch: %q", b)
	}
	write(t, filepath.Join(src, "hooks", "hooks.json"), `{"hooks":{"PostToolUse":[{"hooks":[{"type":"command","command":"curl evil"}]}]}}`)
	git(t, src, "commit", "-qam", "hook")
	_, _, changed, err = s.Update(context.Background(), "review-kit")
	if err != nil || !changed {
		t.Fatalf("hook update: changed=%v err=%v", changed, err)
	}
	if runs := s.ActiveRuns(); runs[0].Runs {
		t.Fatal("a changed hook runs without being trusted again")
	}
}

func TestDisableRemoveAndLink(t *testing.T) {
	src := fixture(t)
	cfg := t.TempDir()
	s := Open(cfg)
	rec, _, err := s.Install(context.Background(), src, InstallOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetEnabled("review-kit", false); err != nil {
		t.Fatal(err)
	}
	if d := Dirs(cfg, "skills"); len(d) != 0 {
		t.Fatalf("a disabled plugin's skills load: %+v", d)
	}
	if err := s.Remove("review-kit"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(rec.Dir); !os.IsNotExist(err) {
		t.Fatal("remove left the copy behind")
	}
	// A linked plugin is used in place and never deleted.
	linked, _, err := s.Install(context.Background(), src, InstallOptions{Link: true})
	if err != nil || linked.Dir != src {
		t.Fatalf("link = %+v, %v", linked, err)
	}
	if err := s.Remove("review-kit"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(src); err != nil {
		t.Fatal("removing a linked plugin deleted its source")
	}
}

func TestMarketplace(t *testing.T) {
	src := fixture(t)
	market := filepath.Join(t.TempDir(), "mkt")
	// One plugin in the marketplace's own tree, one elsewhere by git URL.
	write(t, filepath.Join(market, "plugins", "tiny", "commands", "hi.md"), "Say hi")
	write(t, filepath.Join(market, ".claude-plugin", "marketplace.json"), `{"name":"team","owner":{"name":"x"},"plugins":[
		{"name":"tiny","source":"./plugins/tiny","description":"hi"},
		{"name":"review-kit","source":{"source":"url","url":"file://`+filepath.ToSlash(src)+`"}},
		{"name":"escape","source":"../../etc"}]}`)
	git(t, market, "init", "-q")
	git(t, market, "add", "-A")
	git(t, market, "commit", "-qm", "m")
	s := Open(t.TempDir())
	m, idx, err := s.AddMarket(context.Background(), market)
	if err != nil {
		t.Fatal(err)
	}
	if m.Name != "team" || len(idx.Plugins) != 3 {
		t.Fatalf("market = %+v %+v", m, idx)
	}
	rec, _, err := s.Install(context.Background(), "tiny@team", InstallOptions{})
	if err != nil || rec.Name != "tiny" || rec.Marketplace != "team" {
		t.Fatalf("tiny = %+v, %v", rec, err)
	}
	if _, _, err := s.Install(context.Background(), "review-kit@team", InstallOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Install(context.Background(), "escape@team", InstallOptions{}); err == nil || !strings.Contains(err.Error(), "outside") {
		t.Fatalf("an entry pointing outside the marketplace = %v", err)
	}
	if _, _, err := s.Install(context.Background(), "nope@team", InstallOptions{}); err == nil {
		t.Fatal("an unknown entry installed")
	}
	if _, _, err := s.Install(context.Background(), "x@nomarket", InstallOptions{}); err == nil || !strings.Contains(err.Error(), "marketplace add") {
		t.Fatalf("unknown market = %v", err)
	}
	if _, _, _, err := s.Update(context.Background(), "tiny"); err != nil {
		t.Fatalf("update from marketplace: %v", err)
	}
	if err := s.RemoveMarket("team"); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.Get("tiny"); !ok {
		t.Fatal("removing a marketplace uninstalled its plugins")
	}
}

func TestManifestPathsCannotEscape(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, ".claude-plugin", "plugin.json"), `{"name":"p","commands":"../outside","hooks":"/etc/passwd"}`)
	m, _, err := ReadManifest(dir)
	if err != nil {
		t.Fatal(err)
	}
	p := Components(dir, m)
	if len(p.CommandDirs) != 0 || len(p.Hooks) != 0 || len(p.Problems) != 2 {
		t.Fatalf("parts = %+v", p)
	}
}

func TestOptionLikeSourceIsRefused(t *testing.T) {
	s := Open(t.TempDir())
	if _, _, err := s.Install(context.Background(), "--upload-pack=touch /tmp/x", InstallOptions{}); err == nil || !strings.Contains(err.Error(), "option") {
		t.Fatalf("err = %v", err)
	}
}

func TestSymlinksCannotLeaveThePlugin(t *testing.T) {
	outside := t.TempDir()
	write(t, filepath.Join(outside, "hooks.json"), `{"hooks":{"PostToolUse":[{"hooks":[{"type":"command","command":"steal"}]}]}}`)
	write(t, filepath.Join(outside, "cmds", "x.md"), "x")
	dir := t.TempDir()
	write(t, filepath.Join(dir, ".claude-plugin", "plugin.json"), `{"name":"p"}`)
	if err := os.MkdirAll(filepath.Join(dir, "hooks"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "hooks.json"), filepath.Join(dir, "hooks", "hooks.json")); err != nil {
		t.Skip("symlinks unavailable:", err)
	}
	if err := os.Symlink(filepath.Join(outside, "cmds"), filepath.Join(dir, "commands")); err != nil {
		t.Fatal(err)
	}
	m, _, err := ReadManifest(dir)
	if err != nil {
		t.Fatal(err)
	}
	p := Components(dir, m)
	if len(p.Hooks) != 0 || len(p.CommandDirs) != 0 || len(p.Problems) != 2 {
		t.Fatalf("parts = %+v", p)
	}
}
