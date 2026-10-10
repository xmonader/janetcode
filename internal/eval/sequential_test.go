package eval

import (
	"bytes"
	"context"
	"math"
	"math/rand"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/agent"
	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/tool"
)

func seqResult(name string, run int, passed bool, out int) Result {
	return Result{Name: name, Run: run, Passed: passed, Usage: llm.Usage{OutputTokens: out}}
}

// TestSequentialNoEffectStaysBelowAlpha is the property the whole thing is
// for: with no difference between the arms, looking after every pair and
// stopping at the first crossing still reports an effect at most alpha of
// the time. A fixed-sample test peeked at this often would not.
func TestSequentialNoEffectStaysBelowAlpha(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	const trials, pairs = 400, 120
	false_ := 0
	for range trials {
		var base, cur []Result
		for i := range pairs {
			name := string(rune('a' + i%16))
			run := i / 16
			out := 1000 + rng.Intn(2000)
			base = append(base, seqResult(name, run, rng.Float64() < 0.6, out))
			cur = append(cur, seqResult(name, run, rng.Float64() < 0.6, int(float64(out)*math.Exp(rng.NormFloat64()*0.3))))
		}
		if CompareSequential(base, cur, 0.05).Decided(SeqAny) {
			false_++
		}
	}
	if rate := float64(false_) / trials; rate > 0.06 {
		t.Fatalf("no-effect A/Bs decided %.1f%% of the time, above the 5%% the test promises", 100*rate)
	}
}

// TestSequentialStopsEarlyOnAClearEffect: a change that wins every
// discordant pair decides well before the planned number of runs.
func TestSequentialStopsEarlyOnAClearEffect(t *testing.T) {
	var base, cur []Result
	for i := range 64 {
		name := string(rune('a' + i%16))
		base = append(base, seqResult(name, i/16, false, 1000))
		cur = append(cur, seqResult(name, i/16, true, 1000))
	}
	s := CompareSequential(base, cur, 0.05)
	if s.PassStop == 0 || s.PassStop > 16 {
		t.Fatalf("eight straight wins clear 0.025 (LR=2^8/9); stop at %d, p=%.3g", s.PassStop, s.PassP)
	}
	if !s.Decided(SeqPass) || s.Decided(SeqOutput) {
		t.Fatalf("gates: %+v", s)
	}
	var b bytes.Buffer
	WriteSequential(&b, s)
	if !strings.Contains(b.String(), "decided (better)") {
		t.Fatalf("report:\n%s", b.String())
	}
}

// TestSequentialTiesCarryNoEvidence: both arms passing (or both failing)
// every run says nothing about which is better, however many runs.
func TestSequentialTiesCarryNoEvidence(t *testing.T) {
	var base, cur []Result
	for i := range 200 {
		base = append(base, seqResult("t", i, i%2 == 0, 0))
		cur = append(cur, seqResult("t", i, i%2 == 0, 0))
	}
	s := CompareSequential(base, cur, 0.05)
	if s.Ties != 200 || s.PassP != 1 || s.Decided(SeqAny) {
		t.Fatalf("%+v", s)
	}
}

// TestSequentialOutputGate: a consistent 40% cut in output tokens is
// decided within a round or two; three pairs that all moved are not yet
// evidence (three heads in a row is LR 2, nowhere near 40).
func TestSequentialOutputGate(t *testing.T) {
	rng := rand.New(rand.NewSource(3))
	var base, cur []Result
	for i := range 40 {
		out := 2000 + rng.Intn(1000)
		base = append(base, seqResult(string(rune('a'+i)), 0, true, out))
		cur = append(cur, seqResult(string(rune('a'+i)), 0, true, int(float64(out)*0.6*math.Exp(rng.NormFloat64()*0.1))))
	}
	s := CompareSequential(base, cur, 0.05)
	if s.OutputStop == 0 || s.OutputStop > 16 || s.OutputRatio > 0.7 || s.Fewer < s.OutputStop {
		t.Fatalf("output gate: stop %d ratio %.2f p %.3g", s.OutputStop, s.OutputRatio, s.OutputP)
	}
	if short := CompareSequential(base[:3], cur[:3], 0.05); short.OutputStop != 0 {
		t.Fatalf("three pairs decided: %+v", short)
	}
	var b bytes.Buffer
	WriteSequential(&b, s)
	if !strings.Contains(b.String(), "decided (fewer)") {
		t.Fatalf("report:\n%s", b.String())
	}
}

// TestSequentialCountsDroppedPairs: runs lost to the broker are counted per
// arm and kept out, never scored as failures and never silently gone.
func TestSequentialCountsDroppedPairs(t *testing.T) {
	infra := func(r Result) Result { r.Infra = true; return r }
	base := []Result{
		seqResult("a", 0, true, 1), infra(seqResult("b", 0, false, 1)), seqResult("c", 0, true, 1),
		infra(seqResult("d", 0, false, 1)), seqResult("only-base", 0, true, 1),
	}
	cur := []Result{
		seqResult("a", 0, true, 1), seqResult("b", 0, true, 1), infra(seqResult("c", 0, false, 1)),
		infra(seqResult("d", 0, false, 1)), seqResult("only-cur", 0, true, 1),
	}
	cur[2].Mismatches = 0
	s := CompareSequential(base, cur, 0.05)
	if s.Pairs != 4 || s.Used != 1 || s.DroppedBase != 1 || s.DroppedCur != 1 || s.DroppedBoth != 1 || s.Unpaired != 2 {
		t.Fatalf("%+v", s)
	}
	if s.Wins != 0 || s.Losses != 0 {
		t.Fatalf("a dropped pair moved the test: %+v", s)
	}
	var b bytes.Buffer
	WriteSequential(&b, s)
	if !strings.Contains(b.String(), "1 lost in base only · 1 in current only · 1 in both") || !strings.Contains(b.String(), "2 runs in one set only") {
		t.Fatalf("report:\n%s", b.String())
	}
}

func TestParseSeqGate(t *testing.T) {
	for _, s := range []string{"pass", "output", "any"} {
		if g, err := ParseSeqGate(s); err != nil || string(g) != s {
			t.Fatalf("%s: %v", s, err)
		}
	}
	if _, err := ParseSeqGate("cost"); err == nil {
		t.Fatal("cost is not a gate")
	}
}

// TestRunRoundsStopsWhenAsked: rounds run whole, carry their index as Run,
// and stop as soon as the callback says the evidence is decisive.
func TestRunRoundsStopsWhenAsked(t *testing.T) {
	r := &Runner{Runs: 5, NewAgent: func(root, _ string) (*agent.Agent, error) {
		return &agent.Agent{Provider: &scripted{}, Tools: tool.NewRegistry(tool.WriteFile{Root: root}), Model: "m"}, nil
	}}
	tasks := []Task{{Name: "a", Prompt: "p", Check: "true"}, {Name: "b", Prompt: "p", Check: "true"}}
	asked := 0
	res := r.RunRounds(context.Background(), tasks, func(done []Result) bool {
		asked++
		return len(done) >= 4
	})
	if len(res) != 4 || asked != 2 || res[2].Run != 1 || res[3].Name != "b" {
		t.Fatalf("asked %d, results %+v", asked, res)
	}
	// Never asked after the last round: there is nothing left to save.
	r.Runs = 1
	asked = 0
	if res := r.RunRounds(context.Background(), tasks, func([]Result) bool { asked++; return true }); len(res) != 2 || asked != 0 {
		t.Fatalf("asked %d after the only round", asked)
	}
}
