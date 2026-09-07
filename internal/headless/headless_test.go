package headless

import (
	"bytes"
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
