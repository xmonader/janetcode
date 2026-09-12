package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestAskToolReturnsChosenLabels(t *testing.T) {
	ag := &Agent{Asker: AskFunc(func(_ context.Context, qs []Question) ([]Answer, error) {
		if len(qs) != 2 || qs[0].Prompt != "Goal?" || len(qs[0].Options) != 2 {
			t.Fatalf("qs = %+v", qs)
		}
		return []Answer{{Label: "Performance"}, {Label: "Other", Reason: "ship it"}}, nil
	})}
	out, err := (&AskTool{Parent: ag}).Run(context.Background(), json.RawMessage(`{"questions":[
		{"prompt":"Goal?","options":[{"label":"Performance"},{"label":"Readability"}]},
		{"prompt":"Name?","options":[{"label":"Alpha"},{"label":"Beta"}]}
	]}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "Q: Goal?") || !strings.Contains(out, "A: Performance") {
		t.Fatalf("out = %q", out)
	}
	if !strings.Contains(out, "Q: Name?") || !strings.Contains(out, "A: Other — ship it") {
		t.Fatalf("Other: %q", out)
	}
}

func TestAskToolNeedsSomeoneToAnswer(t *testing.T) {
	_, err := (&AskTool{}).Run(context.Background(), json.RawMessage(`{"questions":[{"prompt":"X?","options":[{"label":"A"},{"label":"B"}]}]}`))
	if err == nil || !strings.Contains(err.Error(), "not interactive") {
		t.Fatalf("err = %v", err)
	}
}
