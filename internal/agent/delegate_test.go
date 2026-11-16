package agent

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/delegate"
	"github.com/xmonader/janetcode/internal/llm"
)

// A delegate call is put to the approver like a bash command: a denial
// stops the CLI from ever starting, an approval runs it, and the CLI's
// progress reaches the event stream as status lines.
func TestDelegateGoesThroughApprover(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the fake CLIs are shell scripts")
	}
	bin, err := filepath.Abs("../delegate/testdata/fakebin")
	if err != nil {
		t.Fatal(err)
	}
	// Without the fakes the real, signed-in CLIs on PATH would run (and bill).
	for _, name := range []string{"claude", "codex"} {
		if fi, err := os.Stat(filepath.Join(bin, name)); err != nil || fi.Mode()&0o111 == 0 {
			t.Fatalf("fake %s missing or not executable in %s: %v", name, bin, err)
		}
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	started := filepath.Join(t.TempDir(), "started")
	t.Setenv("DELEGATE_ARGSOUT", started)

	run := func(d Decision) (*Agent, []Event, int) {
		f := &fake{responses: []*llm.Response{
			toolCall("t1", "delegate", `{"agent":"claude","prompt":"ok"}`),
			text("done"),
		}}
		asked := 0
		var names []string
		a, _ := newAgent(t, f, ApproverFunc(func(_ context.Context, c ToolCall) (Decision, error) {
			asked++
			names = append(names, c.Name)
			return d, nil
		}))
		a.Tools.Add(&delegate.Tool{Config: delegate.Config{Enabled: true}, Dir: func() string { return "" }})
		evs, emit := collect()
		if err := a.Run(context.Background(), "delegate it", emit); err != nil {
			t.Fatal(err)
		}
		if len(names) != 1 || names[0] != "delegate" {
			t.Fatalf("approver saw %v", names)
		}
		return a, *evs, asked
	}

	a, _, _ := run(Deny)
	if r := a.Messages[2].Blocks[0]; !r.IsError || !strings.Contains(r.Text, "denied") {
		t.Fatalf("deny result = %+v", r)
	}
	if _, err := os.Stat(started); err == nil {
		t.Fatal("a denied delegate still started the CLI")
	}

	a, evs, _ := run(Allow)
	if r := a.Messages[2].Blocks[0]; r.IsError || !strings.Contains(r.Text, "the answer is 42") {
		t.Fatalf("allow result = %+v", r)
	}
	var phases []string
	for _, e := range evs {
		if e.Kind == EvPhase {
			phases = append(phases, e.Text)
		}
	}
	if !strings.Contains(strings.Join(phases, "\n"), "claude: found it") {
		t.Fatalf("progress did not reach the event stream: %q", phases)
	}
}
