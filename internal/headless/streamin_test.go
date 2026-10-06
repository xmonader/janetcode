package headless

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/xmonader/janetcode/internal/agent"
	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/tool"
)

// lines decodes stdout into objects, by type.
func streamLines(t *testing.T, out string) []map[string]any {
	t.Helper()
	var xs []map[string]any
	sc := bufio.NewScanner(strings.NewReader(out))
	sc.Buffer(nil, 1<<20)
	for sc.Scan() {
		var m map[string]any
		if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
			t.Fatalf("line %q: %v", sc.Text(), err)
		}
		xs = append(xs, m)
	}
	return xs
}

func ofType(xs []map[string]any, typ string) []map[string]any {
	var out []map[string]any
	for _, x := range xs {
		if x["type"] == typ {
			out = append(out, x)
		}
	}
	return out
}

func TestStreamInputRunsATurnPerMessage(t *testing.T) {
	f := &fake{responses: []*llm.Response{endTurn("first answer"), endTurn("second answer")}}
	ag := &agent.Agent{Provider: f, Tools: tool.NewRegistry(), Model: "m"}
	in := strings.Join([]string{
		`{"type":"user","text":"one"}`,
		``,
		`not json`,
		`{"type":"user","message":{"role":"user","content":[{"type":"text","text":"two"}]}}`,
		`{"type":"user","message":{"role":"user","content":[{"type":"image","source":{}}]}}`,
	}, "\n")
	var out, errOut bytes.Buffer
	err := Run(context.Background(), Options{Agent: ag, Stdout: &out, Stderr: &errOut, Format: FormatStreamJSON, Input: strings.NewReader(in)})
	if err != nil {
		t.Fatal(err)
	}
	xs := streamLines(t, out.String())
	results := ofType(xs, "result")
	if len(results) != 2 || results[0]["result"] != "first answer" || results[1]["result"] != "second answer" {
		t.Fatalf("results = %v", results)
	}
	if xs[len(xs)-1]["type"] != "result" {
		t.Fatalf("last line is %v, want the result", xs[len(xs)-1])
	}
	bad := ofType(xs, "input_error")
	if len(bad) != 2 || !strings.Contains(bad[0]["text"].(string), "line 3") || !strings.Contains(bad[1]["text"].(string), `"image" is not supported`) {
		t.Fatalf("input errors = %v", bad)
	}
	if len(ag.Messages) != 4 {
		t.Fatalf("history has %d messages, want both turns in one session", len(ag.Messages))
	}
}

// blocking answers once ctx ends, or with its text when released.
type blocking struct {
	started chan struct{}
	calls   int
}

func (b *blocking) Name() string { return "blocking" }
func (b *blocking) Stream(ctx context.Context, req llm.Request, emit func(llm.Event)) (*llm.Response, error) {
	b.calls++
	if b.calls == 1 {
		close(b.started)
		<-ctx.Done()
		return nil, ctx.Err()
	}
	emit(llm.Event{Type: llm.EventTextDelta, Text: "after the interrupt"})
	return endTurn("after the interrupt"), nil
}

// An interrupt ends the running turn, not the session: the next message
// still runs, and the process exits cleanly at EOF.
func TestStreamInputInterruptCancelsOnlyTheTurn(t *testing.T) {
	p := &blocking{started: make(chan struct{})}
	ag := &agent.Agent{Provider: p, Tools: tool.NewRegistry(), Model: "m"}
	pr, pw := io.Pipe()
	var out, errOut syncBuffer
	done := make(chan error, 1)
	go func() {
		done <- Run(context.Background(), Options{Agent: ag, Stdout: &out, Stderr: &errOut, Format: FormatStreamJSON, Input: pr})
	}()
	io.WriteString(pw, `{"type":"user","text":"slow"}`+"\n")
	select {
	case <-p.started:
	case <-time.After(5 * time.Second):
		t.Fatal("first turn never started")
	}
	io.WriteString(pw, `{"type":"control_request","request_id":"r1","request":{"subtype":"interrupt"}}`+"\n")
	io.WriteString(pw, `{"type":"user","text":"next"}`+"\n")
	pw.Close()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("run: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("run did not end at EOF")
	}
	xs := streamLines(t, out.String())
	results := ofType(xs, "result")
	if len(results) != 2 {
		t.Fatalf("results = %v", results)
	}
	if results[0]["is_error"] != true || !strings.Contains(results[0]["error"].(string), "interrupted") || results[0]["exit_code"].(float64) != ExitCancelled {
		t.Fatalf("first result = %v", results[0])
	}
	if results[1]["is_error"] != false || results[1]["result"] != "after the interrupt" {
		t.Fatalf("second result = %v", results[1])
	}
	acks := ofType(xs, "control_response")
	if len(acks) != 1 || acks[0]["id"] != "r1" || acks[0]["text"] != "interrupted the running turn" {
		t.Fatalf("acks = %v", acks)
	}
}

func TestStreamInputEmptyStillEndsWithAResult(t *testing.T) {
	ag := &agent.Agent{Provider: &fake{}, Tools: tool.NewRegistry(), Model: "m"}
	var out, errOut bytes.Buffer
	if err := Run(context.Background(), Options{Agent: ag, Stdout: &out, Stderr: &errOut, Format: FormatStreamJSON, Input: strings.NewReader("")}); err != nil {
		t.Fatal(err)
	}
	if xs := streamLines(t, out.String()); len(xs) != 1 || xs[0]["type"] != "result" {
		t.Fatalf("out = %q", out.String())
	}
}

func TestStreamInputNeedsStreamOutput(t *testing.T) {
	ag := &agent.Agent{Provider: &fake{}, Tools: tool.NewRegistry(), Model: "m"}
	var out, errOut bytes.Buffer
	err := Run(context.Background(), Options{Agent: ag, Stdout: &out, Stderr: &errOut, Format: FormatJSON, Input: strings.NewReader("")})
	if ExitCode(err) != ExitBadInput {
		t.Fatalf("err = %v", err)
	}
}

func TestOverlongInputLineIsSkipped(t *testing.T) {
	big := `{"type":"user","text":"` + strings.Repeat("x", maxInputLine) + `"}` + "\n"
	br := bufio.NewReaderSize(strings.NewReader(big+`{"type":"user","text":"ok"}`), 64<<10)
	if _, err := readLine(br); err != errLineTooLong {
		t.Fatalf("err = %v", err)
	}
	line, err := readLine(br)
	if err != io.EOF || string(line) != `{"type":"user","text":"ok"}` {
		t.Fatalf("next line = %q, %v", line, err)
	}
}

func TestParseInputFormat(t *testing.T) {
	for in, want := range map[string]InputFormat{"": InputText, "text": InputText, "stream-json": InputStreamJSON} {
		if got, err := ParseInputFormat(in); err != nil || got != want {
			t.Errorf("%q: %v %v", in, got, err)
		}
	}
	if _, err := ParseInputFormat("xml"); err == nil {
		t.Error("xml accepted")
	}
}
