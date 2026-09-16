package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/tool"
)

func TestWithheldToolIsNotATypo(t *testing.T) {
	reg := tool.NewRegistry(tool.ReadFile{Root: t.TempDir()})
	reg.Withhold("bash", "excluded by the explore agent's disallowed_tools")
	a := &Agent{Tools: reg, AutoApprove: true}
	out, _, isErr, err := a.executeTool(context.Background(), llm.Block{ID: "c1", Name: "bash", Input: []byte(`{"command":"ls"}`)}, func(Event) {})
	if err != nil {
		t.Fatal(err)
	}
	if !isErr || !strings.Contains(out, "withheld by policy") || !strings.Contains(out, "do not search") {
		t.Fatalf("out = %q", out)
	}
	if strings.Contains(out, "Unknown tool") {
		t.Fatalf("withheld reported as unknown: %q", out)
	}
}

func TestUnknownToolStillSaysUnknown(t *testing.T) {
	a := &Agent{Tools: tool.NewRegistry(tool.ReadFile{Root: t.TempDir()}), AutoApprove: true}
	out, _, isErr, err := a.executeTool(context.Background(), llm.Block{ID: "c1", Name: "nope", Input: []byte(`{}`)}, func(Event) {})
	if err != nil {
		t.Fatal(err)
	}
	if !isErr || !strings.Contains(out, "Unknown tool") {
		t.Fatalf("out = %q", out)
	}
}
