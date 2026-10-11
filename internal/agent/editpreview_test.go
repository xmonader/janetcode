package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/tool"
)

// Two edits of one file in one reply: the second one's line numbers are
// written against the file the first leaves. Its preview, which the
// approval prompt shows, is worked out when its turn comes, after the
// first has run, so it is not refused against the stale file ("to_line 8
// is past the end of the file, which has 7 line(s)").
func TestEditPreviewSeesTheEarlierEditOfTheBatch(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "f.txt")
	if err := os.WriteFile(path, []byte("1\n2\n3\n4\n5\n6\n7\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	f := &fake{responses: []*llm.Response{
		{Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{
			{Type: llm.BlockToolUse, ID: "e1", Name: "edit_file", Input: json.RawMessage(`{"path":"f.txt","from_line":7,"to_line":7,"new_string":"7\n8"}`)},
			{Type: llm.BlockToolUse, ID: "e2", Name: "edit_file", Input: json.RawMessage(`{"path":"f.txt","from_line":8,"to_line":8,"new_string":"eight"}`)},
		}}, StopReason: llm.StopToolUse},
		text("done"),
	}}
	previews := map[string]string{}
	a := &Agent{
		Provider: f,
		Tools:    tool.NewRegistry(tool.EditFile{Root: root}),
		System:   "sys",
		Model:    "m",
	}
	a.Approver = ApproverFunc(func(ctx context.Context, call ToolCall) (Decision, error) {
		previews[call.ID] = call.Preview
		return Allow, nil
	})
	if err := a.Run(context.Background(), "edit", nil); err != nil {
		t.Fatal(err)
	}
	if p := previews["e2"]; strings.Contains(p, "preview unavailable") || !strings.Contains(p, "+eight") {
		t.Fatalf("second edit's preview = %q", p)
	}
	data, _ := os.ReadFile(path)
	if string(data) != "1\n2\n3\n4\n5\n6\n7\neight\n" {
		t.Fatalf("file = %q", data)
	}
}
