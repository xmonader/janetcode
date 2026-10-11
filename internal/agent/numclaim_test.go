package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/session"
	"github.com/xmonader/janetcode/internal/tool"
)

func numclaimAgent(f *fake, root string) *Agent {
	return &Agent{Provider: f, Model: "m", Log: &session.Memory{}, AutoApprove: true, Root: root,
		Tools: tool.NewRegistry(tool.WriteFile{Root: root}, tool.Bash{Root: root})}
}

// numclaimLogged is the numclaim notice the turn logged, if any.
func numclaimLogged(a *Agent) *session.Event {
	for _, e := range a.Log.Events() {
		if e.Type == session.TypeNotice && e.Name == "numclaim" {
			return &e
		}
	}
	return nil
}

// An invented speed-up is named beside the answer; with the nudge on the
// turn goes back once to measure it.
func TestNumclaimFlagsAnUnmeasuredSpeedup(t *testing.T) {
	for _, nudge := range []bool{false, true} {
		root := t.TempDir()
		f := &fake{responses: []*llm.Response{
			toolCall("t1", "write_file", `{"path":"calc.go","content":"package calc\n"}`),
			text("Rewrote the loop; Parse is now 40% faster."),
			text("It is an estimate; I did not measure it."),
		}}
		a := numclaimAgent(f, root)
		a.NumericClaimsNudge = nudge
		var notices []string
		err := a.Run(context.Background(), "make parse faster", func(e Event) {
			if e.Kind == EvNotice {
				notices = append(notices, e.Text)
			}
		})
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(strings.Join(notices, "\n"), `unverified numbers in the answer: "40%": no benchmark or timing run`) {
			t.Fatalf("nudge=%v: notices = %q", nudge, notices)
		}
		e := numclaimLogged(a)
		if e == nil || !e.IsError || !strings.Contains(string(e.Extra), `"status":"unmeasured"`) {
			t.Fatalf("nudge=%v: logged %+v", nudge, e)
		}
		want := 2
		if nudge {
			want = 3
			if got := diagLastText(f.requests[2]); !strings.Contains(got, `"40%" (speed)`) || !strings.Contains(got, "say plainly that the numbers are estimates") {
				t.Fatalf("note = %q", got)
			}
		}
		if len(f.requests) != want {
			t.Fatalf("nudge=%v: %d model calls, want %d", nudge, len(f.requests), want)
		}
	}
}

// A speed-up a run before and a run after the edit bear out passes
// quietly.
func TestNumclaimPassesAMeasuredSpeedup(t *testing.T) {
	root := t.TempDir()
	f := &fake{responses: []*llm.Response{
		toolCall("t1", "bash", `{"command":"echo 'BenchmarkParse-8 100 1000 ns/op'"}`),
		toolCall("t2", "write_file", `{"path":"calc.go","content":"package calc\n"}`),
		toolCall("t3", "bash", `{"command":"echo 'BenchmarkParse-8 100 600 ns/op'"}`),
		text("Parse is now 40% faster (1000 → 600 ns/op)."),
	}}
	a := numclaimAgent(f, root)
	a.NumericClaimsNudge = true
	var notices []string
	if err := a.Run(context.Background(), "make parse faster", func(e Event) {
		if e.Kind == EvNotice {
			notices = append(notices, e.Text)
		}
	}); err != nil {
		t.Fatal(err)
	}
	if len(f.requests) != 4 {
		t.Fatalf("%d model calls, want 4", len(f.requests))
	}
	for _, n := range notices {
		if strings.Contains(n, "unverified numbers") {
			t.Fatalf("a measured claim was flagged: %q", n)
		}
	}
	if e := numclaimLogged(a); e == nil || e.IsError {
		t.Fatalf("logged %+v", e)
	}
}

// An answer with no number, and a run with the switch off, log nothing.
func TestNumclaimQuietWithoutNumbersOrWhenOff(t *testing.T) {
	for _, tc := range []struct {
		answer string
		off    map[string]bool
	}{
		{"Rewrote the loop; it should be faster now.", nil},
		{"Parse is now 40% faster.", map[string]bool{"numclaim": true}},
	} {
		f := &fake{responses: []*llm.Response{text(tc.answer)}}
		a := numclaimAgent(f, t.TempDir())
		a.Off = tc.off
		a.NumericClaimsNudge = true
		if err := a.Run(context.Background(), "speed it up", nil); err != nil {
			t.Fatal(err)
		}
		if e := numclaimLogged(a); e != nil {
			t.Fatalf("%q off=%v: logged %+v", tc.answer, tc.off, e)
		}
		if len(f.requests) != 1 {
			t.Fatalf("%d model calls", len(f.requests))
		}
	}
}
