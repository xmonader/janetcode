package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/session"
	"github.com/xmonader/janetcode/internal/tool"
)

// checkCmd is a command the verdict ledger reads as a check ("test" in it,
// and not one of the path utilities) that works anywhere.
const checkCmd = `sh -c "echo test-run; cat n.py"`

func acUse(id, name string, in map[string]string) llm.Block {
	raw, _ := json.Marshal(in)
	return llm.Block{Type: llm.BlockToolUse, ID: id, Name: name, Input: raw}
}

func acReply(blocks ...llm.Block) *llm.Response {
	return &llm.Response{
		Message:    llm.Message{Role: llm.RoleAssistant, Blocks: blocks},
		StopReason: llm.StopToolUse,
		Usage:      llm.Usage{InputTokens: 1, OutputTokens: 1},
	}
}

func autoAgent(t *testing.T, f *fake) (*Agent, *session.Memory) {
	t.Helper()
	a, root := newAgent(t, f, &namesApprover{})
	a.Tools = tool.NewRegistry(tool.Bash{Root: root}, tool.WriteFile{Root: root})
	a.Headless = true
	mem := &session.Memory{}
	a.Log = mem
	return a, mem
}

// toolResults is every tool result text of the history, in order.
func toolResults(a *Agent) []string {
	var out []string
	for _, m := range a.Messages {
		for _, b := range m.Blocks {
			if b.Type == llm.BlockToolResult {
				out = append(out, b.Text)
			}
		}
	}
	return out
}

func runAuto(t *testing.T, a *Agent) {
	t.Helper()
	if err := a.Run(context.Background(), "go", func(Event) {}); err != nil {
		t.Fatal(err)
	}
}

// After the model ran a check once, a later reply that edits gets the
// check's output on the edit's result, and the rerun is a bash event the
// verdict counts as a check.
func TestAutoCheckRerunsTheLearnedCheckAfterAnEdit(t *testing.T) {
	f := &fake{responses: []*llm.Response{
		acReply(acUse("t1", "write_file", map[string]string{"path": "n.py", "content": "v1"})),
		acReply(acUse("t2", "bash", map[string]string{"command": checkCmd})),
		acReply(acUse("t3", "write_file", map[string]string{"path": "n.py", "content": "v2"})),
		text("done"),
	}}
	a, mem := autoAgent(t, f)
	runAuto(t, a)
	res := toolResults(a)
	if strings.Contains(res[0], "[autocheck]") {
		t.Fatalf("an autocheck ran before any check was learned: %q", res[0])
	}
	if !strings.Contains(res[2], "\n\n[autocheck] $ "+checkCmd+"\n") || !strings.Contains(res[2], "test-run\nv2") {
		t.Fatalf("the edit's result carries no autocheck: %q", res[2])
	}
	var seen bool
	for _, e := range mem.Events() {
		if e.Type == session.TypeToolResult && e.CallID == "t3~autocheck" && e.Name == "bash" {
			seen = true
		}
	}
	if !seen {
		t.Fatal("the autocheck left no bash event in the log")
	}
	o := FoldOutcomeIn(mem.Events(), a.turnN, "")
	if o.Verdict != VerdictNotDemonstrated && o.Verdict != VerdictVerified {
		t.Fatalf("verdict = %q", o.Verdict)
	}
	if o.EditedAfterCheck || o.Ran != 1 || o.Passed != 1 {
		t.Fatalf("the autocheck was not counted as the check that ran last: %+v", o)
	}
}

// Without a check learned there is nothing to rerun.
func TestAutoCheckNeedsALearnedCheck(t *testing.T) {
	f := &fake{responses: []*llm.Response{
		acReply(acUse("t1", "write_file", map[string]string{"path": "n.py", "content": "v1"})),
		text("done"),
	}}
	a, mem := autoAgent(t, f)
	runAuto(t, a)
	if strings.Contains(toolResults(a)[0], "[autocheck]") {
		t.Fatal("ran with no learned check")
	}
	for _, e := range mem.Events() {
		if e.Name == "bash" {
			t.Fatalf("a bash call was made: %+v", e)
		}
	}
}

// A reply that already ran the check after its edit needs no second run.
func TestAutoCheckSkipsWhenTheReplyChecked(t *testing.T) {
	f := &fake{responses: []*llm.Response{
		acReply(acUse("t1", "bash", map[string]string{"command": checkCmd})),
		acReply(acUse("t2", "write_file", map[string]string{"path": "n.py", "content": "v2"}),
			acUse("t3", "bash", map[string]string{"command": checkCmd})),
		text("done"),
	}}
	a, mem := autoAgent(t, f)
	runAuto(t, a)
	for _, r := range toolResults(a) {
		if strings.Contains(r, "[autocheck]") {
			t.Fatalf("reran a check the reply had run: %q", r)
		}
	}
	for _, e := range mem.Events() {
		if strings.HasSuffix(e.CallID, "~autocheck") {
			t.Fatalf("autocheck event %+v", e)
		}
	}
}

// A check that ran before the edit in the same reply does not cover it.
func TestAutoCheckRunsWhenTheReplyCheckedBeforeItsEdit(t *testing.T) {
	f := &fake{responses: []*llm.Response{
		acReply(acUse("t1", "bash", map[string]string{"command": checkCmd}),
			acUse("t2", "write_file", map[string]string{"path": "n.py", "content": "v2"})),
		text("done"),
	}}
	a, _ := autoAgent(t, f)
	runAuto(t, a)
	if res := toolResults(a); !strings.Contains(res[1], "[autocheck] $ ") {
		t.Fatalf("no autocheck after an edit the check preceded: %q", res[1])
	}
}

func TestAutoCheckSwitchedOffOrInteractive(t *testing.T) {
	for _, tc := range []struct {
		name  string
		tweak func(*Agent)
	}{
		{"off", func(a *Agent) { a.Off = map[string]bool{"autocheck": true} }},
		{"interactive", func(a *Agent) { a.Headless = false }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &fake{responses: []*llm.Response{
				acReply(acUse("t1", "bash", map[string]string{"command": checkCmd})),
				acReply(acUse("t2", "write_file", map[string]string{"path": "n.py", "content": "v2"})),
				text("done"),
			}}
			a, _ := autoAgent(t, f)
			tc.tweak(a)
			runAuto(t, a)
			if strings.Contains(toolResults(a)[1], "[autocheck]") {
				t.Fatal("ran while off")
			}
		})
	}
}

// bashEv is a bash result event of turn 1 as the log holds it.
func bashEv(cmd, out string, ms int64) session.Event {
	args, _ := json.Marshal(map[string]string{"command": cmd})
	return session.Event{Type: session.TypeToolResult, Turn: 1, CallID: "c" + cmd, Name: "bash", Arguments: string(args), Output: out, DurationMs: ms}
}

func TestLearnedChecks(t *testing.T) {
	ok := "ok  \tx\t0.1s\n[exit code 0]"
	for _, tc := range []struct {
		name   string
		events []session.Event
		want   []string
	}{
		{"none", nil, nil},
		{"not a check", []session.Event{bashEv("ls -la", "x", 5)}, nil},
		{"latest run of a command, newest last", []session.Event{
			bashEv("go test ./a/", ok, 5), bashEv("go vet ./...", ok, 5), bashEv("go test ./a/", ok, 5),
		}, []string{"go vet ./...", "go test ./a/"}},
		{"only the last two distinct", []session.Event{
			bashEv("go build ./...", ok, 5), bashEv("go vet ./...", ok, 5), bashEv("go test ./a/", ok, 5),
		}, []string{"go vet ./...", "go test ./a/"}},
		{"slower than a minute is left out", []session.Event{bashEv("go test ./...", ok, 61_000)}, nil},
		{"a backgrounded run is left out", []session.Event{bashEv("go test ./...", "Started background job 1 (pid 2)", 5)}, nil},
		{"held after the limit is left out", []session.Event{bashEv("go test ./...", "Still running after 3m0s, so it was moved", 5)}, nil},
		{"a command that never started is left out", []session.Event{bashEv("pytest x", "bash: pytest: command not found\n[exit code 127]", 5)}, nil},
		{"a refused call is left out", []session.Event{func() session.Event {
			e := bashEv("go test ./...", "denied", 5)
			e.Denied = true
			return e
		}()}, nil},
		{"a failing check is learned", []session.Event{bashEv("go test ./...", "FAIL\n[exit code 1]", 5)}, []string{"go test ./..."}},
		{"the no-cache marker makes no second command", []session.Event{
			bashEv("python /c/checks/s/check.py", ok, 5), bashEv("python /c/checks/s/check.py # no-cache", ok, 5),
		}, []string{"python /c/checks/s/check.py"}},
		{"a leading cd of the workspace is not part of it", []session.Event{bashEv("cd /w && go test ./...", ok, 5)}, []string{"go test ./..."}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := learnedChecks(tc.events, 1, "/w")
			if strings.Join(got, "|") != strings.Join(tc.want, "|") {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
	// Another turn's runs are not this turn's.
	other := bashEv("go test ./...", ok, 5)
	other.Turn = 2
	if got := learnedChecks([]session.Event{other}, 1, "/w"); got != nil {
		t.Fatalf("learned from another turn: %q", got)
	}
}

// Both of the model's checks are rerun, the second not after a red first.
func TestAutoCheckRerunsTwoAndStopsAtAFailure(t *testing.T) {
	second := `sh -c "echo test-two"`
	f := &fake{responses: []*llm.Response{
		acReply(acUse("t1", "bash", map[string]string{"command": `sh -c "echo test-one; exit 1"`}),
			acUse("t2", "bash", map[string]string{"command": second})),
		acReply(acUse("t3", "write_file", map[string]string{"path": "n.py", "content": "v2"})),
		text("done"),
	}}
	a, _ := autoAgent(t, f)
	runAuto(t, a)
	out := toolResults(a)[2]
	if !strings.Contains(out, "test-one") || strings.Contains(out, "test-two") {
		t.Fatalf("a red first check should stop the run: %q", out)
	}
}

// A scratch-file edit is not the work: the turn that only reworked its
// check file stays "nothing was edited", and the check run counts.
func TestFoldIgnoresEditsToTheScratchFile(t *testing.T) {
	p := "/home/u/.cache/janetcode/checks/s1/check.py"
	evs := []session.Event{
		result(1, "write_file", `{"path":"`+p+`","content":"x"}`, "Created "+p, false),
		result(1, "bash", `{"command":"python3 `+p+`"}`, "ok\n[exit code 0]", false),
	}
	o := FoldOutcomeIn(evs, 1, "/work")
	if o.Edited != 0 || o.Ran != 1 {
		t.Fatalf("outcome = %+v", o)
	}
}
