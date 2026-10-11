package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/tool"
)

// setupTourAgent runs one turn that changes two existing files: c1
// changes what core.go computes, c2 only reformats style.go.
func setupTourAgent(t *testing.T) (*Agent, string) {
	t.Helper()
	f := &fake{responses: []*llm.Response{
		{Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{
			{Type: llm.BlockText, Text: "Make F add instead of multiply."},
			{Type: llm.BlockToolUse, ID: "c1", Name: "write_file", Input: []byte(`{"path":"core.go","content":"package m\n\nfunc F(x int) int { return x + 2 }\n"}`)},
		}}, StopReason: llm.StopToolUse},
		{Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{
			{Type: llm.BlockText, Text: "Tidy the spacing."},
			{Type: llm.BlockToolUse, ID: "c2", Name: "write_file", Input: []byte(`{"path":"style.go","content":"package m\n\nfunc G(x int) int { return x * 2 }\n"}`)},
		}}, StopReason: llm.StopToolUse},
		text("done"),
	}}
	a, root := newAgent(t, f, nil)
	for name, body := range map[string]string{
		"core.go":  "package m\n\nfunc F(x int) int { return x * 2 }\n",
		"style.go": "package m\n\nfunc G(x int) int { return x*2 }\n",
	} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	a.AutoApprove = true
	a.Tools = tool.NewRegistry(tool.WriteFile{Root: root}, tool.EditFile{Root: root})
	if err := a.Run(context.Background(), "go", nil); err != nil {
		t.Fatal(err)
	}
	if len(a.Changes()) != 2 {
		t.Fatalf("setup: changes = %+v", a.Changes())
	}
	return a, root
}

func TestTourBriefAndText(t *testing.T) {
	a, root := setupTourAgent(t)
	var asked []string
	a.Reach = func(_ context.Context, path, _, _ string) int {
		asked = append(asked, filepath.Base(path))
		return 7
	}
	if b := a.TourBrief(); !strings.Contains(b, "1 hunk (1 behaviour)") || !strings.Contains(b, "mechanical collapsed") {
		t.Errorf("brief = %q", b)
	}
	// Auto-approved, never shown: both lines of the behaviour change are
	// debt; the reformat is not.
	if d := a.DebtLine(root); !strings.Contains(d, "2 lines approved unread") {
		t.Errorf("debt = %q", d)
	}
	out := a.TourText(context.Background(), "", root)
	for _, want := range []string{"review tour of turn 1", "[behaviour] core.go", "reaches 7 uses", "intent: Make F add instead of multiply.", "/tour all shows them"} {
		if !strings.Contains(out, want) {
			t.Errorf("tour lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "style.go") {
		t.Errorf("mechanical hunk shown without all:\n%s", out)
	}
	// Reach is asked only about files with a behaviour hunk.
	if len(asked) != 1 || asked[0] != "core.go" {
		t.Errorf("reach asked about %v", asked)
	}
	// Having been toured, the turn is no longer debt.
	if d := a.DebtLine(root); d != "" {
		t.Errorf("debt after the tour = %q", d)
	}
	if all := a.TourText(context.Background(), "1 all", root); !strings.Contains(all, "[format] style.go") {
		t.Errorf("tour all:\n%s", all)
	}
	if got := a.TourText(context.Background(), "x", root); !strings.HasPrefix(got, "usage:") {
		t.Errorf("bad arg = %q", got)
	}
	if got := a.TourText(context.Background(), "9", root); got != "turn 9 changed no files" {
		t.Errorf("empty turn = %q", got)
	}
}

// An edit approved with its whole diff on screen is not debt; nor is one
// shown afterwards by /changes <n>.
func TestDebtClearedByReview(t *testing.T) {
	a, root := setupTourAgent(t)
	a.MarkReviewed("c2") // the reformat: not debt either way
	if d := a.DebtLine(root); !strings.Contains(d, "2 lines") {
		t.Errorf("debt = %q", d)
	}
	a.MarkReviewed("c1")
	if d := a.DebtLine(root); d != "" {
		t.Errorf("debt after the prompt review = %q", d)
	}

	b, root := setupTourAgent(t)
	b.MarkChangeReviewed(1)
	if d := b.DebtLine(root); d != "" {
		t.Errorf("debt after /changes 1 = %q", d)
	}
}

func TestTourOff(t *testing.T) {
	a, root := setupTourAgent(t)
	a.Off = map[string]bool{"tour": true}
	if b := a.TourBrief(); b != "" {
		t.Errorf("brief with tour off = %q", b)
	}
	if d := a.DebtLine(root); d != "" {
		t.Errorf("debt with tour off = %q", d)
	}
	if got := a.TourText(context.Background(), "", root); !strings.Contains(got, "off") {
		t.Errorf("text with tour off = %q", got)
	}
	if !KnownSwitch("tour") {
		t.Error("tour is not a known switch")
	}
}

// A prompt that could not build its preview showed nothing to read, so
// approving it is not reading it.
func TestPreviewShown(t *testing.T) {
	for _, c := range []struct {
		preview string
		want    bool
	}{
		{"", false},
		{"preview unavailable: no such file", false},
		{"--- a.go\n+++ a.go\n@@ -1 +1 @@\n-a\n+b\n", true},
	} {
		if got := (ToolCall{Preview: c.preview}).PreviewShown(); got != c.want {
			t.Errorf("PreviewShown(%q) = %v", c.preview, got)
		}
	}
}
