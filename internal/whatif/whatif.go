// Package whatif implements counterfactual replay: "what would have
// happened if...". Given a session that already ran and a turn N, it
// reproduces the tree the session had right before turn N by replaying
// turns 1..N-1 against the model's own recorded replies — no tokens
// spent, see internal/replay — in an isolated git worktree (see
// worktree.AtStart), then runs the session's remaining user
// prompts live against a changed setup: a different model, ablations, an
// extra permission-deny rule, or a replacement prompt for turn N. It
// reports how the two runs compare: where they first diverge, per-turn
// verdicts, files changed, a check run in the resulting worktree, tool
// calls, tokens and cost.
//
// cmd/janetcode's --what-if flag only parses arguments and prints
// Report.Text() or Report.JSON(); everything else — resolving the
// session, preparing the worktree, building the agent, driving the
// turns, and folding the comparison — is here.
package whatif

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/xmonader/janetcode/internal/agent"
	"github.com/xmonader/janetcode/internal/config"
	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/mutate"
	"github.com/xmonader/janetcode/internal/perm"
	"github.com/xmonader/janetcode/internal/replay"
	"github.com/xmonader/janetcode/internal/session"
	"github.com/xmonader/janetcode/internal/textfmt"
	"github.com/xmonader/janetcode/internal/verify"
	"github.com/xmonader/janetcode/internal/worktree"
)

// Options configures one counterfactual run.
type Options struct {
	// SessionArg is the --what-if argument: a session id (looked up under
	// session.Dir()) or a path to its .jsonl file directly.
	SessionArg string
	// Root is the repository the session's recorded start state replays
	// against (see session.StartState, worktree.AtStart). Empty falls
	// back to the session's own recorded Meta.Cwd.
	Root string
	// FromTurn is N, 1-based: turns before it replay from the recording,
	// it and every turn after run live.
	FromTurn int

	// Config carries the live model, its token/iteration/compaction
	// defaults and its price table. Only its fields, not Config as a
	// whole, decide anything: a *config.Config is threaded through so a
	// caller does not have to restate what cmd/janetcode's own flags
	// already resolved (--model, --provider, --max-turns, ...).
	Config *config.Config
	// Provider is the live backend used from FromTurn on.
	Provider llm.Provider

	// Off ablates named behaviours for the live part, same names and
	// meaning as --eval-off / JANETCODE_OFF (see agent.KnownSwitch).
	Off []string
	// Deny adds extra permission-deny rules for the live part (see
	// perm.Parse); a deny rule wins even under AutoApprove.
	Deny []string
	// Prompt, when set, replaces the text of turn FromTurn; empty keeps
	// the session's own recorded prompt for it.
	Prompt string

	// Keep leaves the worktree on disk instead of removing it; Report.Dir
	// names it either way, but it is only valid after Run returns when
	// Keep is true.
	Keep bool
	// Log, when set, receives one line per turn as the run proceeds.
	Log func(format string, args ...any)
}

// Summary is one run's facts: what happened turn by turn, which files it
// touched, and what it cost. Original and Now (see Report) are both this
// shape so they line up in a comparison.
type Summary struct {
	Turns     []TurnVerdict `json:"turns"`
	Files     []FileStat    `json:"files,omitempty"`
	ToolCalls int           `json:"tool_calls"`
	Usage     llm.Usage     `json:"usage"`
	CostUSD   float64       `json:"cost_usd"`
	CostKnown bool          `json:"cost_known"`
}

// TurnVerdict is one turn's outcome (see agent.FoldOutcome): what the
// turn's own check result was, for both the original session (its
// recorded events already show this — no check is re-run for it, see
// Report's doc) and the what-if run.
type TurnVerdict struct {
	Turn    int           `json:"turn"`
	Verdict agent.Verdict `json:"verdict"`
	Why     string        `json:"why"`
}

// FileStat is one file's net line change across a whole run, folded from
// agent.Change records (see internal/agent/provenance.go): the same
// before/after content /undo and /proof already keep. Only write_file and
// edit_file are tracked this way (see tool.Toucher); a file changed
// purely by a bash command has no entry here on either side, a known,
// symmetric gap noted in Report.Notes.
type FileStat struct {
	Path string `json:"path"`
	Add  int    `json:"add"`
	Del  int    `json:"del"`
}

// CallInfo is one tool call, named and with its raw arguments, for
// Divergence.
type CallInfo struct {
	Turn int    `json:"turn"`
	Name string `json:"name"`
	// Target is what the call acts on (see callTarget); two calls with
	// the same name and target are the same action.
	Target    string `json:"target"`
	Arguments string `json:"arguments"`
}

// Divergence is the first tool call, at or after FromTurn, where the
// original session and the what-if run disagree — by name, by arguments,
// or because one made a call the other did not. Original or Now is nil
// when that side had no call at Index (one run made fewer calls than the
// other from FromTurn on).
type Divergence struct {
	Turn     int       `json:"turn"`
	Index    int       `json:"index"` // 0-based, among calls at or after FromTurn
	Original *CallInfo `json:"original,omitempty"`
	Now      *CallInfo `json:"now,omitempty"`
}

// CheckResult is one run of the project's verify commands (see
// internal/verify) in a worktree.
type CheckResult struct {
	Passed   bool          `json:"passed"`
	Failed   string        `json:"failed,omitempty"`
	Output   string        `json:"output,omitempty"`
	Duration time.Duration `json:"duration"`
}

// Report is what Run returns: the comparison between the session as it
// actually happened (Original) and as it happened with one thing changed
// from FromTurn on (Now).
//
// Original's Check is never set: re-running its checks would need its own
// tree materialised a second time, which nothing here does — its
// TurnVerdict list (folded from its own recorded events, the same way
// Now's is) is the substitute, and stands for whatever its checks showed
// at the time. Now.Check is the project's real verify commands, run once
// in the worktree after the whole comparison run finishes.
type Report struct {
	SessionID string `json:"session_id"`
	FromTurn  int    `json:"from_turn"`
	// Model is the live model actually used from FromTurn on.
	Model            string   `json:"model"`
	Off              []string `json:"off,omitempty"`
	Deny             []string `json:"deny,omitempty"`
	PromptOverridden bool     `json:"prompt_overridden"`

	Divergence *Divergence `json:"divergence,omitempty"`
	// ArgumentDiffs counts calls before the divergence that did the same
	// thing to the same target but phrased their arguments differently.
	ArgumentDiffs int `json:"argument_diffs,omitempty"`

	Original Summary `json:"original"`
	Now      Summary `json:"now"`

	// LiveUsage, LiveCostUSD and LiveCostKnown are the tokens and cost
	// this run actually bought, from FromTurn on. Now.Usage/CostUSD also
	// include the turns before FromTurn, which cost nothing new — they
	// were replayed from the recording, not bought again — so the two
	// figures answer different questions: Now.CostUSD is "what would the
	// whole session, as changed, have cost", LiveCostUSD is "what did
	// this run actually spend".
	LiveUsage     llm.Usage `json:"live_usage"`
	LiveCostUSD   float64   `json:"live_cost_usd"`
	LiveCostKnown bool      `json:"live_cost_known"`

	Check *CheckResult `json:"check,omitempty"`

	// Dir is the worktree's path when Options.Keep asked to keep it;
	// empty otherwise, since a removed directory is not a fact worth
	// reporting.
	Dir string `json:"dir,omitempty"`

	Notes []string `json:"notes,omitempty"`
}

// Text renders the report for a terminal.
func (r Report) Text() string {
	var b strings.Builder
	fmt.Fprintf(&b, "what-if: %s from turn %d, live model %s\n", r.SessionID, r.FromTurn, r.Model)
	if len(r.Off) > 0 {
		fmt.Fprintf(&b, "off: %s\n", strings.Join(r.Off, ", "))
	}
	if len(r.Deny) > 0 {
		fmt.Fprintf(&b, "deny: %s\n", strings.Join(r.Deny, ", "))
	}
	if r.PromptOverridden {
		fmt.Fprintf(&b, "turn %d's prompt was replaced\n", r.FromTurn)
	}
	b.WriteString("\n")
	if r.Divergence == nil {
		b.WriteString("no divergence: the same actions on the same targets, in the same order, from turn " + fmt.Sprint(r.FromTurn) + " on\n")
	} else {
		d := r.Divergence
		fmt.Fprintf(&b, "first divergence: turn %d, call #%d\n", d.Turn, d.Index+1)
		fmt.Fprintf(&b, "  original: %s\n", callLine(d.Original))
		fmt.Fprintf(&b, "  now:      %s\n", callLine(d.Now))
	}
	if r.ArgumentDiffs > 0 {
		fmt.Fprintf(&b, "(%d earlier call(s) did the same thing with differently phrased arguments)\n", r.ArgumentDiffs)
	}
	b.WriteString("\nper-turn verdicts:\n")
	writeVerdicts(&b, "original", r.Original.Turns)
	writeVerdicts(&b, "now     ", r.Now.Turns)
	b.WriteString("\nfiles changed:\n")
	writeFiles(&b, "original", r.Original.Files)
	writeFiles(&b, "now     ", r.Now.Files)
	if r.Check != nil {
		b.WriteString("\ncheck (now, in the worktree): ")
		if r.Check.Passed {
			fmt.Fprintf(&b, "passed (%s)\n", r.Check.Duration.Round(100*time.Millisecond))
		} else {
			fmt.Fprintf(&b, "failed: %s\n", r.Check.Failed)
			if r.Check.Output != "" {
				fmt.Fprintf(&b, "%s\n", r.Check.Output)
			}
		}
	}
	b.WriteString("\ntool calls: original ")
	fmt.Fprintf(&b, "%d, now %d\n", r.Original.ToolCalls, r.Now.ToolCalls)
	fmt.Fprintf(&b, "tokens: original %s, now %s (of which %s bought live from turn %d on)\n",
		r.Original.Usage.TokenLine(), r.Now.Usage.TokenLine(), r.LiveUsage.TokenLine(), r.FromTurn)
	fmt.Fprintf(&b, "cost: original %s, now %s (%s bought live)\n",
		costStr(r.Original.CostUSD, r.Original.CostKnown), costStr(r.Now.CostUSD, r.Now.CostKnown), costStr(r.LiveCostUSD, r.LiveCostKnown))
	if r.Dir != "" {
		fmt.Fprintf(&b, "\nworktree kept at %s\n", r.Dir)
	}
	for _, n := range r.Notes {
		fmt.Fprintf(&b, "\nnote: %s\n", n)
	}
	return b.String()
}

func callLine(c *CallInfo) string {
	if c == nil {
		return "(no call here)"
	}
	if c.Target != "" {
		return fmt.Sprintf("turn %d: %s %s", c.Turn, c.Name, textfmt.Clip(c.Target, 100))
	}
	return fmt.Sprintf("turn %d: %s(%s)", c.Turn, c.Name, textfmt.Clip(strings.TrimSpace(c.Arguments), 100))
}

func writeVerdicts(b *strings.Builder, label string, turns []TurnVerdict) {
	fmt.Fprintf(b, "  %s:", label)
	for _, t := range turns {
		fmt.Fprintf(b, " %d=%s", t.Turn, t.Verdict)
	}
	b.WriteString("\n")
}

func writeFiles(b *strings.Builder, label string, files []FileStat) {
	if len(files) == 0 {
		fmt.Fprintf(b, "  %s: (none)\n", label)
		return
	}
	fmt.Fprintf(b, "  %s:\n", label)
	for _, f := range files {
		fmt.Fprintf(b, "    %s +%d -%d\n", f.Path, f.Add, f.Del)
	}
}

func costStr(usd float64, known bool) string {
	if !known {
		return "?"
	}
	return llm.FormatUSDPrecise(usd)
}

// JSON renders the report as indented JSON, --what-if --json's output.
func (r Report) JSON() ([]byte, error) { return json.MarshalIndent(r, "", "  ") }

// Run replays a session's turns before Options.FromTurn from its
// recording, then runs the rest live against the changed setup Options
// describes, and reports the comparison. See the package doc for the
// mechanism.
func Run(ctx context.Context, opts Options) (Report, error) {
	logf := opts.Log
	if logf == nil {
		logf = func(string, ...any) {}
	}

	path, dir, id, err := resolveSession(opts.SessionArg)
	if err != nil {
		return Report{}, err
	}
	sess, _, err := session.Open(dir, id)
	if err != nil {
		return Report{}, fmt.Errorf("open session %s: %w", opts.SessionArg, err)
	}
	origEvents := sess.Events()

	if sess.Meta.Start == nil || sess.Meta.Start.Snap == "" {
		return Report{}, fmt.Errorf("session %s has no recorded start state: it began before --what-if existed, before its repository had any commit, or outside a git repository", sess.Meta.ID)
	}
	if opts.Config == nil || opts.Provider == nil {
		return Report{}, fmt.Errorf("--what-if needs a configured provider for the live part")
	}

	turns := session.Turns(origEvents)
	total := len(turns)
	if total == 0 {
		return Report{}, fmt.Errorf("session %s has no recorded turns to replay", sess.Meta.ID)
	}
	n := opts.FromTurn
	switch {
	case n < 1:
		return Report{}, fmt.Errorf("--from-turn %d is out of range: turns are 1-based", n)
	case n > total:
		return Report{}, fmt.Errorf("--from-turn %d: the recording has only %d turn(s)", n, total)
	}

	off, err := agent.ParseSwitches(strings.Join(opts.Off, ","))
	if err != nil {
		return Report{}, fmt.Errorf("--off: %w", err)
	}
	denyRules, err := perm.ParseAll(opts.Deny)
	if err != nil {
		return Report{}, fmt.Errorf("--deny: %w", err)
	}

	root := strings.TrimSpace(opts.Root)
	if root == "" {
		root = sess.Meta.Cwd
	}
	logf("preparing a worktree at the session's start state (%s)", sess.Meta.ID)
	workDir, cleanup, err := worktree.AtStart(ctx, "whatif", root, sess.Meta.Start.Head, sess.Meta.Start.Snap)
	if err != nil {
		return Report{}, fmt.Errorf("prepare worktree: %w", err)
	}
	if !opts.Keep {
		defer cleanup()
	}

	recorded, err := replay.Load(path)
	if err != nil {
		return Report{}, fmt.Errorf("load recording: %w", err)
	}

	ag, notes := buildAgent(workDir, off, opts.Config)
	ag.Rules = perm.Rules{Deny: denyRules}
	ag.AutoApprove = true
	notes = append(notes, "every tool call in the worktree was auto-approved with no prompt: safe here because the whole run took place in an isolated git worktree, "+worktreeFate(opts.Keep))
	ag.Provider = replay.Until(recorded, n, opts.Provider)

	// Compaction and MaxIterations are the harness's own guardrails, not
	// part of what was recorded: a compaction summary calls the model on
	// its own (see internal/agent/compact.go), which would consume a
	// response meant for the next real turn and desync everything after
	// it, and a low MaxIterations could cut a replayed turn off before it
	// reaches the recorded end-of-turn reply. Both are disabled while
	// replaying and restored the moment the live part starts, where the
	// real provider makes them safe (and meaningful) again.
	ag.CompactAt = -1
	ag.MaxIterations = recordedCallCount(recorded) + total + 20

	var (
		preLive  llm.Usage
		lastTurn int
	)
	for _, tp := range turns {
		if tp.Turn == n {
			preLive = ag.Usage
			ag.CompactAt = opts.Config.CompactAt
			ag.MaxIterations = opts.Config.MaxIterations
			logf("turn %d: switching to the live model (%s)", tp.Turn, opts.Config.Model)
		}
		text := tp.Text
		if tp.Turn == n && strings.TrimSpace(opts.Prompt) != "" {
			text = opts.Prompt
		}
		runErr := ag.Run(ctx, text, nil)
		lastTurn = tp.Turn
		if runErr != nil {
			notes = append(notes, fmt.Sprintf("turn %d ended early: %v", tp.Turn, runErr))
			break
		}
	}

	liveUsage := subUsage(ag.Usage, preLive)
	nowEvents := ag.Log.Events()

	origPrice := opts.Config.PriceFor(sess.Meta.Model)
	livePrice := opts.Config.PriceFor(opts.Config.Model)

	origUsage := foldUsage(origEvents)
	origCost, origCostKnown := origUsage.Cost(origPrice)

	replayedCost, replayedKnown := preLive.Cost(origPrice)
	liveCost, liveKnown := liveUsage.Cost(livePrice)
	nowCost := replayedCost + liveCost
	nowCostKnown := replayedKnown && liveKnown

	origCalls := toolCallSeq(origEvents, 1, root)
	nowCalls := toolCallSeq(nowEvents, 1, workDir)
	origLiveCalls := toolCallSeq(origEvents, n, root)
	nowLiveCalls := toolCallSeq(nowEvents, n, workDir)
	divergence, argDiffs := firstDivergence(origLiveCalls, nowLiveCalls)

	origTurnNums := make([]int, total)
	for i := range origTurnNums {
		origTurnNums[i] = i + 1
	}
	nowTurnNums := make([]int, 0, lastTurn)
	for t := 1; t <= lastTurn; t++ {
		nowTurnNums = append(nowTurnNums, t)
	}

	origFiles, origChangeErr := loadOriginalFiles(sess)
	if origChangeErr != nil {
		notes = append(notes, "original file changes unavailable: "+origChangeErr.Error())
	}
	notes = append(notes, "files changed only counts write_file/edit_file: a file touched only by a bash command in either run does not appear here")

	var check *CheckResult
	if vcfg, _, verr := verify.Load(workDir); verr != nil {
		notes = append(notes, "could not load the worktree's verify config: "+verr.Error())
	} else if len(vcfg.Commands) == 0 {
		notes = append(notes, "no project verify commands (.janetcode/verify.json) in the worktree; nothing was checked at the end")
	} else {
		vr := (&verify.Runner{Config: vcfg, Cwd: workDir, Mutate: mutate.ForVerify}).Run(ctx)
		check = &CheckResult{Passed: vr.OK(), Failed: vr.Failed, Output: vr.Output, Duration: vr.Duration}
	}

	if used := usedTools(origEvents); len(used) > 0 {
		if missing := missingFrom(used, ag.Tools.Names()); len(missing) > 0 {
			notes = append(notes, fmt.Sprintf("the original session used tool(s) this run's minimal agent does not carry (%s), the same eval-style tool set docs/manual/evals.md describes rather than main's full session agent; a live turn that reached for one of these behaves differently for that reason alone", strings.Join(missing, ", ")))
		}
	}

	r := Report{
		SessionID:        sess.Meta.ID,
		FromTurn:         n,
		Model:            opts.Config.Model,
		Off:              opts.Off,
		Deny:             opts.Deny,
		PromptOverridden: strings.TrimSpace(opts.Prompt) != "",
		Divergence:       divergence,
		ArgumentDiffs:    argDiffs,
		Original: Summary{
			Turns:     foldVerdicts(origEvents, origTurnNums),
			Files:     origFiles,
			ToolCalls: len(origCalls),
			Usage:     origUsage,
			CostUSD:   origCost,
			CostKnown: origCostKnown,
		},
		Now: Summary{
			Turns:     foldVerdicts(nowEvents, nowTurnNums),
			Files:     foldFileStats(ag.Changes()),
			ToolCalls: len(nowCalls),
			Usage:     ag.Usage,
			CostUSD:   nowCost,
			CostKnown: nowCostKnown,
		},
		LiveUsage:     liveUsage,
		LiveCostUSD:   liveCost,
		LiveCostKnown: liveKnown,
		Check:         check,
		Notes:         notes,
	}
	if opts.Keep {
		r.Dir = workDir
	}
	return r, nil
}

func worktreeFate(keep bool) string {
	if keep {
		return "kept on disk for inspection rather than the real project tree"
	}
	return "removed once this run finished"
}
