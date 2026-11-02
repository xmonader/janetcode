package mcptool

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/xmonader/janetcode/internal/agent"
	"github.com/xmonader/janetcode/internal/mcp"
)

type script struct {
	rounds [][]agent.Answer
	asked  [][]agent.Question
}

func (s *script) Ask(_ context.Context, qs []agent.Question) ([]agent.Answer, error) {
	s.asked = append(s.asked, qs)
	if len(s.rounds) == 0 {
		return nil, context.DeadlineExceeded
	}
	r := s.rounds[0]
	s.rounds = s.rounds[1:]
	return r, nil
}

func other(t string) agent.Answer { return agent.Answer{Label: agent.OtherLabel, Reason: t} }
func pick(l string) agent.Answer  { return agent.Answer{Label: l} }

func TestRunForm(t *testing.T) {
	schema := `{"required":["email","size"],"properties":{
	  "email":{"type":"string","format":"email"},
	  "size":{"enum":["s","m"],"enumNames":["Small","Medium"]},
	  "ok":{"type":"boolean"},
	  "note":{"type":"string"}}}`
	fields, err := mcp.ParseForm(json.RawMessage(schema))
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name   string
		rounds [][]agent.Answer
		action string
		want   string
		asks   int
	}{
		{"accept", [][]agent.Answer{{other("a@b.co"), pick("Medium"), pick("Yes"), pick("(leave empty)")}}, "accept", `{"email":"a@b.co","ok":true,"size":"m"}`, 1},
		{"retry bad email", [][]agent.Answer{
			{other("bad"), pick("Small"), pick("No"), pick("(leave empty)")},
			{other("x@y.zz")}}, "accept", `{"email":"x@y.zz","ok":false,"size":"s"}`, 2},
		{"decline", [][]agent.Answer{{other("a@b.co"), pick(DeclineLabel), pick("No"), pick("(leave empty)")}}, "decline", "", 1},
		{"cancel", [][]agent.Answer{{pick(CancelLabel), pick("Small"), pick("No"), pick("(leave empty)")}}, "cancel", "", 1},
		{"dismissed card", [][]agent.Answer{{pick("skipped"), pick("skipped"), pick("skipped"), pick("skipped")}}, "cancel", "", 1},
		{"timeout", nil, "cancel", "", 1},
		{"gives up after 3 bad rounds", [][]agent.Answer{
			{other("1"), pick("Small"), pick("No"), pick("(leave empty)")},
			{other("2")}, {other("3")}}, "cancel", "", 3},
	} {
		s := &script{rounds: c.rounds}
		r := RunForm(context.Background(), s, "srv", "need info", fields)
		if r.Action != c.action {
			t.Errorf("%s: action %q", c.name, r.Action)
		}
		if c.want != "" {
			b, _ := json.Marshal(r.Content)
			if string(b) != c.want {
				t.Errorf("%s: content %s", c.name, b)
			}
		}
		if len(s.asked) != c.asks {
			t.Errorf("%s: asked %d times", c.name, len(s.asked))
		}
	}
}

func TestQuestionShape(t *testing.T) {
	fields, _ := mcp.ParseForm(json.RawMessage(`{"required":["a"],"properties":{"a":{"type":"integer","minimum":1},"b":{"type":"boolean"}}}`))
	qa := Question("srv", "msg", fields[0], "must be an integer")
	if qa.Prompt != "a (required)" || qa.Intent != "elicitation" || len(qa.Options) != 2 {
		t.Fatalf("%+v", qa)
	}
	for _, want := range []string{"srv: msg", "min 1", "Not accepted: must be an integer"} {
		if !contains(qa.Detail, want) {
			t.Errorf("detail lacks %q: %q", want, qa.Detail)
		}
	}
	if qb := Question("srv", "", fields[1], ""); qb.Options[0].Label != "Yes" || qb.Options[1].Label != "No" {
		t.Fatalf("%+v", qb)
	}
}

func contains(s, sub string) bool {
	return len(sub) == 0 || (len(s) >= len(sub) && indexOf(s, sub) >= 0)
}
func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

func TestElicitViaAskerWithoutAskerDeclines(t *testing.T) {
	f := ElicitViaAsker(func() agent.Asker { return nil })
	if r := f(context.Background(), mcp.ElicitRequest{Schema: json.RawMessage(`{"properties":{}}`)}); r.Action != mcp.ElicitDecline {
		t.Fatalf("%+v", r)
	}
}
