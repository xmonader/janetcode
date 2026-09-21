package agent

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/jev"
	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/session"
	"github.com/xmonader/janetcode/internal/verify"
)

// fakeDecisions is a DecisionProvider with canned answers.
type fakeDecisions struct {
	res   *jev.Result
	err   error
	avail bool
	calls int
}

func (f *fakeDecisions) Evaluate(_ context.Context, _ string, _ []jev.Question) (*jev.Result, error) {
	f.calls++
	return f.res, f.err
}
func (f *fakeDecisions) Available() bool { return f.avail }

func triageResult(caused float64, class string, conf float64) *jev.Result {
	return &jev.Result{Model: "jev-1.13.0", Answers: map[string]jev.Answer{
		qCausedByChange: {Kind: jev.Noul, Noul: caused},
		qFailureClass:   {Kind: jev.Choice, Choice: class, Confidence: conf},
		qWorthRepair:    {Kind: jev.Noul, Noul: 0.5},
	}}
}

// A live gate failure pays for every question it asks, so it asks only
// the ones the mapping reads; worth_repair is recorded by the offline
// replay and nothing else, so it lives in that set alone.
func TestLiveTriageAsksOnlyWhatTheMappingReads(t *testing.T) {
	ids := func(qs []jev.Question) map[string]bool {
		m := map[string]bool{}
		for _, q := range qs {
			m[q.ID] = true
		}
		return m
	}
	live := ids(triageQuestions())
	if len(live) != 2 || !live[qCausedByChange] || !live[qFailureClass] {
		t.Fatalf("live questions = %v", live)
	}
	if live[qWorthRepair] {
		t.Fatal("worth_repair is asked live but triageAction never reads it")
	}
	replay := ids(TriageQuestions())
	if len(replay) != 3 || !replay[qWorthRepair] || !replay[qCausedByChange] || !replay[qFailureClass] {
		t.Fatalf("replay questions = %v", replay)
	}
	// The replay set is built from the live one, so building it must
	// not leave the live one changed.
	if len(triageQuestions()) != 2 {
		t.Fatal("TriageQuestions mutated the live set")
	}
}

// The mapping is the whole feature; it is pure, so it is tested
// exhaustively rather than through the loop.
func TestTriageActionMapping(t *testing.T) {
	cases := []struct {
		name   string
		caused float64
		class  string
		conf   float64
		skip   bool
	}{
		{"confident patch-caused sends back", 0.9, "patch_caused", 0.9, false},
		{"barely over the line sends back", triageCaused, "environmental", 0.99, false},
		{"confident environmental skips", 0.1, "environmental", 0.8, true},
		{"confident flaky skips", 0.05, "flaky", 0.9, true},
		{"confident unrelated skips", 0.2, "unrelated", triageClassConfidence, true},
		{"low confidence in the class sends back", 0.1, "environmental", 0.5, false},
		{"unconfident class cannot skip", 0.1, "inconclusive", 0.95, false},
		{"inconclusive never skips", 0.1, "inconclusive", 0.99, false},
		{"patch_caused never skips however sure", 0.1, "patch_caused", 0.99, false},
		{"middle band sends back", 0.5, "environmental", 0.99, false},
		{"boundary: just under skips", triageNotCaused - 0.01, "flaky", triageClassConfidence, true},
		{"boundary: just over the band sends back", triageNotCaused + 0.01, "flaky", triageClassConfidence, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := triageAction(triageResult(c.caused, c.class, c.conf))
			if got.skip != c.skip {
				t.Fatalf("skip = %v (p=%.2f class=%s conf=%.2f)", got.skip, c.caused, c.class, c.conf)
			}
		})
	}
	// A missing answer of either kind is today's behaviour.
	for _, res := range []*jev.Result{
		{Answers: map[string]jev.Answer{}},
		nil,
	} {
		if res != nil && triageAction(res).skip {
			t.Fatal("empty answers skipped a round")
		}
	}
}

// The scenario the feature exists for: an environmental failure that
// today burns a full repair turn. With triage on and a confident
// answer, the turn ends and the model never sees the failure.
func TestTriageSkipEndsTheTurnWithoutARepairCall(t *testing.T) {
	f := &fake{responses: []*llm.Response{
		toolCall("t1", "write_file", `{"path":"a.txt","content":"v1"}`),
		text("done"), // gate runs: grep -q ok a.txt fails
	}}
	a, root := newAgent(t, f, nil)
	a.AutoApprove = true
	a.Log = &session.Memory{}
	a.Verify = &verify.Runner{Cwd: root, Config: verify.Config{Commands: []string{"grep -q ok a.txt"}}}
	fd := &fakeDecisions{res: triageResult(0.1, "environmental", 0.8), avail: true}
	a.Decisions = fd
	a.DecisionsTriage = true
	var notices []string
	if err := a.Run(context.Background(), "make the file say ok", func(e Event) {
		if e.Kind == EvNotice {
			notices = append(notices, e.Text)
		}
	}); err != nil {
		t.Fatal(err)
	}
	if len(f.requests) != 2 {
		t.Fatalf("requests = %d, want 2: the repair call must not happen", len(f.requests))
	}
	joined := strings.Join(notices, "\n")
	if !strings.Contains(joined, "not sending the model back") || !strings.Contains(joined, "repair round not spent (1/3)") {
		t.Fatalf("notices = %q", joined)
	}
	if fd.calls != 1 {
		t.Fatalf("decision calls = %d", fd.calls)
	}
	// The decision is on the log with its answers.
	var found *session.Event
	for _, e := range a.Log.Events() {
		if e.Type == session.TypeDecision {
			found = &e
		}
	}
	if found == nil {
		t.Fatal("no decision event on the log")
	}
	// The event is the audit record: questions asked, answers with their
	// distributions, tokens and latency — an offline replay needs the
	// whole payload, not a summary.
	extra := string(found.Extra)
	for _, want := range []string{`"questions"`, `"answers"`, `"tokens_in"`, `"caused_by_change"`, `failure_class`} {
		if !strings.Contains(extra, want) {
			t.Fatalf("decision Extra missing %s: %s", want, extra)
		}
	}
	// The failed check is still on the record: skipping the repair must
	// not tidy the verdict away.
	if st := a.Stats(); st.DecisionCalls != 1 {
		t.Fatalf("decision stats = %+v", st)
	}
}

// The common case: Jev agrees it is the patch's fault, so everything is
// exactly as it was without Jev — same number of model calls, feedback
// text intact.
func TestTriagePatchCausedFeedsBackAsAlways(t *testing.T) {
	f := &fake{responses: []*llm.Response{
		toolCall("t1", "write_file", `{"path":"a.txt","content":"v1"}`),
		text("done"),
		toolCall("t2", "write_file", `{"path":"a.txt","content":"ok"}`),
		text("fixed"),
	}}
	a, root := newAgent(t, f, nil)
	a.AutoApprove = true
	a.Verify = &verify.Runner{Cwd: root, Config: verify.Config{Commands: []string{"grep -q ok a.txt"}}}
	a.Decisions = &fakeDecisions{res: triageResult(0.9, "patch_caused", 0.8), avail: true}
	a.DecisionsTriage = true
	if err := a.Run(context.Background(), "go", nil); err != nil {
		t.Fatal(err)
	}
	if len(f.requests) != 4 {
		t.Fatalf("requests = %d, want 4 (the ordinary repair round)", len(f.requests))
	}
	last := f.requests[2].Messages
	if fb := last[len(last)-1].Text(); !strings.Contains(fb, "Verification failed") {
		t.Fatalf("feedback = %q", fb)
	}
}

// Every soft failure — provider error, closed circuit, switch off —
// must equal the behaviour with no provider at all.
func TestTriageFailsOpen(t *testing.T) {
	run := func(fd *fakeDecisions, off map[string]bool) int {
		f := &fake{responses: []*llm.Response{
			toolCall("t1", "write_file", `{"path":"a.txt","content":"v1"}`),
			text("done"),
			toolCall("t2", "write_file", `{"path":"a.txt","content":"ok"}`),
			text("fixed"),
		}}
		a, root := newAgent(t, f, nil)
		a.AutoApprove = true
		a.Verify = &verify.Runner{Cwd: root, Config: verify.Config{Commands: []string{"grep -q ok a.txt"}}}
		if fd != nil {
			a.Decisions = fd
			a.DecisionsTriage = true
		}
		a.Off = off
		if err := a.Run(context.Background(), "go", nil); err != nil {
			t.Fatal(err)
		}
		return len(f.requests)
	}
	baseline := run(nil, nil)
	if baseline != 4 {
		t.Fatalf("baseline requests = %d", baseline)
	}
	for name, fd := range map[string]*fakeDecisions{
		"error":   {err: errors.New("boom"), avail: true},
		"unavail": {res: triageResult(0.1, "environmental", 0.9)},
		"garbage": {res: &jev.Result{Answers: map[string]jev.Answer{}}, avail: true},
	} {
		t.Run(name, func(t *testing.T) {
			if got := run(fd, nil); got != baseline {
				t.Fatalf("requests = %d, want the baseline %d", got, baseline)
			}
		})
	}
	t.Run("switch off", func(t *testing.T) {
		fd := &fakeDecisions{res: triageResult(0.1, "environmental", 0.9), avail: true}
		if got := run(fd, map[string]bool{"decisions": true}); got != baseline {
			t.Fatalf("requests = %d with decisions off", got)
		}
		if fd.calls != 0 {
			t.Fatalf("decisions off still called the provider %d times", fd.calls)
		}
	})
}

func TestDecisionLine(t *testing.T) {
	var s Stats
	if s.DecisionLine() != "" {
		t.Fatal("empty stats must say nothing")
	}
	s.DecisionCalls = 2
	if got, want := s.DecisionLine(), "decisions: 2 calls"; got != want {
		t.Fatalf("DecisionLine = %q, want %q", got, want)
	}
	s.DecisionErrors = 1
	if got, want := s.DecisionLine(), "decisions: 2 calls · 1 errored"; got != want {
		t.Fatalf("DecisionLine = %q, want %q", got, want)
	}
}
