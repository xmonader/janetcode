package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/agent"
	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/memory"
	"github.com/xmonader/janetcode/internal/prompt"
	"github.com/xmonader/janetcode/internal/repomap"
	"github.com/xmonader/janetcode/internal/rules"
	"github.com/xmonader/janetcode/internal/skill"
	"github.com/xmonader/janetcode/internal/tool"
)

// TestLeannessTable prints what the first request of a session costs,
// section by section and tool by tool. It is a measurement, not an
// assertion: run it with -v and JANETCODE_MEASURE=<dir> to point it at a
// project other than this one. The guards that keep the numbers from
// creeping are TestFirstRequestSystemPromptCeiling and
// TestToolSchemaBytesCeiling.
func TestLeannessTable(t *testing.T) {
	dir := os.Getenv("JANETCODE_MEASURE")
	if dir == "" {
		dir = repoRootFor(t)
	}
	cfgDir := t.TempDir()

	base := prompt.System(dir)
	sections := splitSystem(base)

	mem := memory.Open(t.TempDir(), dir)
	skills := func() []skill.Skill { return skill.Load(dir, cfgDir) }
	memIndex := mem.Index()
	skillIndex := skill.IndexWithin(skills(), 0)

	var ruleBytes int
	var ruleNames []string
	for _, r := range rules.Load(dir, cfgDir) {
		ruleBytes += len(r.Body)
		ruleNames = append(ruleNames, r.Name)
	}

	repo := &repomap.Cache{Root: dir}
	prep := repomap.TurnPrep(repo, 0)
	blocks, notice := prep(context.Background(), []llm.Block{{Type: llm.BlockText, Text: "add a flag"}})
	var mapBytes int
	for _, b := range blocks[1:] {
		mapBytes += len(b.Text)
	}

	t.Logf("project: %s", dir)
	t.Logf("%-42s %9s %9s", "section", "bytes", "~tokens")
	row := func(name string, n int) {
		t.Logf("%-42s %9d %9d", name, n, n/4)
	}
	for _, s := range sections {
		row("system: "+s.name, len(s.text))
	}
	row("system: TOTAL", len(base))
	row("extra: memory index", len(memIndex))
	row("extra: skill index", len(skillIndex))
	row(fmt.Sprintf("rules from path (%d files)", len(ruleNames)), ruleBytes)
	row("repomap attached to turn 1", mapBytes)
	if notice != "" {
		row("repomap notice", len(notice))
	}

	defs := toolDefs(dir, repo)
	total := 0
	type td struct {
		name string
		n    int
	}
	var list []td
	for _, d := range defs {
		n := len(d.Description) + len(d.InputSchema) + len(d.Name)
		list = append(list, td{d.Name, n})
		total += n
	}
	sort.Slice(list, func(i, j int) bool { return list[i].n > list[j].n })
	t.Logf("")
	t.Logf("%-42s %9s %9s", fmt.Sprintf("tool schemas (%d tools)", len(defs)), "bytes", "~tokens")
	for _, d := range list {
		row("  "+d.name, d.n)
	}
	row("tool schemas: TOTAL", total)
}

// The two ceilings below are the guard on the first request: the bytes
// every session pays whatever the project, before a file has been read.
// They are set just above what was measured on 2026-09-11. They are not
// aspirations — a change that needs more room than this is a change that
// should be argued for, and moving the number is the argument.
//
// Raising one means saying in the commit message what the extra bytes
// buy, measured. Lowering one after a cut is free and welcome.
const (
	// emptyProjectSystemCeiling is the whole system prompt for a project
	// with no git, no instruction file and no memory — the base prefix
	// every session pays before a single file is read. Measured 5,777
	// bytes on 2026-09-15 (feat/measurement); the ceiling is 6,000 so a
	// new feature cannot quietly eat the leanness advantage.
	// 6,198 on 2026-10-09: the post-green rule (no harness of your own,
	// fix a wrong check once). On effbench a-build-incremental the model
	// spent 10 of 27 calls debugging a 305-line scratch harness after the
	// public tests passed; hero_agent measured the same class at up to 61
	// calls a campaign. 198 bytes, cached, against those calls.
	// 6,355 on 2026-11-02: the post-green testing rules from hero_agent's
	// CODING_RULES (assert what the task states, a few edge asserts, skip
	// for a small change to a short request). 135 bytes in the cached
	// head against the edge-rule misses lean runs took there (tests of a
	// neighbouring case, or of the code's own output).
	emptyProjectSystemCeiling = 6360
	// toolSchemaCeiling is name + description + schema over every tool a
	// full session offers: 13,862 bytes for seventeen, after the
	// duplicated edit_file locator came out. 14,707 on 2026-09-29, when
	// bash took stdin and interactive and the job tool's stdin learned
	// eof: commands that ask questions stopped failing or hanging.
	// 14,941 on 2026-10-08, when write_file and edit_file took then_run:
	// 221 bytes a request (mostly cached) against a whole round trip
	// saved on every edit that is followed by its test run.
	toolSchemaCeiling = 14960
)

// TestFirstRequestSystemPromptCeiling: an empty directory is the floor of
// what any session pays, before a single file is read.
func TestFirstRequestSystemPromptCeiling(t *testing.T) {
	s := prompt.System(t.TempDir())
	t.Logf("empty-project system prompt: %d bytes (ceiling %d)", len(s), emptyProjectSystemCeiling)
	if len(s) > emptyProjectSystemCeiling {
		t.Fatalf("the system prompt for an empty project is %d bytes, over the ceiling of %d; "+
			"say in the commit message what the extra %d bytes buy, then raise it",
			len(s), emptyProjectSystemCeiling, len(s)-emptyProjectSystemCeiling)
	}
}

// TestToolSchemaBytesCeiling: the tool definitions are re-sent with every
// request of every turn, so they are the most expensive text in the
// harness per byte written.
func TestToolSchemaBytesCeiling(t *testing.T) {
	dir := t.TempDir()
	defs := toolDefs(dir, &repomap.Cache{Root: dir})
	total := 0
	for _, d := range defs {
		total += len(d.Name) + len(d.Description) + len(d.InputSchema)
	}
	t.Logf("tool schemas: %d bytes over %d tools (ceiling %d)", total, len(defs), toolSchemaCeiling)
	if total > toolSchemaCeiling {
		t.Fatalf("the tool definitions are %d bytes, over the ceiling of %d; "+
			"say in the commit message what the extra %d bytes buy, then raise it",
			total, toolSchemaCeiling, total-toolSchemaCeiling)
	}
}

// splitSystem cuts the system prompt at its headings so each part can be
// weighed on its own.
type section struct{ name, text string }

func splitSystem(s string) []section {
	marks := []struct{ head, name string }{
		{"You are janetcode", "base prompt"},
		{"# The harness you are running in", "harness"},
		{"# Environment", "environment"},
		{"# Project instructions", "project instructions"},
	}
	var out []section
	for i, m := range marks {
		idx := strings.Index(s, m.head)
		if idx < 0 {
			continue
		}
		end := len(s)
		for _, n := range marks[i+1:] {
			if j := strings.Index(s, n.head); j > idx {
				end = j
				break
			}
		}
		out = append(out, section{m.name, s[idx:end]})
	}
	return out
}

// toolDefs builds the same registry a real session builds, so the schema
// bytes below are the ones the model is actually sent.
func toolDefs(dir string, repo *repomap.Cache) []llm.ToolDef {
	seen := &tool.Seen{}
	jobs := &tool.Jobs{}
	expand := &agent.ExpandOutput{}
	r := tool.NewRegistry(
		tool.ReadFile{Root: dir, Seen: seen},
		tool.WriteFile{Root: dir, Seen: seen, ThenRun: true},
		tool.EditFile{Root: dir, Seen: seen, ThenRun: true},
		tool.Bash{Root: dir, Cache: &tool.ShellCache{}, Jobs: jobs},
		tool.JobTool{Jobs: jobs},
		tool.FetchURL{},
		expand,
		tool.ListDir{Root: dir},
		tool.Glob{Root: dir},
		tool.Grep{Root: dir},
		repomap.Tool{Cache: repo},
	)
	ag := &agent.Agent{}
	r.Add(&agent.GoalStatus{Parent: ag})
	r.Add(&agent.TodoTool{Parent: ag})
	r.Add(&agent.Script{Parent: ag, Root: dir})
	r.Add(&skill.Tool{Skills: func() []skill.Skill { return nil }})
	r.Add(&memory.Tool{})
	r.Add(&tool.ToolSearch{Registry: r})
	return r.Defs()
}

// repoRootFor finds the module root from this source file's own path
// rather than from the working directory: another test in this package
// may have chdir'd, and the measurement must not depend on the order.
func repoRootFor(t *testing.T) string {
	t.Helper()
	_, self, _, ok := runtime.Caller(0)
	if !ok {
		t.Skip("no source path")
	}
	for d := filepath.Dir(self); d != "/" && d != "."; d = filepath.Dir(d) {
		if _, err := os.Stat(filepath.Join(d, "go.mod")); err == nil {
			return d
		}
	}
	t.Skip("module root not found")
	return ""
}
