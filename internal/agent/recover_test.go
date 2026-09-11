package agent

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/llm/openai"
	"github.com/xmonader/janetcode/internal/tool"
)

// prose builds an assistant turn whose "tool call" is only text.
func prose(s string) *llm.Response {
	return &llm.Response{Message: llm.TextMessage(llm.RoleAssistant, s), StopReason: llm.StopEndTurn, Usage: llm.Usage{InputTokens: 1, OutputTokens: 1}}
}

func TestRecoversXMLToolCallFromText(t *testing.T) {
	f := &fake{responses: []*llm.Response{
		prose("<invoke name=\"read_file\">\n<parameter name=\"path\">a.txt</parameter>\n</invoke>"),
		text("it says alpha"),
	}}
	a, _ := newAgent(t, f, nil)
	evs, emit := collect()
	if err := a.Run(context.Background(), "read a.txt", emit); err != nil {
		t.Fatal(err)
	}
	var results []Event
	for _, e := range *evs {
		if e.Kind == EvToolResult {
			results = append(results, e)
		}
	}
	if len(results) != 1 || !strings.Contains(results[0].Output, "alpha") {
		t.Fatalf("the recovered call did not run: %+v", results)
	}
	if st := a.Stats(); st.Recoveries != 1 {
		t.Fatalf("recoveries = %d", st.Recoveries)
	}
	notice := false
	for _, e := range *evs {
		if e.Kind == EvNotice && strings.Contains(e.Text, "read_file") {
			notice = true
		}
	}
	if !notice {
		t.Fatal("no notice said a call was recovered")
	}
}

func TestRecoversToolCallJSONAndKeepsProse(t *testing.T) {
	f := &fake{responses: []*llm.Response{
		prose("Reading it.\n<tool_call>{\"name\":\"read_file\",\"arguments\":{\"path\":\"a.txt\"}}</tool_call>"),
		text("done"),
	}}
	a, _ := newAgent(t, f, nil)
	if err := a.Run(context.Background(), "read a.txt", func(Event) {}); err != nil {
		t.Fatal(err)
	}
	if got := a.Messages[1].Text(); got != "Reading it." {
		t.Fatalf("assistant text = %q, want the prose without the block", got)
	}
	if uses := a.Messages[1].ToolUses(); len(uses) != 1 || uses[0].Name != "read_file" {
		t.Fatalf("tool uses = %+v", uses)
	}
}

func TestRecoveryIgnoresUnknownToolsAndRealCalls(t *testing.T) {
	f := &fake{responses: []*llm.Response{
		prose("<invoke name=\"not_a_tool\">\n<parameter name=\"x\">1</parameter>\n</invoke>"),
	}}
	a, _ := newAgent(t, f, nil)
	if err := a.Run(context.Background(), "go", func(Event) {}); err != nil {
		t.Fatal(err)
	}
	if st := a.Stats(); st.Recoveries != 0 {
		t.Fatalf("recovered a call to a tool that does not exist: %d", st.Recoveries)
	}
	if st := a.Stats(); st.ModelCalls != 1 {
		t.Fatalf("the turn should have ended: %d model calls", st.ModelCalls)
	}
}

// End to end over the wire: a server that streams the call as content,
// the way a small model behind a gateway that dropped the tool schema
// does, still gets the tool run.
func TestRecoveryFromStreamedServerResponse(t *testing.T) {
	body := func(text string) string {
		chunk, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"content": text}}}})
		return "data: " + string(chunk) + "\n\ndata: {\"choices\":[{\"finish_reason\":\"stop\",\"delta\":{}}]}\n\ndata: [DONE]\n\n"
	}
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		calls++
		if calls == 1 {
			io.WriteString(w, body("<invoke name=\"read_file\">\n<parameter name=\"path\">a.txt</parameter>\n</invoke>"))
			return
		}
		io.WriteString(w, body("it says alpha"))
	}))
	defer srv.Close()
	prov, err := openai.New(openai.Config{BaseURL: srv.URL + "/v1", APIKey: "k"})
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "a.txt"), []byte("alpha\n"), 0o644)
	a := &Agent{Provider: prov, Tools: tool.NewRegistry(tool.ReadFile{Root: root}), Model: "m", System: "sys"}
	evs, emit := collect()
	if err := a.Run(context.Background(), "read a.txt", emit); err != nil {
		t.Fatal(err)
	}
	ran := false
	for _, e := range *evs {
		if e.Kind == EvToolResult && strings.Contains(e.Output, "alpha") {
			ran = true
		}
	}
	if !ran {
		t.Fatal("the call written as text never ran")
	}
	if st := a.Stats(); st.Recoveries != 1 {
		t.Fatalf("recoveries = %d", st.Recoveries)
	}
}
