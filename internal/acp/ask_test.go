package acp

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"testing"

	"github.com/xmonader/janetcode/internal/agent"
)

func TestAskChoosesOptionsThroughPermissionPrompts(t *testing.T) {
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	defer inW.Close()
	srv := New(Options{In: inR, Out: outW, Log: io.Discard})
	go func() { _ = srv.Serve(context.Background()) }()
	srv.sessions["s1"] = &acpSession{id: "s1", seq: 1, turn: make(chan struct{}, 1)}
	srv.sessions["s2"] = &acpSession{id: "s2", seq: 2, turn: make(chan struct{}, 1)}

	// The "editor" picks the second option of every prompt, then dismisses.
	picks := []string{"opt-1", ""}
	var seen []requestPermissionParams
	go func() {
		sc := bufio.NewScanner(outR)
		for n := 0; sc.Scan(); n++ {
			var m message
			if json.Unmarshal(sc.Bytes(), &m) != nil || m.Method != "session/request_permission" {
				continue
			}
			var p requestPermissionParams
			_ = json.Unmarshal(m.Params, &p)
			seen = append(seen, p)
			res := requestPermissionResult{Outcome: permissionOutcome{Outcome: "selected", OptionID: picks[len(seen)-1]}}
			if picks[len(seen)-1] == "" {
				res.Outcome = permissionOutcome{Outcome: "cancelled"}
			}
			raw, _ := json.Marshal(res)
			b, _ := json.Marshal(message{ID: m.ID, Result: raw})
			_, _ = inW.Write(append(b, '\n'))
		}
	}()

	srv.sessions["s1"].turn <- struct{}{} // s1 is mid-turn: it gets the question
	qs := []agent.Question{
		{Prompt: "Size", Options: []agent.AskOption{{Label: "S"}, {Label: "M"}, {Label: "Decline request"}}},
		{Prompt: "Other", Options: []agent.AskOption{{Label: "x"}}},
	}
	ans, err := srv.Ask(context.Background(), qs)
	if err != nil {
		t.Fatal(err)
	}
	if len(ans) != 2 || ans[0].Label != "M" || ans[1].Label != "skipped" {
		t.Fatalf("answers = %+v", ans)
	}
	if len(seen) != 2 || seen[0].SessionID != "s1" || seen[0].Options[2].Kind != optRejectOnce || seen[0].Options[0].Kind != optAllowOnce {
		t.Fatalf("prompts = %+v", seen)
	}
}

func TestAskWithoutASessionFails(t *testing.T) {
	srv := New(Options{In: io.NopCloser(nil), Out: io.Discard, Log: io.Discard})
	if _, err := srv.Ask(context.Background(), []agent.Question{{Prompt: "x"}}); err == nil {
		t.Fatal("expected an error")
	}
}
