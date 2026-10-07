package eval

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/xmonader/janetcode/internal/agent"
	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/tool"
)

// down answers every call with a gateway outage.
type down struct{}

func (down) Name() string { return "fake" }
func (down) Stream(context.Context, llm.Request, func(llm.Event)) (*llm.Response, error) {
	return nil, statusErr(504)
}

type statusErr int

func (s statusErr) Error() string            { return fmt.Sprintf("upstream %d", int(s)) }
func (s statusErr) HTTPStatus() int          { return int(s) }
func (s statusErr) RetryHint() time.Duration { return 0 }

// swapped answers like the 2026-10-01 broker: the requested name in the
// model field, Kimi's tool ids.
type swapped struct{ scripted }

func (s *swapped) Stream(ctx context.Context, req llm.Request, emit func(llm.Event)) (*llm.Response, error) {
	r, err := s.scripted.Stream(ctx, req, emit)
	if r != nil {
		r.Served = llm.Provenance{Model: req.Model, IDPrefix: "chatcmpl-", ToolIDs: "tool_24"}
	}
	return r, err
}

// A run lost to the gateway and a run another vendor's model answered
// are classified, reported, and left out of the pass rate — the two
// ways a published result last measured something other than the model.
func TestRunnerClassifiesInfraAndMismatch(t *testing.T) {
	task := Task{Name: "t", Prompt: "write hello", Check: "test -f hello.txt"}
	r := &Runner{NewAgent: func(root, _ string) (*agent.Agent, error) {
		return &agent.Agent{Provider: down{}, Tools: tool.NewRegistry(tool.WriteFile{Root: root}), Model: "xiaomi/mimo-v2.6-flash"}, nil
	}}
	res := r.Run(context.Background(), []Task{task})
	if !res[0].Infra || res[0].Passed || res[0].Outcome() != "INFRA" {
		t.Fatalf("outage: %+v", res[0])
	}
	r = &Runner{NewAgent: func(root, _ string) (*agent.Agent, error) {
		return &agent.Agent{Provider: &swapped{}, Tools: tool.NewRegistry(tool.WriteFile{Root: root}), Model: "xiaomi/mimo-v2.6-flash"}, nil
	}}
	sw := r.Run(context.Background(), []Task{task})
	if sw[0].Mismatches == 0 || !strings.Contains(sw[0].Mismatch, "moonshot") || sw[0].Outcome() != "MISMATCH" || sw[0].Served == nil {
		t.Fatalf("swap: %+v", sw[0])
	}
	r = &Runner{NewAgent: func(root, _ string) (*agent.Agent, error) {
		return &agent.Agent{Provider: &scripted{}, Tools: tool.NewRegistry(tool.WriteFile{Root: root}), Model: "xiaomi/mimo-v2.6-flash"}, nil
	}}
	ok := r.Run(context.Background(), []Task{task})
	all := []Result{res[0], sw[0], ok[0]}
	s := Summarize(all)
	if s.Total != 1 || s.Passed != 1 || s.Infra != 1 || s.Mismatched != 1 {
		t.Fatalf("summary: total=%d passed=%d infra=%d mismatched=%d", s.Total, s.Passed, s.Infra, s.Mismatched)
	}
	var buf bytes.Buffer
	Report(&buf, all)
	out := buf.String()
	for _, want := range []string{"1/1 passed", "INFRA", "MISMATCH", "not scored", "ANOTHER VENDOR'S MODEL"} {
		if !strings.Contains(out, want) {
			t.Fatalf("report lacks %q:\n%s", want, out)
		}
	}
}

// A comparison pairs only what both arms measured: an arm whose runs of a
// task were all lost to the gateway does not pair that task.
func TestCompareLeavesUnscoredRunsOut(t *testing.T) {
	base := []Result{
		{Name: "a", Passed: true, CostUSD: 1, CostKnown: true},
		{Name: "b", Passed: true, CostUSD: 1, CostKnown: true},
	}
	cur := []Result{
		{Name: "a", Passed: true, CostUSD: 1, CostKnown: true},
		{Name: "b", Infra: true, Reason: "run: upstream 504", CostKnown: true},
	}
	c := Compare(base, cur)
	if c.Shared != 1 || len(c.Regressed) != 0 || c.Current.Total != 1 || c.Current.Infra != 1 {
		t.Fatalf("comparison: shared=%d regressed=%v current=%+v", c.Shared, c.Regressed, c.Current)
	}
}

// harnessbench reads janetcode's provenance out of its result envelope,
// and a row another vendor's model answered is in neither column.
func TestBenchRowsCarryProvenance(t *testing.T) {
	var r BenchTaskResult
	r.Harness = "janetcode"
	r.NoteProvenance([]byte(`{"type":"result","harness":"janetcode","result":"ok","served":{"model":"xiaomi/mimo-v2.6-flash","tool_ids":"tool_24"},"provenance_mismatches":3,"provenance_mismatch":"requested xiaomi/mimo-v2.6-flash: served by moonshot, not xiaomi (tool ids tool_24)"}`))
	if r.Mismatches != 3 || r.Served == nil || r.Served.ToolIDs != "tool_24" || !strings.Contains(r.Mismatch, "moonshot") {
		t.Fatalf("row = %+v", r)
	}
	r.Passed = true
	tot := AggregateHarness([]BenchTaskResult{r, {Harness: "janetcode", Passed: true}})
	if tot.Passed != 1 || tot.Mismatched != 1 || tot.Failed != 0 {
		t.Fatalf("totals = %+v", tot)
	}
}
