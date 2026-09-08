package headless

import (
	"bytes"
	"errors"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/agent"
	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/session"
	"github.com/xmonader/janetcode/internal/tool"
)

type fake struct{ responses []*llm.Response }

func (f *fake) Name() string { return "fake" }
func (f *fake) Stream(ctx context.Context, req llm.Request, emit func(llm.Event)) (*llm.Response, error) {
	r := f.responses[0]
	f.responses = f.responses[1:]
	for _, b := range r.Message.Blocks {
		if b.Type == llm.BlockText {
			emit(llm.Event{Type: llm.EventTextDelta, Text: b.Text})
		}
	}
	return r, nil
}

func TestRunStreamsTextAndDeniesWithoutTTY(t *testing.T) {
	root := t.TempDir()
	f := &fake{responses: []*llm.Response{
		{Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{
			{Type: llm.BlockToolUse, ID: "t1", Name: "write_file", Input: json.RawMessage(`{"path":"x","content":"1"}`)},
		}}, StopReason: llm.StopToolUse},
		{Message: llm.TextMessage(llm.RoleAssistant, "could not write"), StopReason: llm.StopEndTurn, Usage: llm.Usage{InputTokens: 3, OutputTokens: 4}},
	}}
	ag := &agent.Agent{Provider: f, Tools: tool.NewRegistry(tool.WriteFile{Root: root}), Model: "m"}
	sess, err := session.New(t.TempDir(), root, "fake", "m")
	if err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	devnull, _ := os.Open(os.DevNull)
	defer devnull.Close()
	err = Run(context.Background(), Options{Agent: ag, Session: sess, Prompt: "write x", Stdout: &out, Stderr: &errOut, Stdin: devnull})
	if err != nil {
		t.Fatal(err)
	}
	if out.String() != "could not write\n" {
		t.Fatalf("stdout = %q", out.String())
	}
	e := errOut.String()
	if !strings.Contains(e, "[permission] write_file denied") || !strings.Contains(e, "[tool error]") || !strings.Contains(e, "[usage] in=3 out=4") {
		t.Fatalf("stderr = %q", e)
	}
	if _, err := os.Stat(filepath.Join(root, "x")); err == nil {
		t.Fatal("file was written despite denial")
	}
	// Session persisted all four messages.
	_, msgs, err := session.Open(filepath.Dir(sess.Path()), sess.Meta.ID)
	if err != nil || len(msgs) != 4 {
		t.Fatalf("session: err=%v n=%d", err, len(msgs))
	}
}

func TestUntilReloopsUntilCheckPasses(t *testing.T) {
	root := t.TempDir()
	f := &fake{responses: []*llm.Response{
		{Message: llm.TextMessage(llm.RoleAssistant, "first try"), StopReason: llm.StopEndTurn},
		{Message: llm.TextMessage(llm.RoleAssistant, "second try"), StopReason: llm.StopEndTurn},
	}}
	ag := &agent.Agent{Provider: f, Tools: tool.NewRegistry(), Model: "m", AutoApprove: true}
	var out, errOut bytes.Buffer
	// The check fails once and passes on its second run.
	check := "test -f flag || { touch flag; echo 'not yet'; exit 3; }"
	err := Run(context.Background(), Options{Agent: ag, Prompt: "do it", Stdout: &out, Stderr: &errOut, Until: check, Cwd: root})
	if err != nil {
		t.Fatalf("err = %v\n%s", err, errOut.String())
	}
	if !strings.Contains(out.String(), "first try") || !strings.Contains(out.String(), "second try") {
		t.Fatalf("stdout = %q", out.String())
	}
	if !strings.Contains(errOut.String(), "round 1/5 failed (exit 3)") || !strings.Contains(errOut.String(), "[until] passed") {
		t.Fatalf("stderr = %q", errOut.String())
	}
	// The failure went back to the model as the next user message.
	if got := ag.Messages[2].Text(); !strings.Contains(got, "Exit code: 3") || !strings.Contains(got, "not yet") {
		t.Fatalf("second prompt = %q", got)
	}
	// A check that never passes stops after the round limit.
	f2 := &fake{responses: []*llm.Response{
		{Message: llm.TextMessage(llm.RoleAssistant, "a"), StopReason: llm.StopEndTurn},
		{Message: llm.TextMessage(llm.RoleAssistant, "b"), StopReason: llm.StopEndTurn},
	}}
	ag2 := &agent.Agent{Provider: f2, Tools: tool.NewRegistry(), Model: "m"}
	err = Run(context.Background(), Options{Agent: ag2, Prompt: "x", Stdout: &out, Stderr: &errOut, Until: "false", UntilRounds: 2, Cwd: root})
	if !errors.Is(err, ErrUntilFailed) {
		t.Fatalf("err = %v", err)
	}
}
