package main

import (
	"os"
	"path/filepath"

	"github.com/xmonader/janetcode/internal/agent"
	"github.com/xmonader/janetcode/internal/config"
	"github.com/xmonader/janetcode/internal/eval"
	"github.com/xmonader/janetcode/internal/localize/tree"
	"github.com/xmonader/janetcode/internal/settingsapply"
	"github.com/xmonader/janetcode/internal/verify"
)

// evalGateTimeout bounds one verify command inside an eval run. Eval
// tasks are small; a check that runs longer than this has hung, and the
// task's own wall budget is what the run is measured against.
const evalGateTimeout = 120

// evalVerifyRunner is the verification gate an eval task runs under when
// --eval-gate is given, or nil when nothing can be derived. A task that
// ships its own .janetcode/verify.json wins — it is part of the fixture,
// so it needs no trust prompt — and otherwise the commands come from the
// project type of the work directory, which is what a real project's
// verify.json almost always says for these languages.
//
// The gate is opt-in because every existing benchmark in this repository
// was recorded without one: a gate changes tool counts, pass rates and
// cost on every task, so turning it on by default would silently
// invalidate the baselines.
// hiddenGate, when set, is the held-out acceptance gate for this run: it
// is appended as the last command, under a display label, so the failure
// the model reads names the failing tests and never the script that ran
// them (see internal/eval/hidden.go and verify.Runner.Display).
func evalVerifyRunner(root, hiddenGate string) *verify.Runner {
	cfg, _, err := verify.Load(root)
	if err != nil || len(cfg.Commands) == 0 {
		cmds := evalProjectChecks(root)
		if len(cmds) == 0 && hiddenGate == "" {
			return nil
		}
		cfg = verify.Config{Commands: cmds, Timeout: evalGateTimeout}
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = evalGateTimeout
	}
	var display map[string]string
	if hiddenGate != "" {
		command := eval.HiddenGateCommand(hiddenGate, root)
		cfg.Commands = append(cfg.Commands, command)
		display = map[string]string{command: eval.HiddenGateLabel}
	}
	// No Mutate: mutation testing is a second experiment and would
	// dominate the wall time of a task that takes a minute to run.
	return &verify.Runner{Config: cfg, Cwd: root, Display: display}
}

// evalProjectChecks reads the work directory's project type and returns
// the checks for it. Unknown project types get no gate rather than a
// guessed command: a gate that fails for its own reasons would feed the
// model noise and count as a repair round.
func evalProjectChecks(root string) []string {
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err == nil {
		return []string{"go build ./...", "go test ./..."}
	}
	if py, _ := filepath.Glob(filepath.Join(root, "*.py")); len(py) > 0 {
		return []string{"python3 -m compileall -q ."}
	}
	return nil
}

// wireEvalGate puts the verify gate and the decision provider on an eval
// agent, the same two things a real session gets from cd.go's
// "verification" step and stepDecisions. Decisions follow the config and
// --eval-off exactly as a session does, so an arm with decisions off is
// a gate-only run and the pair differs in one thing.
func wireEvalGate(ag *agent.Agent, root string, cfg *config.Config, off map[string]bool, hiddenGate string) {
	if r := evalVerifyRunner(root, hiddenGate); r != nil {
		ag.SetVerify(r, false)
	}
	// The stale-check nudge is deterministic and costs no model call, but
	// it rides the same gate as triage does. It was reported as part of a
	// measured arm once while the eval agent never set it, so an arm that
	// turns it on in the config now actually gets it.
	ag.StaleCheckNudge = cfg.StaleCheckNudge
	// The effort controller likewise, so an arm that sets
	// effort_control is the arm it says it is.
	ag.EffortControl = cfg.EffortControl
	// Round-trip elision is opt-in for the same reason, and an arm that
	// asks for it has to get it.
	ag.ElideChecks = cfg.ElideChecks
	// Localization rides the gate too, and is measured the same way.
	ag.LocalizeFailures = cfg.LocalizeFailures
	ag.Localize = tree.Run
	ag.SubagentMerge = cfg.SubagentMerge
	ag.ProbeLedger = cfg.ProbeLedger
	ag.BlindTest = blindTester(cfg)
	ag.SetSpecCoverage(cfg.SpecCoverage)
	ag.SetSpecExamples(cfg.SpecExamples)
	settingsapply.WireDecisions(cfg, ag, off)
}
