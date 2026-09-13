package whatif

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/xmonader/janetcode/internal/agent"
	"github.com/xmonader/janetcode/internal/config"
	"github.com/xmonader/janetcode/internal/eval"
	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/llm/replay"
	"github.com/xmonader/janetcode/internal/perm"
	"github.com/xmonader/janetcode/internal/prompt"
	"github.com/xmonader/janetcode/internal/repomap"
	"github.com/xmonader/janetcode/internal/session"
)

// turnPrompt is one turn's driving text: what Run feeds to agent.Run to
// reproduce it.
type turnPrompt struct {
	Turn int
	Text string
}

// sessionTurns reads the session's turn boundaries and driving text
// straight from its event log: session.TypeTurnStart marks each real call
// to Agent.Run the session ever made (its Turn field is exactly
// agent.Agent's own turn counter), and session.TypeUserMessage's first
// event per turn is the text that call was given. Both matter for
// replay's turn-count alignment: replay.Provider serves its recorded
// responses strictly in order regardless of what Run feeds it (see
// internal/llm/replay), so Run must call agent.Run exactly once per turn
// the recording actually made — including a turn whose only "prompt" was
// synthetic (a /goal round, an undo note) — or the response queue
// desyncs from the turn after the skipped one on. This is why Run does
// not use eval.Keep's userPrompts: that helper drops synthetic and
// mid-turn entries on purpose, which is right for turning a session into
// a fresh eval task (turn numbers there are irrelevant, only the prompts
// spoken by a person matter) and wrong here, where turn numbers are load
// bearing.
func sessionTurns(events []session.Event) []turnPrompt {
	maxTurn := 0
	for _, e := range events {
		if e.Type == session.TypeTurnStart && e.Turn > maxTurn {
			maxTurn = e.Turn
		}
	}
	if maxTurn == 0 {
		return nil
	}
	text := make([]string, maxTurn+1)
	seen := make([]bool, maxTurn+1)
	for _, e := range events {
		if e.Type != session.TypeUserMessage || e.Message == nil || e.Turn <= 0 || e.Turn > maxTurn || seen[e.Turn] {
			continue
		}
		seen[e.Turn] = true
		text[e.Turn] = e.Message.Text()
	}
	out := make([]turnPrompt, 0, maxTurn)
	for t := 1; t <= maxTurn; t++ {
		out = append(out, turnPrompt{Turn: t, Text: text[t]})
	}
	return out
}

// resolveSession turns --what-if's argument into a session file: arg is
// either a path to a .jsonl file (anything containing a path separator,
// or ending in .jsonl) or a bare session id looked up under session.Dir().
func resolveSession(arg string) (path, dir, id string, err error) {
	arg = strings.TrimSpace(arg)
	if arg == "" {
		return "", "", "", fmt.Errorf("--what-if needs a session id or path")
	}
	if strings.ContainsAny(arg, "/\\") || strings.HasSuffix(arg, ".jsonl") {
		p := arg
		if !strings.HasSuffix(p, ".jsonl") {
			p += ".jsonl"
		}
		if _, statErr := os.Stat(p); statErr != nil {
			return "", "", "", fmt.Errorf("%s: %w", arg, statErr)
		}
		abs, aerr := filepath.Abs(p)
		if aerr != nil {
			abs = p
		}
		return abs, filepath.Dir(abs), strings.TrimSuffix(filepath.Base(abs), ".jsonl"), nil
	}
	d, derr := session.Dir()
	if derr != nil {
		return "", "", "", derr
	}
	p := filepath.Join(d, arg+".jsonl")
	if _, statErr := os.Stat(p); statErr != nil {
		return "", "", "", fmt.Errorf("no session %q (janetcode --sessions lists them)", arg)
	}
	return p, d, arg, nil
}

// buildAgent wires the what-if agent: the real (not read-only) tool set
// eval.Tools gives an eval run, rooted at workDir, plus the todo tool and
// a code-execution tool the same way evals add them (see
// cmd/janetcode/eval.go's runEval and internal/agent's buildAgent) — no
// MCP, no hooks, no memory or skills, matching docs/manual/evals.md's
// "minimal agent" convention. That convention is a deliberate choice, not
// an oversight: the tools a real session carries beyond this (job
// control, a terminal, fetch/web-search, memory, skills, subagents, a
// cron) need wiring (config paths, a running process registry, a browser
// or terminal) that has no meaning inside a throwaway worktree, and
// reproducing main's full buildAgent here would mean carrying that
// wiring into a package that has no session, no config directory and no
// process of its own to hang it on. Run notes it in the report when the
// original session actually used a tool outside this set, so the gap is
// visible rather than silent.
func buildAgent(workDir string, off map[string]bool, cfg *config.Config) (*agent.Agent, []string) {
	var notes []string
	archiveDir, aerr := os.MkdirTemp("", "janetcode-whatif-archive-")
	if aerr != nil {
		notes = append(notes, "tool-output archive unavailable: "+aerr.Error())
	}
	archive := agent.NewArchive(archiveDir)
	expand := &agent.ExpandOutput{Archive: archive}
	repo := &repomap.Cache{Root: workDir}
	ag := &agent.Agent{
		Log:           &session.Memory{},
		Archive:       archive,
		Tools:         eval.Tools(workDir, repo, expand, off),
		System:        prompt.System(workDir),
		Off:           off,
		Model:         cfg.Model,
		MaxTokens:     cfg.MaxTokens,
		MaxIterations: cfg.MaxIterations,
		CompactAt:     cfg.CompactAt,
		ContextWindow: cfg.MetaFor(cfg.Model).ContextWindow,
		Price:         cfg.PriceFor(cfg.Model),
	}
	expand.Log = ag.LogEvent
	if !off["todo"] {
		ag.Tools.Add(&agent.TodoTool{Parent: ag})
	}
	if script := (&agent.Script{Parent: ag, Root: workDir}); !off["script"] && script.Available() {
		ag.Tools.Add(script)
	} else {
		ag.Tools.Add(&agent.RunCode{Parent: ag})
	}
	if !off["repomap"] {
		ag.Prepare = repomap.TurnPrep(repo, 0)
	}
	ag.PlanCheck = repo.CheckPlan
	ag.Tools.Intent = cfg.IntentEnabled() && !off["intent"]
	return ag, notes
}

// subUsage is a minus b, field by field; llm.Usage only ever accumulates
// (Add), so Run keeps its own subtraction for splitting the live part's
// spend out of the running total.
func subUsage(a, b llm.Usage) llm.Usage {
	return llm.Usage{
		InputTokens:      a.InputTokens - b.InputTokens,
		OutputTokens:     a.OutputTokens - b.OutputTokens,
		CacheReadTokens:  a.CacheReadTokens - b.CacheReadTokens,
		CacheWriteTokens: a.CacheWriteTokens - b.CacheWriteTokens,
		CostUSD:          a.CostUSD - b.CostUSD,
	}
}

// foldUsage sums every model call's usage across a run's event log.
func foldUsage(events []session.Event) llm.Usage {
	var u llm.Usage
	for _, e := range events {
		if e.Type == session.TypeAssistant && e.Usage != nil {
			u.Add(*e.Usage)
		}
	}
	return u
}

// toolCallSeq is the ordinary tool calls at or after minTurn, in order,
// named and with their raw arguments — the sequence Divergence compares.
// A script/run_code call's synthetic sub-calls (CallID prefixed
// "script-"/"runcode-") are excluded: their ids and order are that tool's
// own implementation detail, not a decision comparable across two runs
// the way an ordinary call is (cmd/janetcode/replay.go's recordedCalls
// excludes them from its own comparison for the same reason).
//
// root is the directory the run worked in, so a call's target can be
// written relative to it: the original ran in the user's checkout and the
// what-if in a worktree, and an absolute path would differ for no reason.
func toolCallSeq(events []session.Event, minTurn int, root string) []CallInfo {
	var out []CallInfo
	for _, e := range events {
		if e.Type != session.TypeToolCall || e.Turn < minTurn {
			continue
		}
		if strings.HasPrefix(e.CallID, "script-") || strings.HasPrefix(e.CallID, "runcode-") {
			continue
		}
		out = append(out, CallInfo{Turn: e.Turn, Name: e.Name, Target: callTarget(e.Name, e.Arguments, root), Arguments: e.Arguments})
	}
	return out
}

// callTarget is what a call acts on, the way a permission rule sees it
// (perm.Subject: the path of a file tool, the command of bash, the URL of
// a fetch), with root made relative and whitespace collapsed.
func callTarget(name, args, root string) string {
	t := perm.Subject(name, json.RawMessage(args))
	if root != "" {
		if rel, err := filepath.Rel(root, t); err == nil && filepath.IsAbs(t) && !strings.HasPrefix(rel, "..") {
			t = rel
		}
		t = strings.ReplaceAll(t, root+string(filepath.Separator), "")
	}
	return strings.Join(strings.Fields(t), " ")
}

// firstDivergence finds the first index where orig and now, each already
// filtered to calls at or after FromTurn, stop doing the same thing: a
// different tool, a different target (file, command, URL), or one run
// making a call the other did not. Two calls on the same target that only
// phrase their arguments differently — an edit written as a line range in
// one run and as an edits list in the other — are the same action; they
// are counted in argDiffs instead, so a formatting difference no longer
// hides the decision that actually changed.
func firstDivergence(orig, now []CallInfo) (d *Divergence, argDiffs int) {
	n := max(len(orig), len(now))
	for i := 0; i < n; i++ {
		var o, w *CallInfo
		if i < len(orig) {
			c := orig[i]
			o = &c
		}
		if i < len(now) {
			c := now[i]
			w = &c
		}
		if o != nil && w != nil && o.Name == w.Name && o.Target == w.Target {
			if o.Arguments != w.Arguments {
				argDiffs++
			}
			continue
		}
		turn := 0
		switch {
		case o != nil:
			turn = o.Turn
		case w != nil:
			turn = w.Turn
		}
		return &Divergence{Turn: turn, Index: i, Original: o, Now: w}, argDiffs
	}
	return nil, argDiffs
}

// foldVerdicts is agent.FoldOutcome for each of turnNums, in order.
func foldVerdicts(events []session.Event, turnNums []int) []TurnVerdict {
	out := make([]TurnVerdict, 0, len(turnNums))
	for _, t := range turnNums {
		o := agent.FoldOutcome(events, t)
		out = append(out, TurnVerdict{Turn: t, Verdict: o.Verdict, Why: o.Why})
	}
	return out
}

// foldFileStats reduces a run's agent.Change records (see
// internal/agent/provenance.go, the record /undo, /proof and blame all
// share) to one net line-count per path: the earliest recorded Before and
// the latest recorded After, diffed once, rather than summing every
// intermediate edit's own stats and double-counting lines that were added
// and later removed again in the same run.
func foldFileStats(changes []agent.Change) []FileStat {
	type span struct {
		before, after []byte
		existed       bool
	}
	spans := map[string]*span{}
	var order []string
	for _, c := range changes {
		s, ok := spans[c.Path]
		if !ok {
			s = &span{before: c.Before, existed: c.Existed}
			spans[c.Path] = s
			order = append(order, c.Path)
		}
		s.after = c.After
	}
	sort.Strings(order)
	var out []FileStat
	for _, p := range order {
		s := spans[p]
		add, del := (agent.Change{Before: s.before, After: s.after, Existed: s.existed}).Stats()
		if add == 0 && del == 0 {
			continue
		}
		out = append(out, FileStat{Path: p, Add: add, Del: del})
	}
	return out
}

// loadOriginalFiles reads the original session's file changes from its
// undo sidecar (session.UndoPath): the same agent.Change records the live
// agent's own Changes() returns, loaded the same way LoadUndo restores
// them for /undo on a resume, so both sides of the comparison are folded
// by the one function (foldFileStats) rather than two.
func loadOriginalFiles(sess *session.Session) ([]FileStat, error) {
	tmp := &agent.Agent{UndoPath: sess.UndoPath()}
	if err := tmp.LoadUndo(); err != nil {
		return nil, err
	}
	return foldFileStats(tmp.Changes()), nil
}

// usedTools is the ordinary tool names (see toolCallSeq's exclusion) a
// session's events called at least once.
func usedTools(events []session.Event) []string {
	seen := map[string]bool{}
	for _, e := range events {
		if e.Type != session.TypeToolCall || e.Name == "" {
			continue
		}
		if strings.HasPrefix(e.CallID, "script-") || strings.HasPrefix(e.CallID, "runcode-") {
			continue
		}
		seen[e.Name] = true
	}
	names := make([]string, 0, len(seen))
	for name := range seen {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// missingFrom is the entries of used that are not in have.
func missingFrom(used, have []string) []string {
	haveSet := make(map[string]bool, len(have))
	for _, h := range have {
		haveSet[h] = true
	}
	var out []string
	for _, u := range used {
		if !haveSet[u] {
			out = append(out, u)
		}
	}
	return out
}

// recordedCallCount is how many recorded model calls the recording holds
// in total, for sizing the generous MaxIterations Run uses while
// replaying (see Run): Remaining before anything has been consumed is the
// whole count.
func recordedCallCount(p *replay.Provider) int { return p.Remaining() }
