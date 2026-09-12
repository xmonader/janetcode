package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/tool"
)

func TestWorkflowStartsRealTaskChild(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("alpha"), 0o644); err != nil {
		t.Fatal(err)
	}
	parent := &Agent{Provider: &scriptedByPrompt{}, Model: "m", System: "sys", AutoApprove: true}
	task := &Task{Parent: parent}
	wf := &Workflow{Parent: parent}
	parent.Tools = tool.NewRegistry(tool.ReadFile{Root: root}, task, wf)
	out, err := wf.Run(context.Background(), json.RawMessage(`{"description":"fan","script":[{"description":"one","prompt":"explore one"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "REPORT: a.txt says") {
		t.Fatalf("missing child report: %q", out)
	}
	ids := task.ChildIDs()
	if len(ids) != 1 {
		t.Fatalf("children = %v", ids)
	}
	if !strings.Contains(out, ids[0]) {
		t.Fatalf("report should name child id %s: %q", ids[0], out)
	}
}

func TestReadOnlyChildDoesNotGetWorkflow(t *testing.T) {
	parent := &Agent{Provider: &scriptedByPrompt{}, Model: "m", System: "sys", AutoApprove: true}
	task := &Task{Parent: parent}
	wf := &Workflow{Parent: parent}
	parent.Tools = tool.NewRegistry(tool.ReadFile{Root: t.TempDir()}, task, wf)
	child, err := task.child(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := child.Tools.Get("workflow"); ok {
		t.Fatal("read-only child must not inherit workflow")
	}
	if _, ok := child.Tools.Get("read_file"); !ok {
		t.Fatal("read-only child should still have read_file")
	}
}
