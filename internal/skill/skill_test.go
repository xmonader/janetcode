package skill

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/llm"
)

func write(t *testing.T, p, s string) {
	t.Helper()
	os.MkdirAll(filepath.Dir(p), 0o755)
	if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestLoadShadowingAndFrontmatter(t *testing.T) {
	cwd, cfg := t.TempDir(), t.TempDir()
	write(t, filepath.Join(cfg, "skills", "deploy", "SKILL.md"), "---\nname: deploy\ndescription: \"Ship a release to production\"\nkeywords: release, ship\n---\n# Deploy\nglobal steps\n")
	write(t, filepath.Join(cwd, ".janetcode", "skills", "deploy.md"), "---\ndescription: Project deploy procedure with the ansible playbook\n---\nproject steps\n")
	write(t, filepath.Join(cwd, ".claude", "skills", "sql-migrations", "SKILL.md"), "# SQL migrations\nUse goose. Never edit an applied migration.\n")
	write(t, filepath.Join(cwd, ".janetcode", "skills", "empty.md"), "---\nname: empty\n---\n\n")
	write(t, filepath.Join(cwd, ".janetcode", "skills", "notes.txt"), "ignored")
	skills := Load(cwd, cfg)
	if len(skills) != 2 {
		t.Fatalf("skills = %+v", skills)
	}
	dep, mig := skills[0], skills[1]
	if dep.Name != "deploy" || dep.Source != "project" || dep.Body != "project steps" || dep.Description != "Project deploy procedure with the ansible playbook" || len(dep.Keywords) != 0 {
		t.Fatalf("deploy = %+v", dep)
	}
	if mig.Name != "sql-migrations" || mig.Source != "claude" || mig.Description != "SQL migrations" || !strings.Contains(mig.Body, "goose") {
		t.Fatalf("migrations = %+v", mig)
	}
	meta, body := Frontmatter("---\r\nname: 'x y'\r\n---\r\nbody\n")
	if meta["name"] != "x y" || strings.TrimSpace(body) != "body" {
		t.Fatalf("crlf frontmatter: %q %q", meta, body)
	}
	if meta, body := Frontmatter("no front\n---\n"); len(meta) != 0 || body != "no front\n---\n" {
		t.Fatalf("plain text mangled: %q %q", meta, body)
	}
	if len(Load("", "")) != 0 {
		t.Fatal("nothing should load from nowhere")
	}
}

func TestSelectNeedsClearSignal(t *testing.T) {
	skills := []Skill{
		{Name: "deploy", Description: "Ship a release to production", Keywords: []string{"release"}},
		{Name: "sql-migrations", Description: "Write database schema migrations with goose"},
		{Name: "review", Description: "Review a pull request for correctness and style"},
		{Name: "docs", Description: "Write documentation for the project"},
	}
	got := Select(skills, "please deploy the new build", 5)
	if len(got) != 1 || got[0].Name != "deploy" {
		t.Fatalf("name match: %+v", got)
	}
	got = Select(skills, "cut a release today", 5)
	if len(got) != 1 || got[0].Name != "deploy" {
		t.Fatalf("keyword match: %+v", got)
	}
	got = Select(skills, "add a database schema migration for users", 5)
	if len(got) != 1 || got[0].Name != "sql-migrations" {
		t.Fatalf("description match: %+v", got)
	}
	if got = Select(skills, "fix the typo in main.go", 5); len(got) != 0 {
		t.Fatalf("weak signal selected: %+v", got)
	}
	if got = Select(skills, "write the project docs and review the pull request", 1); len(got) != 1 {
		t.Fatalf("max not applied: %+v", got)
	}
	if Select(skills, "", 5) != nil {
		t.Fatal("empty text selected something")
	}
}

func TestInjectorAttachesOncePerWindow(t *testing.T) {
	skills := []Skill{{Name: "deploy", Description: "Ship a release", Body: "steps", Path: "/s/deploy.md"}}
	in := &Injector{Skills: func() []Skill { return skills }}
	blocks, names := in.Prepare([]llm.Block{{Type: llm.BlockText, Text: "deploy it"}})
	if len(blocks) != 2 || len(names) != 1 || !strings.HasPrefix(blocks[1].Text, `<skill name="deploy" source="/s/deploy.md">`) || !strings.Contains(blocks[1].Text, "steps") {
		t.Fatalf("blocks=%+v names=%v", blocks, names)
	}
	for i := 0; i < freshTurns-1; i++ {
		if b, n := in.Prepare([]llm.Block{{Type: llm.BlockText, Text: "deploy again"}}); len(b) != 1 || len(n) != 0 {
			t.Fatalf("turn %d re-attached", i)
		}
	}
	if _, n := in.Prepare([]llm.Block{{Type: llm.BlockText, Text: "deploy once more"}}); len(n) != 1 {
		t.Fatal("not re-attached after the window")
	}
	var nilIn *Injector
	if b, n := nilIn.Prepare([]llm.Block{{Type: llm.BlockText, Text: "deploy"}}); len(b) != 1 || n != nil {
		t.Fatal("nil injector changed blocks")
	}
	if idx := Index(skills); !strings.Contains(idx, "- deploy: Ship a release") || Index(nil) != "" {
		t.Fatalf("index = %q", idx)
	}
}

func TestToolReadsSkillByName(t *testing.T) {
	tl := &Tool{Skills: func() []Skill { return []Skill{{Name: "deploy", Body: "steps", Path: "p"}} }}
	out, err := tl.Run(context.Background(), json.RawMessage(`{"name":"Deploy"}`))
	if err != nil || !strings.Contains(out, "steps") {
		t.Fatalf("%q %v", out, err)
	}
	if _, err := tl.Run(context.Background(), json.RawMessage(`{"name":"nope"}`)); err == nil || !strings.Contains(err.Error(), "available: deploy") {
		t.Fatalf("err = %v", err)
	}
}

func TestLoadProfiles(t *testing.T) {
	cwd, cfg := t.TempDir(), t.TempDir()
	write(t, filepath.Join(cfg, "agents", "reviewer.md"), "---\ndescription: Reviews code\ntools: read_file, grep\nmodel: cheap-model\n---\nYou review code.\n")
	write(t, filepath.Join(cwd, ".claude", "agents", "reviewer.md"), "---\nname: reviewer\ndescription: Project reviewer\ntools: [grep]\n---\nProject rules apply.\n")
	write(t, filepath.Join(cwd, ".janetcode", "agents", "empty.md"), "---\nname: empty\n---\n")
	ps := LoadProfiles(cwd, cfg)
	if len(ps) != 1 {
		t.Fatalf("profiles = %+v", ps)
	}
	p := ps[0]
	if p.Name != "reviewer" || p.Source != "claude" || p.Description != "Project reviewer" || p.Body != "Project rules apply." || len(p.Tools) != 1 || p.Tools[0] != "grep" {
		t.Fatalf("profile = %+v", p)
	}
}
