package agent

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/xmonader/janetcode/internal/tool"
)

// A subagent runs its own model calls, on the parent's account. Until
// they were billed to the parent, the session total, /stats and the cost
// ceiling all stopped at what the main conversation itself paid — so a
// run under --max-cost could delegate its way past the ceiling.
func TestSubagentSpendIsBilledToTheParent(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("alpha"), 0o644); err != nil {
		t.Fatal(err)
	}
	parent := &Agent{Provider: &scriptedByPrompt{}, Model: "m", System: "sys", AutoApprove: true}
	task := &Task{Parent: parent}
	parent.Tools = tool.NewRegistry(tool.ReadFile{Root: root}, tool.WriteFile{Root: root}, task)

	if err := parent.Run(context.Background(), "go", nil); err != nil {
		t.Fatal(err)
	}
	kids := task.Usage()
	if kids.InputTokens == 0 {
		t.Fatal("the children spent nothing; the test proves nothing")
	}
	session := parent.UsageSnapshot()
	if session.InputTokens < kids.InputTokens || session.OutputTokens < kids.OutputTokens {
		t.Fatalf("session usage %+v does not include the subagents' %+v", session, kids)
	}
}
