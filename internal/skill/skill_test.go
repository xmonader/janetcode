package skill

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/memory"
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
	write(t, filepath.Join(cwd, ".claude", "skills", "sql-migrations", "SKILL.md"), "# SQL migrations\nUse goose. Never edit an applied migration.\n## Adding a database schema migration\n")
	write(t, filepath.Join(cwd, ".janetcode", "skills", "empty.md"), "---\nname: empty\n---\n\n")
	write(t, filepath.Join(cwd, ".janetcode", "skills", "notes.txt"), "ignored")
	write(t, filepath.Join(cwd, ".janetcode", "skills", "release", "SKILL.md"), "---\ndisable-model-invocation: true\n---\n# Cutting a release\nRun scripts/cut.sh $ARGUMENTS\n")
	write(t, filepath.Join(cwd, ".janetcode", "skills", "release", "scripts", "cut.sh"), "#!/bin/sh\n")
	write(t, filepath.Join(cwd, ".janetcode", "skills", "release", ".hidden"), "x")
	skills := Load(cwd, cfg)
	if len(skills) != 3 {
		t.Fatalf("skills = %+v", skills)
	}
	dep, rel, mig := skills[0], skills[1], skills[2]
	if rel.Name != "release" || rel.ModelInvocable || !rel.UserInvocable || len(rel.Files) != 1 || rel.Files[0] != "scripts/cut.sh" || rel.Description != "Cutting a release" {
		t.Fatalf("release = %+v", rel)
	}
	if b := rel.Block().Text; !strings.Contains(b, "Files shipped with this skill") || !strings.Contains(b, filepath.Join(rel.Dir, "scripts/cut.sh")) {
		t.Fatalf("block = %q", b)
	}
	if x := rel.Expand("v1.2"); !strings.Contains(x, "Run scripts/cut.sh v1.2") {
		t.Fatalf("expand = %q", x)
	}
	if idx := Index(skills); strings.Contains(idx, "release") || !strings.Contains(idx, "- deploy:") {
		t.Fatalf("index lists a model-hidden skill: %q", idx)
	}
	if got := Select(skills, "how do I cut a release?", 5); len(got) != 0 {
		t.Fatalf("model-hidden skill selected: %+v", got)
	}
	if got := Select(skills, "add a database schema migration", 5); len(got) != 1 || got[0].Name != "sql-migrations" {
		t.Fatalf("heading/description match: %+v", got)
	}
	if dep.Name != "deploy" || dep.Source != "project" || dep.Body != "project steps" || dep.Description != "Project deploy procedure with the ansible playbook" || len(dep.Keywords) != 0 {
		t.Fatalf("deploy = %+v", dep)
	}
	if mig.Name != "sql-migrations" || mig.Source != "claude" || mig.Description != "SQL migrations" || !strings.Contains(mig.Body, "goose") || !mig.ModelInvocable || !mig.UserInvocable {
		t.Fatalf("migrations = %+v", mig)
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
		{Name: "hidden", Description: "Deploy a release to production too"},
	}
	for i := range skills {
		skills[i].ModelInvocable = i != 4
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
	skills[0].ModelInvocable = true
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
	tl := &Tool{Skills: func() []Skill { return []Skill{{Name: "deploy", Body: "steps", Path: "p", ModelInvocable: true}} }}
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

func TestBuiltinExploreNeedsNoFile(t *testing.T) {
	ps := MergeProfiles(Builtins(), nil)
	var explore *Profile
	for i := range ps {
		if ps[i].Name == "explore" {
			explore = &ps[i]
			break
		}
	}
	if explore == nil || explore.Source != "builtin" || explore.Body == "" {
		t.Fatalf("builtin explore missing: %+v", ps)
	}
	user := []Profile{{Name: "explore", Description: "project", Body: "override", Source: "project"}}
	merged := MergeProfiles(Builtins(), user)
	if len(merged) != 1 || merged[0].Source != "project" || merged[0].Body != "override" {
		t.Fatalf("overlay should win: %+v", merged)
	}
}

func TestTurnPrepAttachesSkillsAndMemory(t *testing.T) {
	mem := memory.Open(t.TempDir(), t.TempDir())
	mem.Put(memory.Entry{Name: "goose", Description: "Database migrations use goose", Body: "goose up from db/"})
	mem.Put(memory.Entry{Name: "unrelated", Description: "Deploys use ansible", Body: "ansible-playbook"})
	in := &Injector{Skills: func() []Skill {
		return []Skill{{Name: "sql-migrations", Description: "Write database schema migrations", Body: "steps", ModelInvocable: true}}
	}}
	prep := TurnPrep(in, mem)
	blocks, note := prep([]llm.Block{{Type: llm.BlockText, Text: "add a database schema migration for users"}})
	if len(blocks) != 3 || !strings.HasPrefix(blocks[1].Text, "<skill name=\"sql-migrations\"") || !strings.HasPrefix(blocks[2].Text, "<memory name=\"goose\"") {
		t.Fatalf("blocks = %+v", blocks)
	}
	if note != "skills attached: sql-migrations · memory recalled: goose" {
		t.Fatalf("note = %q", note)
	}
	if e, _ := mem.Get("goose"); e.Uses != 1 {
		t.Fatalf("uses = %d", e.Uses)
	}
	if blocks, note = prep([]llm.Block{{Type: llm.BlockText, Text: "hello"}}); len(blocks) != 1 || note != "" {
		t.Fatalf("unrelated turn got %+v %q", blocks, note)
	}
}
