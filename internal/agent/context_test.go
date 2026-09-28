package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/tool"
)

// TestContextReportsTheThresholdInForce holds /context to the threshold
// compaction actually uses. It read compact_at alone and fell back to the
// default, so a model with a 32k window — compacted at 24k — was reported
// as compacting at 80k.
func TestContextReportsTheThresholdInForce(t *testing.T) {
	a := &Agent{Tools: tool.NewRegistry()}
	a.ContextWindow = 32_000
	if got, want := a.Context().CompactAt, a.CompactThreshold(); got != want || got != 24_000 {
		t.Fatalf("report says %d, compaction runs at %d", got, want)
	}
}

func TestPinTextPassesThroughPlainText(t *testing.T) {
	got, err := PinText(t.TempDir(), "remember this")
	if err != nil || got != "remember this" {
		t.Fatalf("got %q err=%v", got, err)
	}
}

func TestPinTextLoadsAtPath(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := PinText(dir, "@notes.txt")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(got, "File notes.txt as of pinning:\n") || !strings.Contains(got, "hello") {
		t.Fatalf("got %q", got)
	}
}

func TestPinTextTruncatesLargeFiles(t *testing.T) {
	dir := t.TempDir()
	big := strings.Repeat("x", maxPinFile+100)
	if err := os.WriteFile(filepath.Join(dir, "big.txt"), []byte(big), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := PinText(dir, "@big.txt")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(got, "… (truncated)") {
		t.Fatalf("not truncated: %q", got[len(got)-40:])
	}
}

func TestPinTextWithSpaceIsNotAPath(t *testing.T) {
	// "@" followed by a space is a note that happens to start with @, not
	// a file reference.
	got, err := PinText(t.TempDir(), "@ mention someone")
	if err != nil || got != "@ mention someone" {
		t.Fatalf("got %q err=%v", got, err)
	}
}

func TestPinTextMissingFile(t *testing.T) {
	if _, err := PinText(t.TempDir(), "@nope.txt"); err == nil {
		t.Fatal("want error for a missing file")
	}
}

func TestContextSplitsThePrefix(t *testing.T) {
	a, _ := newAgent(t, &fake{}, nil)
	a.System = "You are janetcode.\n# Environment\n- Date: today\n# Project instructions (from AGENTS.md)\ndo not rewrite history\n"
	a.SystemExtra = func() string {
		return "\n# Memory\n- user: likes tabs\n\n# Skills\n- gofmt: run gofmt\n"
	}
	a.PlanMode = true
	a.ReadOnly = true
	r := a.Context()
	if r.Base == 0 || r.Project == 0 {
		t.Fatalf("base/project = %d/%d, want both set", r.Base, r.Project)
	}
	if r.Base+r.Project != r.System {
		t.Fatalf("base %d + project %d != system %d", r.Base, r.Project, r.System)
	}
	if r.Memory == 0 || r.Skills == 0 {
		t.Fatalf("memory/skills = %d/%d, want both set", r.Memory, r.Skills)
	}
	if r.Memory+r.Skills != r.Extra {
		t.Fatalf("memory %d + skills %d != extra %d", r.Memory, r.Skills, r.Extra)
	}
	if r.Modes == 0 {
		t.Fatal("plan/read-only mode text missing from Modes")
	}
	lines := r.PrefixLines()
	for _, want := range []string{"base", "project", "modes", "memory", "skills", "pins"} {
		if !strings.Contains(lines, want) {
			t.Fatalf("PrefixLines missing %q:\n%s", want, lines)
		}
	}
}

func TestSplitPromptAndExtra(t *testing.T) {
	base, project := splitPromptTokens("hello\n# Project instructions (from X)\nworld")
	if base == 0 || project == 0 {
		t.Fatalf("base=%d project=%d", base, project)
	}
	base, project = splitPromptTokens("no project here")
	if project != 0 || base == 0 {
		t.Fatalf("no-project: base=%d project=%d", base, project)
	}
	mem, skills := splitExtraTokens("\n# Memory\n- a\n\n# Skills\n- b\n")
	if mem == 0 || skills == 0 {
		t.Fatalf("mem=%d skills=%d", mem, skills)
	}
	mem, skills = splitExtraTokens("\n# Memory\n- a\n")
	if mem == 0 || skills != 0 {
		t.Fatalf("memory-only: mem=%d skills=%d", mem, skills)
	}
	mem, skills = splitExtraTokens("\n# Skills\n- b\n")
	if mem != 0 || skills == 0 {
		t.Fatalf("skills-only: mem=%d skills=%d", mem, skills)
	}
	mem, skills = splitExtraTokens("unlabelled extra")
	if mem != 0 || skills != 0 {
		t.Fatalf("unlabelled: mem=%d skills=%d", mem, skills)
	}
}
