package headless

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/xmonader/janetcode/internal/agent"
	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/session"
	"github.com/xmonader/janetcode/internal/tool"
)

// bgRun runs one turn whose model starts a background command, then
// answers, and returns the stream-json lines and stderr.
func bgRun(t *testing.T, ctx context.Context, command string, opts Options) (lines []map[string]any, stderr string, took time.Duration, err error) {
	t.Helper()
	root := t.TempDir()
	jobs := &tool.Jobs{Dir: t.TempDir()}
	in, _ := json.Marshal(map[string]any{"command": command, "background": true})
	f := &fake{responses: []*llm.Response{
		{Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{
			{Type: llm.BlockToolUse, ID: "b1", Name: "bash", Input: in},
		}}, StopReason: llm.StopToolUse},
		{Message: llm.TextMessage(llm.RoleAssistant, "started"), StopReason: llm.StopEndTurn},
	}}
	ag := &agent.Agent{Provider: f, Tools: tool.NewRegistry(tool.Bash{Root: root, Jobs: jobs}), Model: "m", Jobs: jobs, AutoApprove: true}
	sess, serr := session.New(t.TempDir(), root, "fake", "m")
	if serr != nil {
		t.Fatal(serr)
	}
	devnull, _ := os.Open(os.DevNull)
	defer devnull.Close()
	var out, errOut bytes.Buffer
	opts.Agent, opts.Session, opts.Prompt = ag, sess, "go"
	opts.Stdout, opts.Stderr, opts.Stdin, opts.Format = &out, &errOut, devnull, FormatStreamJSON
	start := time.Now()
	err = Run(ctx, opts)
	took = time.Since(start)
	for _, l := range strings.Split(strings.TrimSpace(out.String()), "\n") {
		var m map[string]any
		if json.Unmarshal([]byte(l), &m) != nil {
			t.Fatalf("bad line %q", l)
		}
		lines = append(lines, m)
	}
	return lines, errOut.String(), took, err
}

func bgLines(lines []map[string]any) (jobs []map[string]any, results int) {
	for _, l := range lines {
		switch l["type"] {
		case "background_job":
			jobs = append(jobs, l)
		case "result":
			results++
		}
	}
	return
}

func TestRunWaitsForBackgroundJob(t *testing.T) {
	lines, stderr, took, err := bgRun(t, context.Background(), "sleep 1", Options{})
	if err != nil {
		t.Fatal(err)
	}
	if took < 900*time.Millisecond {
		t.Fatalf("returned after %s, before the job ended", took)
	}
	if !strings.Contains(stderr, "[background] waiting for 1 job(s): 1 (sleep 1)") {
		t.Fatalf("stderr = %q", stderr)
	}
	jobs, results := bgLines(lines)
	if len(jobs) != 2 || jobs[0]["status"] != "running" || jobs[1]["status"] != "finished" || jobs[1]["exit_code"] != float64(0) {
		t.Fatalf("job lines = %v", jobs)
	}
	if results != 1 || lines[len(lines)-1]["type"] != "result" {
		t.Fatalf("want exactly one result, last: %v", lines)
	}
}

func TestRunNoWaitKillsBackgroundJob(t *testing.T) {
	lines, stderr, took, err := bgRun(t, context.Background(), "sleep 30", Options{NoWaitBackground: true})
	if err != nil {
		t.Fatal(err)
	}
	if took > 10*time.Second || strings.Contains(stderr, "waiting") {
		t.Fatalf("took %s, stderr %q", took, stderr)
	}
	jobs, results := bgLines(lines)
	if len(jobs) != 2 || jobs[1]["status"] != "killed" || results != 1 {
		t.Fatalf("lines = %v", lines)
	}
}

func TestRunWallTimeStopsBackgroundWait(t *testing.T) {
	lines, _, took, err := bgRun(t, context.Background(), "sleep 30", Options{MaxWallTime: 1500 * time.Millisecond})
	if err != nil {
		t.Fatalf("a finished turn is not a wall-time failure: %v", err)
	}
	if took > 10*time.Second {
		t.Fatalf("waited %s", took)
	}
	jobs, results := bgLines(lines)
	if len(jobs) != 2 || jobs[1]["status"] != "killed" || results != 1 {
		t.Fatalf("lines = %v", lines)
	}
}

func TestRunCancelKillsBackgroundJob(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(1200*time.Millisecond, cancel)
	lines, _, took, err := bgRun(t, ctx, "sleep 30", Options{})
	if err == nil || took > 10*time.Second {
		t.Fatalf("err %v after %s", err, took)
	}
	jobs, _ := bgLines(lines)
	if len(jobs) != 2 || jobs[1]["status"] != "killed" {
		t.Fatalf("lines = %v", lines)
	}
}

func TestJobEventStatus(t *testing.T) {
	now := time.Now()
	failed := jobEvent(tool.Job{ID: 2, Command: "false", Done: true, Exit: 1, Started: now, Ended: now.Add(2 * time.Second)})
	if failed.Status != "failed" || *failed.ExitCode != 1 || failed.ElapsedSecs != 2 {
		t.Fatalf("%+v", failed)
	}
}
