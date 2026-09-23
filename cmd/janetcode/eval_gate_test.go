package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/xmonader/janetcode/internal/agent"
	"github.com/xmonader/janetcode/internal/config"
	"github.com/xmonader/janetcode/internal/jev"
	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/minimal"
	"github.com/xmonader/janetcode/internal/prompt"
	"github.com/xmonader/janetcode/internal/session"
)

// TestEvalProjectChecksFollowTheWorkDir covers the three shapes an eval
// work directory comes in: a Go module, a Python task, and something the
// harness has no checks for — which must get no gate rather than a
// guessed command that would fail for its own reasons.
func TestEvalProjectChecksFollowTheWorkDir(t *testing.T) {
	goDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(goDir, "go.mod"), []byte("module x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := evalProjectChecks(goDir); len(got) != 2 || got[0] != "go build ./..." || got[1] != "go test ./..." {
		t.Fatalf("go checks = %v", got)
	}
	pyDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(pyDir, "tally.py"), []byte("x = 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := evalProjectChecks(pyDir); len(got) != 1 || !strings.Contains(got[0], "compileall") {
		t.Fatalf("python checks = %v", got)
	}
	if got := evalProjectChecks(t.TempDir()); got != nil {
		t.Fatalf("unknown project checks = %v, want none", got)
	}
	if r := evalVerifyRunner(t.TempDir(), ""); r != nil {
		t.Fatal("a directory with no project type got a gate")
	}
}

// A task that ships its own .janetcode/verify.json is the fixture
// speaking: it wins over the project type, and needs no trust prompt.
func TestEvalVerifyRunnerPrefersTheTaskFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, ".janetcode"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".janetcode", "verify.json"),
		[]byte(`{"commands":["make check"]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	r := evalVerifyRunner(dir, "")
	if r == nil || len(r.Config.Commands) != 1 || r.Config.Commands[0] != "make check" {
		t.Fatalf("runner = %+v", r)
	}
	if r.Config.Timeout != evalGateTimeout {
		t.Fatalf("timeout = %d, want the eval default %d", r.Config.Timeout, evalGateTimeout)
	}
	if r.Mutate != nil {
		t.Fatal("the eval gate must not run mutation testing")
	}
}

// evalGateTestConfig is a decisions block with both consumers on and a
// key environment variable the test owns.
func evalGateTestConfig(t *testing.T, key string) *config.Config {
	t.Helper()
	t.Setenv("JEV_EVAL_TEST_KEY", key)
	return &config.Config{Model: "m", Decisions: &config.DecisionsConfig{
		Model: "jev-1.13.0", Triage: true, Intake: true, APIKeyEnv: "JEV_EVAL_TEST_KEY",
	}}
}

// The flag's two states, on one agent each: on wires both the gate and
// the provider; --eval-off decisions leaves the gate and drops the
// provider, which is the pair the comparison runs.
func TestWireEvalGateOnAndDecisionsOff(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := evalGateTestConfig(t, "k")
	ag := evalTestAgent(t, dir, cfg)
	wireEvalGate(ag, dir, cfg, nil, "")
	gate, _ := ag.VerifyGate()
	if !gate.Enabled() {
		t.Fatal("no verify gate after --eval-gate")
	}
	if ag.Decisions == nil || !ag.DecisionsTriage || !ag.DecisionsIntake {
		t.Fatalf("decisions = %v triage=%v intake=%v", ag.Decisions, ag.DecisionsTriage, ag.DecisionsIntake)
	}
	if _, ok := ag.Decisions.(*jev.Client); !ok {
		t.Fatalf("Decisions is %T, want the same *jev.Client a session gets", ag.Decisions)
	}

	off := evalTestAgent(t, dir, cfg)
	wireEvalGate(off, dir, cfg, map[string]bool{"decisions": true}, "")
	gate, _ = off.VerifyGate()
	if !gate.Enabled() {
		t.Fatal("--eval-off decisions must keep the gate: that is the control arm")
	}
	if off.Decisions != nil || off.DecisionsTriage {
		t.Fatalf("decisions survived --eval-off: %v", off.Decisions)
	}
}

// Without the flag the eval agent is what it always was: no gate, no
// provider, so every results file recorded before --eval-gate existed
// stays comparable.
func TestEvalAgentHasNoGateWithoutTheFlag(t *testing.T) {
	dir := t.TempDir()
	cfg := evalGateTestConfig(t, "k")
	ag := evalTestAgent(t, dir, cfg)
	if gate, untrusted := ag.VerifyGate(); gate.Enabled() || untrusted {
		t.Fatalf("gate = %+v untrusted = %v", gate, untrusted)
	}
	if ag.Decisions != nil || ag.DecisionsTriage || ag.DecisionsIntake {
		t.Fatal("the eval agent wired decisions without --eval-gate")
	}
}

// A keyless decisions block wires nothing, so a run started without the
// key in the environment is a gate-only run rather than a silent half
// of the experiment.
func TestWireEvalGateWithoutAKeyWiresNoProvider(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := evalGateTestConfig(t, "")
	ag := evalTestAgent(t, dir, cfg)
	wireEvalGate(ag, dir, cfg, nil, "")
	if gate, _ := ag.VerifyGate(); !gate.Enabled() {
		t.Fatal("the gate is independent of the key")
	}
	if ag.Decisions != nil {
		t.Fatal("a keyless config wired a client")
	}
}

// fakeEvalDecisions is a DecisionProvider that answers without a
// network: a confident environmental verdict, which is the answer that
// skips a repair round. Intake asks from its own goroutine while triage
// asks from the turn's, so the count is atomic.
type fakeEvalDecisions struct{ calls atomic.Int32 }

func (f *fakeEvalDecisions) Available() bool { return true }
func (f *fakeEvalDecisions) Evaluate(ctx context.Context, state string, qs []jev.Question) (*jev.Result, error) {
	f.calls.Add(1)
	return &jev.Result{Model: "jev-1.13.0", TokensIn: 430, TokensOut: 73, Cost: 0.00001806,
		Answers: map[string]jev.Answer{
			"caused_by_change": {Kind: jev.Noul, Noul: 0.1},
			"failure_class":    {Kind: jev.Choice, Choice: "environmental", Confidence: 0.9},
		}}, nil
}

// The end of the wiring that the blocker was about: with --eval-gate a
// failing check on an eval agent actually reaches triageFailure. The
// gate command fails deterministically and the provider is a fake, so
// nothing here touches the network.
func TestEvalGateFailureReachesTriage(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".janetcode"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".janetcode", "verify.json"),
		[]byte(`{"commands":["grep -q ok a.txt"]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := evalGateTestConfig(t, "k")
	f := &fakeResumeProvider{responses: []*llm.Response{
		resumeToolCall("t1", "write_file", `{"path":"a.txt","content":"v1"}`),
		resumeText("done"),
	}}
	ag, err := minimal.New(dir, cfg, f, nil, prompt.Override{}, t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	ag.AutoApprove = true
	ag.Log = &session.Memory{}
	wireEvalGate(ag, dir, cfg, nil, "")
	fd := &fakeEvalDecisions{}
	ag.Decisions = fd // the same field wireEvalGate set, with no network behind it
	var notices []string
	if err := ag.Run(context.Background(), "make the file say ok", func(e agent.Event) {
		if e.Kind == agent.EvNotice {
			notices = append(notices, e.Text)
		}
	}); err != nil {
		t.Fatal(err)
	}
	// Both consumers are on, so the provider is asked twice in this turn
	// (intake once, triage once); it is the triage decision on the log
	// that proves the gate failure got there.
	triaged := 0
	for _, e := range ag.Log.Events() {
		if e.Type == session.TypeDecision && e.Name == "verify_triage" {
			triaged++
		}
	}
	if triaged != 1 {
		t.Fatalf("verify_triage decisions = %d (provider calls %d), want 1: the gate failure never reached triageFailure", triaged, fd.calls.Load())
	}
	joined := strings.Join(notices, "\n")
	if !strings.Contains(joined, "repair round not spent") {
		t.Fatalf("notices = %q", joined)
	}
}

// evalTestAgent builds the eval agent the same way runEval does, so
// these tests assert on the real thing rather than a bare agent.
func evalTestAgent(t *testing.T, dir string, cfg *config.Config) *agent.Agent {
	t.Helper()
	ag, err := minimal.New(dir, cfg, nil, nil, prompt.Override{}, t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	return ag
}
