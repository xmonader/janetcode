package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/perm"
	"github.com/xmonader/janetcode/internal/session"
	"github.com/xmonader/janetcode/internal/shell"
	"github.com/xmonader/janetcode/internal/snapshot"
	"github.com/xmonader/janetcode/internal/textfmt"
	"github.com/xmonader/janetcode/internal/tool"
)

// The probe ledger (ProbeLedger): the inline checks a model runs after it
// edits — `python3 - <<'EOF' ... assert ... EOF`, `node -e` with a throw —
// are the rules it believes the task states. They used to vanish once
// they ran. The ledger keeps them, with no model call of its own:
//
//   - each new probe that still passes is run once more against the tree
//     as it was before the turn's first change (the undo checkpoint's
//     snapshot). One that failed there and passes now is discriminating:
//     it tells the change apart from the code it replaced, and it is
//     frozen in harness state, where no edit can reach it. One that
//     passed there too is vacuous: it tests nothing the change did.
//   - every frozen probe is replayed at each later gate. One that fails
//     goes back to the model like a failed check, so fixing rule B cannot
//     quietly break rule A that the model already showed holds.
//   - the spec review is told which probes were vacuous, so it rereads
//     the rules those were meant to check instead of trusting them.
//
// It is opt-in (config probe_ledger), because it reruns commands the
// model wrote and can send a turn back, and nothing on effbench has
// measured it yet; JANETCODE_OFF=probes turns it off again. A probe is
// rerun through the bash tool itself, so the sandbox holds it, and only
// when the permission rules would let that call run without asking.

// probeAssertRe is a script that can fail on purpose: an assert, a throw,
// a raise or an explicit non-zero exit. A script with none of them exits
// 0 whatever the code does, so passing proves nothing.
var probeAssertRe = regexp.MustCompile(`(?i)\bassert\w*\b|\bthrow\b|\braise\b|sys\.exit\(|process\.exit\(`)

// Limits that keep the ledger cheap: a gate classifies at most
// maxNewProbes probes (two runs each), the ledger holds at most
// maxFrozenProbes, each run is bounded by probeTimeout, and one turn is
// sent back for failing probes at most maxProbeRounds times.
const (
	maxNewProbes    = 6
	maxFrozenProbes = 16
	probeTimeout    = 60 * time.Second
	maxProbeRounds  = 2
)

// probeCdFailed is the exit code a pre-edit run takes when it cannot
// enter the exported tree — a docker sandbox does not see the host's
// temporary directory — so that run says nothing about the code.
const probeCdFailed = 97

// Probe is one inline check a turn ran after it had edited something.
type Probe struct {
	// Cmd is the command as the model ran it; Key is the same command
	// without a leading cd into the workspace, for telling two runs of
	// one probe apart from two probes.
	Cmd, Key string
	// Seq is the session sequence number of its last run.
	Seq int
	// Stale says a successful edit came after that run: the probe passed
	// on a tree the turn has since changed.
	Stale bool
}

// IsProbe reports whether a bash command is an inline probe: an
// interpreter fed a script inline (see scratchRe) that can fail on
// purpose, and not a test runner with a script tacked on behind it.
func IsProbe(cmd string) bool {
	loc := scratchRe.FindStringIndex(cmd)
	if loc == nil || !probeAssertRe.MatchString(cmd[loc[0]:]) {
		return false
	}
	return !shell.IsTestRun(cmd[:loc[0]])
}

// InlineProbes lists the probes of one turn whose last run passed, in the
// order they first ran, counting only runs after the turn's first
// successful edit: a check before any change describes the old code, not
// a rule the change was meant to meet. A probe whose last run failed is
// the verdict's business — a red check — not the ledger's. Background
// runs and runs fed stdin are left out, since a replay could not repeat
// them.
func InlineProbes(events []session.Event, turn int, root string) []Probe {
	type run struct {
		cmd    string
		seq    int
		failed bool
	}
	latest := map[string]run{}
	var order []string
	edited, lastEdit := false, 0
	for _, e := range events {
		if e.Type != session.TypeToolResult || e.Turn != turn || e.CallID == "" || refused(e) {
			continue
		}
		if editTool(e.Name) && !e.IsError {
			edited, lastEdit = true, e.Seq
			continue
		}
		if e.Name != "bash" || !edited || neverRan(e) {
			continue
		}
		var in struct {
			Command    string  `json:"command"`
			Background bool    `json:"background"`
			Stdin      *string `json:"stdin"`
		}
		if json.Unmarshal([]byte(e.Arguments), &in) != nil || in.Background || in.Stdin != nil {
			continue
		}
		cmd := strings.TrimSpace(in.Command)
		if !IsProbe(cmd) {
			continue
		}
		key := shell.TrimWorkspaceCd(cmd, root)
		if _, ok := latest[key]; !ok {
			order = append(order, key)
		}
		latest[key] = run{cmd: cmd, seq: e.Seq, failed: runFailed(e)}
	}
	var out []Probe
	for _, key := range order {
		r := latest[key]
		if r.failed {
			continue
		}
		out = append(out, Probe{Cmd: r.cmd, Key: key, Seq: r.seq, Stale: lastEdit > r.seq})
	}
	return out
}

// probeOutcome is what one harness run of a probe showed.
type probeOutcome int

const (
	// probeInconclusive: the run said nothing about the code — it could
	// not run, was not allowed to, never started, or timed out.
	probeInconclusive probeOutcome = iota
	probePass
	probeFail
)

// probeResult is one run's outcome and, for a failure, its output.
type probeResult struct {
	outcome probeOutcome
	output  string
}

// frozenProbe is a probe the ledger replays at every gate.
type frozenProbe struct {
	key, cmd string
	// disputedTurn is the turn the probe was last sent back in, 0 if it
	// never was. A probe still failing in a later turn was declined —
	// the model finished without making it pass — and is retired rather
	// than sent back for ever.
	disputedTurn int
}

// probeLedger is the session's probe state.
type probeLedger struct {
	frozen []frozenProbe
	// seen holds every probe key already classified, so a probe is run
	// against the pre-edit tree once however many gates follow.
	seen map[string]bool
	// turn is the turn vacuous belongs to.
	turn    int
	vacuous []string
}

// probeGateState is one turn's probe gate.
type probeGateState struct {
	seen   int // a.mutations when the probes last ran
	rounds int
}

// SetProbeLedger turns the probe ledger on or off for the turns that
// follow.
func (a *Agent) SetProbeLedger(on bool) {
	a.featMu.Lock()
	a.ProbeLedger = on
	a.featMu.Unlock()
}

// ProbeLedgerOn is ProbeLedger, safe to read while a turn runs, and off
// when JANETCODE_OFF names probes.
func (a *Agent) ProbeLedgerOn() bool {
	a.featMu.RLock()
	defer a.featMu.RUnlock()
	return (a.ProbeLedger || a.ProbeMemory && !a.Off["probememory"]) && !a.Off["probes"]
}

// probeGate classifies the turn's new probes and replays the frozen ones
// when the turn has changed something since it last did. It reports
// whether the turn was sent back because a frozen probe fails.
func (a *Agent) probeGate(ctx context.Context, g *probeGateState, emit func(Event)) bool {
	if !a.ProbeLedgerOn() || a.Log == nil || a.mutations == 0 || a.mutations == g.seen || a.readOnly() || a.DryRunOn() {
		return false
	}
	g.seen = a.mutations
	led := &a.probes
	if led.turn != a.turnN {
		led.turn, led.vacuous = a.turnN, nil
	}
	if led.seen == nil {
		led.seen = map[string]bool{}
	}
	a.probeMemArm(ctx, led, emit)
	var fresh []Probe
	for _, p := range InlineProbes(a.logEvents(), a.turnN, a.displayRoot()) {
		if !led.seen[p.Key] && !a.gateCommand(p.Key) {
			fresh = append(fresh, p)
		}
	}
	if len(fresh) > maxNewProbes {
		fresh = fresh[len(fresh)-maxNewProbes:]
	}
	var failing []failedProbe
	frozenNow := map[string]bool{}
	discriminating, vacuous, unclassified := 0, 0, 0
	pre, preTried := "", false
	defer func() {
		if pre != "" {
			os.RemoveAll(pre)
		}
	}()
	for _, p := range fresh {
		if ctx.Err() != nil {
			return false
		}
		led.seen[p.Key] = true
		now := a.runProbe(ctx, p.Cmd, "")
		switch {
		case now.outcome == probeInconclusive:
			unclassified++
			continue
		case now.outcome == probeFail && p.Stale:
			// It passed for the model, and an edit since has broken it.
			led.freeze(p)
			frozenNow[p.Key] = true
			failing = append(failing, failedProbe{p.Key, now.output})
			continue
		case now.outcome == probeFail:
			// It passed for the model on this very tree and fails now:
			// the probe depends on something besides the code.
			unclassified++
			continue
		}
		if !preTried {
			preTried = true
			pre = a.preEditTree(ctx)
		}
		if pre == "" {
			unclassified++
			continue
		}
		switch a.runProbe(ctx, p.Cmd, pre).outcome {
		case probeFail:
			discriminating++
			led.freeze(p)
			a.probeMemRemember(ctx, p)
			frozenNow[p.Key] = true
		case probePass:
			vacuous++
			led.vacuous = append(led.vacuous, p.Key)
		default:
			unclassified++
		}
	}
	replayed := 0
	for i := 0; i < len(led.frozen); i++ {
		fp := led.frozen[i]
		if frozenNow[fp.key] || ctx.Err() != nil {
			continue
		}
		replayed++
		res := a.runProbe(ctx, fp.cmd, "")
		if res.outcome != probeFail {
			continue
		}
		if fp.disputedTurn != 0 && fp.disputedTurn != a.turnN {
			emit(Event{Kind: EvNotice, Text: "probe ledger: dropping a probe the model left failing last turn: " + checkLabel(fp.key)})
			led.frozen = append(led.frozen[:i], led.frozen[i+1:]...)
			a.probeMemRetire(ctx, fp.key)
			i--
			continue
		}
		failing = append(failing, failedProbe{fp.key, a.probeMemNote(fp.key) + res.output})
	}
	if ctx.Err() != nil {
		return false
	}
	if len(fresh) > 0 || replayed > 0 {
		summary := fmt.Sprintf("%d new probe(s): %d discriminating, %d vacuous, %d unclassified · %d replayed · %d failing · %d frozen",
			len(fresh), discriminating, vacuous, unclassified, replayed, len(failing), len(led.frozen))
		a.logEvent(session.Event{Type: session.TypeNotice, Turn: a.turnN, Name: "probes/ledger", Output: summary})
		emit(Event{Kind: EvNotice, Text: "probe ledger: " + summary})
	}
	if len(failing) == 0 {
		return false
	}
	if g.rounds >= maxProbeRounds {
		emit(Event{Kind: EvNotice, Text: fmt.Sprintf("probe ledger: %s still failing after %d rounds; not sending the model back again", textfmt.Plural(len(failing), "probe"), maxProbeRounds)})
		return false
	}
	g.rounds++
	for _, f := range failing {
		led.dispute(f.key, a.turnN)
	}
	emit(Event{Kind: EvNotice, Text: fmt.Sprintf("probe ledger: %s that passed earlier in this session now fail; sending the model back (%d/%d)", textfmt.Plural(len(failing), "probe"), g.rounds, maxProbeRounds)})
	a.appendHarnessText(probeFeedback(failing))
	return true
}

// failedProbe is a probe that failed on the current tree, with its output.
type failedProbe struct{ key, output string }

// probeFeedback is what the model reads when probes it had passed fail.
func probeFeedback(failing []failedProbe) string {
	var b strings.Builder
	b.WriteString("[harness] Checks you ran yourself earlier, and that passed then, fail on the code as it is now. A later change broke the rule each one checks:\n")
	per := 4000 / max(1, len(failing))
	for _, f := range failing {
		fmt.Fprintf(&b, "\n$ %s\n```\n%s\n```\n", checkLabel(f.key), textfmt.TruncateTo(strings.TrimSpace(f.output), per))
	}
	b.WriteString("\nMake them pass again without undoing what the later change was for, then run the project's tests once. If a check was itself wrong, say so in one line and finish.")
	return b.String()
}

// freeze adds a probe to the ledger, dropping the oldest past the limit.
func (l *probeLedger) freeze(p Probe) {
	for _, f := range l.frozen {
		if f.key == p.Key {
			return
		}
	}
	l.frozen = append(l.frozen, frozenProbe{key: p.Key, cmd: p.Cmd})
	if len(l.frozen) > maxFrozenProbes {
		l.frozen = l.frozen[len(l.frozen)-maxFrozenProbes:]
	}
}

// dispute marks a frozen probe as sent back in turn.
func (l *probeLedger) dispute(key string, turn int) {
	for i := range l.frozen {
		if l.frozen[i].key == key {
			l.frozen[i].disputedTurn = turn
		}
	}
}

// probeReviewFacts is what the spec review is told about this turn's
// vacuous probes, or "" when there were none.
func (a *Agent) probeReviewFacts() string {
	led := &a.probes
	if !a.ProbeLedgerOn() || led.turn != a.turnN || len(led.vacuous) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("[harness] These checks you ran after editing also pass on the code as it was before your first change, so they do not show that your change meets the rule each was meant to check. Hold those rules against the code when you read it:\n")
	for i, k := range led.vacuous {
		if i == 4 {
			fmt.Fprintf(&b, "- and %d more\n", len(led.vacuous)-i)
			break
		}
		b.WriteString("- " + textfmt.FirstLine(checkLabel(k), 160) + "\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// gateCommand reports whether cmd is one of the project's verify
// commands, which the gate runs on its own.
func (a *Agent) gateCommand(cmd string) bool {
	gate, _ := a.VerifyGate()
	if gate == nil {
		return false
	}
	for _, c := range gate.Config.Commands {
		if strings.TrimSpace(c) == cmd {
			return true
		}
	}
	return false
}

// runProbe runs a probe in the workspace, or with dir set in that copy of
// it: a leading cd goes first, and every mention of the workspace's path
// in the command is pointed at the copy, or `cd /work && python3 ...`
// would run against the work tree after all.
func (a *Agent) runProbe(ctx context.Context, cmd, dir string) probeResult {
	if dir != "" {
		if root := a.displayRoot(); root != "" {
			cmd = strings.ReplaceAll(cmd, root, dir)
		}
		cmd = fmt.Sprintf("cd %s || exit %d\n%s", shellQuote(dir), probeCdFailed, cmd)
	}
	if a.probeExec != nil {
		return a.probeExec(ctx, cmd)
	}
	if a.Tools == nil {
		return probeResult{}
	}
	t, ok := a.Tools.Get("bash")
	if !ok {
		return probeResult{}
	}
	input, _ := json.Marshal(map[string]any{"command": cmd, "timeout": probeTimeout.Milliseconds()})
	if !a.probeAllowed(t, input) {
		return probeResult{}
	}
	out, err := t.Run(ctx, input)
	if err != nil {
		return probeResult{}
	}
	return judgeProbeRun(string(input), out)
}

// judgeProbeRun reads a bash result as a probe's outcome.
func judgeProbeRun(arguments, output string) probeResult {
	e := session.Event{Arguments: arguments, Output: output}
	if neverRan(e) || strings.Contains(output, "[timed out") {
		return probeResult{}
	}
	if m := reExitCode.FindAllStringSubmatch(output, -1); len(m) > 0 && m[len(m)-1][1] == fmt.Sprint(probeCdFailed) {
		return probeResult{}
	}
	if runFailed(e) {
		return probeResult{outcome: probeFail, output: output}
	}
	return probeResult{outcome: probePass}
}

// probeAllowed reports whether the rules let a probe's bash call run
// without anyone being asked: the harness reruns it unattended, so a call
// that would have needed an answer is not made at all.
func (a *Agent) probeAllowed(t tool.Tool, input json.RawMessage) bool {
	u := llm.Block{Type: llm.BlockToolUse, Name: "bash", Input: input}
	_, verdict, rule, hard := a.ruleDecision(&u)
	if hard != "" || verdict == perm.Denied || verdict == perm.Ask && !rule.IsZero() {
		return false
	}
	return !a.mustPrompt(t, &u, false, verdict, false)
}

// preEditTree exports the tree as it was before the turn's first change
// into a new temporary directory, and returns it, or "" when there is no
// snapshot to export. The caller removes it.
func (a *Agent) preEditTree(ctx context.Context) string {
	if a.probePreTree != nil {
		return a.probePreTree(ctx)
	}
	if runtime.GOOS == "windows" {
		// The pre-edit run is a POSIX `cd` and a quoted path.
		return ""
	}
	snap := a.cp.turnSnap()
	ex, ok := a.snaps().(interface {
		Export(context.Context, snapshot.Snap, string) error
	})
	if snap == nil || !ok {
		return ""
	}
	dir, err := os.MkdirTemp("", "janetcode-probe-")
	if err != nil {
		return ""
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if ex.Export(ctx, *snap, dir) != nil {
		os.RemoveAll(dir)
		return ""
	}
	return dir
}

// turnSnap is the git snapshot taken before the running turn's first
// change, or nil.
func (c *checkpointer) turnSnap() *snapshot.Snap {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.open || len(c.all) == 0 || c.all[len(c.all)-1].Snap == nil {
		return nil
	}
	s := *c.all[len(c.all)-1].Snap
	return &s
}

// shellQuote quotes s for a POSIX shell.
func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }
