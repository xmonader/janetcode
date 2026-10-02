package webui

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/xmonader/janetcode/internal/agent"
)

// A question with a recommendation in a bounded wait: the page gets the
// recommendation and deadline, the turn goes on when the wait runs out,
// and the question stays open marked proceeded — on /api/state too, so a
// page that reconnects shows it as one that takes a late answer.
func TestQuestionTimesOutAndStaysOpenForALateAnswer(t *testing.T) {
	ts, ag := testApp(t)
	defer ts.Close()
	ag.SetAskWait(80*time.Millisecond, 80*time.Millisecond)
	out := make(chan string, 1)
	go func() {
		res, err := (&agent.AskTool{Parent: ag}).Run(context.Background(), json.RawMessage(`{"questions":[{"prompt":"Which port?","options":[{"label":"8080"},{"label":"9000"}],"recommended":"9000"}]}`))
		if err != nil {
			res = "error: " + err.Error()
		}
		out <- res
	}()
	select {
	case res := <-out:
		if !strings.Contains(res, "A: 9000 (recommended)") {
			t.Fatalf("tool result = %q", res)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the bounded wait never ended")
	}
	state := func() []agent.Question {
		res, err := http.Get(ts.URL + "/api/state")
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		var snap struct {
			Questions []agent.Question `json:"questions"`
		}
		if err := json.NewDecoder(res.Body).Decode(&snap); err != nil {
			t.Fatal(err)
		}
		return snap.Questions
	}
	qs := state()
	if len(qs) != 1 || !qs[0].Proceeded || qs[0].Recommended != "9000" || qs[0].Deadline.IsZero() {
		t.Fatalf("after the timeout /api/state = %+v", qs)
	}
	body, _ := json.Marshal(map[string]any{"answers": []agent.Answer{{Label: "8080"}}})
	pr, err := http.Post(ts.URL+"/api/question", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	pr.Body.Close()
	if pr.StatusCode != 200 {
		t.Fatalf("late answer: %d", pr.StatusCode)
	}
	if qs := state(); len(qs) != 0 {
		t.Fatalf("the answered question is still up: %+v", qs)
	}
}
