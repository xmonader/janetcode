package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	"github.com/xmonader/janetcode/internal/hook"
	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/perm"
	"github.com/xmonader/janetcode/internal/session"
	"github.com/xmonader/janetcode/internal/shell"
	"github.com/xmonader/janetcode/internal/tool"
)

// MaxParallelTools bounds concurrent read-only tool execution.
const MaxParallelTools = 8

// executeAll runs the turn's tool calls: read-only tools concurrently, then
// mutating (permission-gated) tools one at a time in order. It always
// returns one result per call so the history stays valid, even when the
// batch stops part-way — cancelled, or with an approver that could not
// answer — and the error that stopped it is returned rather than dropped.
func (a *Agent) executeAll(ctx context.Context, uses []llm.Block, emit func(Event)) ([]llm.Block, []llm.Block, error) {
	a.setEmitter(emit)
	results := make([]llm.Block, len(uses))
	filled := make([]bool, len(uses))
	var (
		images []llm.Block
		imgMu  sync.Mutex
	)
	addImages := func(imgs []llm.Block) {
		if len(imgs) == 0 {
			return
		}
		imgMu.Lock()
		images = append(images, imgs...)
		imgMu.Unlock()
	}
	parallel := func(u llm.Block) bool {
		t, ok := a.Tools.Get(u.Name)
		return ok && !t.Mutating()
	}

	// The first error any call returned. execute only errors for
	// cancellation and for an approver that could not answer; either way
	// the batch stops and the error is reported, rather than being lost
	// behind results that claim the user cancelled.
	var (
		firstErr error
		errMu    sync.Mutex
	)
	fail := func(err error) {
		if err == nil {
			return
		}
		errMu.Lock()
		if firstErr == nil {
			firstErr = err
		}
		errMu.Unlock()
	}

	var wg sync.WaitGroup
	sem := make(chan struct{}, MaxParallelTools)
	for i, u := range uses {
		if !parallel(u) {
			continue
		}
		wg.Add(1)
		go func(i int, u llm.Block) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				fail(ctx.Err())
				return
			}
			out, imgs, isErr, err := a.execute(ctx, u, emit)
			if err != nil {
				fail(err)
				return
			}
			results[i] = llm.ToolResult(replyID(u), out, isErr)
			filled[i] = true
			addImages(imgs)
		}(i, u)
	}
	wg.Wait()

	// Every call is answered whatever happened: nothing may come between
	// a tool_use and its tool_result, so a call that never ran gets a
	// result saying so.
	stop := func(err error) ([]llm.Block, []llm.Block, error) {
		msg := "Cancelled by user before the tool finished."
		if !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
			msg = "Not run: the turn stopped before this call could be made (" + err.Error() + ")."
		}
		for i, u := range uses {
			if !filled[i] {
				results[i] = llm.ToolResult(replyID(u), msg, true)
			}
		}
		return results, images, err
	}
	if err := ctx.Err(); err != nil {
		return stop(err)
	}
	errMu.Lock()
	err := firstErr
	errMu.Unlock()
	if err != nil {
		return stop(err)
	}
	for i, u := range uses {
		if filled[i] {
			continue
		}
		// A completed call reports its real result even when the turn
		// was cancelled right after it, so a stop has to be checked
		// for here too — otherwise the next mutating call would start
		// (and could prompt) after the user already cancelled.
		if err := ctx.Err(); err != nil {
			return stop(err)
		}
		out, imgs, isErr, err := a.execute(ctx, u, emit)
		if err != nil {
			return stop(err)
		}
		results[i] = llm.ToolResult(replyID(u), out, isErr)
		filled[i] = true
		addImages(imgs)
	}
	return results, images, nil
}

// execute runs one tool call. The returned error is non-nil only for
// context cancellation; tool failures come back as (message, true, nil).
func (a *Agent) execute(ctx context.Context, u llm.Block, emit func(Event)) (string, []llm.Block, bool, error) {
	start := time.Now()
	// A permission prompt blocks inside the call, so the time the user
	// took to answer would otherwise be charged to the tool. The meter
	// rides on the context because tool calls can run concurrently.
	ctx, wait := meterWaits(ctx)
	out, images, isErr, err := a.executeTool(ctx, u, emit)
	a.stats.tool(u.Name, time.Since(start)-wait.total(), isErr || err != nil)
	return out, images, isErr, err
}

// authorize is everything that has to happen before a tool call may run:
// the pre-tool hook (which can rewrite the input or refuse the call), the
// preview and warning shown to the user, the permission rules, and the
// approval prompt for a mutating call. It returns the notes the hooks
// added, or the message explaining why the call must not proceed.
//
// It is separate from executeTool because a call made from inside a script
// has to pass exactly the same gates as one the model made directly: a
// script is a way to save round trips, not a way around the user.
//
// The steps below are in the order they must happen in: a hook sees the
// call first and may rewrite the input everything after it works on; the
// accept lock refuses before anything can allow; a deny rule refuses
// before anything asks; and only what none of those settled reaches the
// user.
func (a *Agent) authorize(ctx context.Context, t tool.Tool, u *llm.Block, call *ToolCall, emit func(Event)) ([]string, string, error) {
	hookNotes, hookAllowed, hookMustAsk, refusal := a.preToolHook(ctx, u, call)
	if refusal != "" {
		return nil, refusal, nil
	}
	mustAsk := a.describeCall(ctx, t, u, call)
	if hookMustAsk {
		// describeCall writes call.Warning from the tool's own Warner, so
		// this goes on afterwards or it would be overwritten.
		mustAsk = true
		call.Warning = strings.TrimPrefix(call.Warning+"; a hook from this repository rewrote this call's input", "; ")
	}
	// A write to a protected path always asks, under /edits, /auto, an
	// allow rule and --yolo alike, and the prompt says which path it is
	// asking about. It rides on the warning line the prompt already has,
	// so both front-ends show it without knowing about any of this.
	if t.Mutating() && !a.Off["protected"] {
		if why := a.Protected.Reason(perm.Call{Tool: u.Name, Input: u.Input, Root: a.displayRoot()}); why != "" {
			mustAsk = true
			if !strings.Contains(call.Warning, why) {
				call.Warning = strings.TrimPrefix(call.Warning+"; "+why, "; ")
			}
		}
	}
	// A locked accept-test file is refused like a denied permission rule:
	// before anything asks the user, and with no way for AutoApprove or a
	// saved rule to skip past it (see accept.Mode.Denial).
	if t.Mutating() {
		if reason := a.locks().Denial(t, u.Input); reason != "" {
			return nil, reason, nil
		}
	}
	subject, verdict, rule, hard := a.ruleDecision(u)
	// The hard deny tier: a command that destroys the machine is refused
	// before the rules are consulted at all, so no allow rule and no mode
	// can put it in front of the user as something to approve.
	if hard != "" {
		return nil, fmt.Sprintf("Refused: this command %s. janetcode never runs it, not even with auto-approve or an allow rule; do not retry it in another form.", hard), nil
	}
	if verdict == perm.Denied {
		return nil, fmt.Sprintf("Denied by permission rule %s. Do not retry this call; choose another approach or ask the user to change the rule.", rule), nil
	}
	switch {
	case verdict == perm.Allowed:
		call.Warning = strings.TrimPrefix(call.Warning+"; allowed by rule "+rule.String(), "; ")
	case hookAllowed:
		call.Warning = strings.TrimPrefix(call.Warning+"; allowed by a hook", "; ")
	}
	if !a.mustPrompt(t, u, mustAsk, verdict, hookAllowed) {
		return hookNotes, "", nil
	}
	if a.Approver == nil {
		return nil, "Permission denied: no approver configured for mutating tools.", nil
	}
	d, reason, suggested, err := a.ask(ctx, u, call, subject, mustAsk)
	if err != nil {
		return nil, "", err
	}
	if refusal := a.applyDecision(d, reason, suggested, u.Name, mustAsk, emit); refusal != "" {
		return nil, refusal, nil
	}
	return hookNotes, "", nil
}

// preToolHook runs the pre-tool hook, which may refuse the call outright,
// allow it past the prompt, or rewrite the input everything after this
// point — the preview, the approval and the call itself — works on.
func (a *Agent) preToolHook(ctx context.Context, u *llm.Block, call *ToolCall) (notes []string, allowed, mustAsk bool, refusal string) {
	hooks := a.HookRunner()
	if !hooks.Has(hook.PreTool) {
		return nil, false, false, ""
	}
	res := hooks.Run(ctx, hook.Input{Event: hook.PreTool, Cwd: hooks.Cwd, Tool: u.Name, Input: u.Input})
	if res.Denied {
		return nil, false, false, "Blocked by a hook: " + res.Reason
	}
	if !bytes.Equal(res.Input, u.Input) {
		// The rewritten input is what gets previewed, approved and run.
		u.Input = res.Input
		call.Input = res.Input
		notes = append(notes, "a hook rewrote this call's input to: "+string(res.Input))
		// When the repository's own hook wrote it, the user has to see it.
		// A rewrite is stronger than the allow decision a project hook is
		// already refused: it chooses the path a write lands in and the
		// command bash runs, and under --yolo nothing else would look.
		mustAsk = res.InputFromProject
	}
	return append(notes, res.Notes...), res.Allowed, mustAsk, ""
}

// describeCall fills in what the user would be shown about the call — the
// preview and the warning — and reports whether the tool's own guard says
// this input must be asked about however permissive the session is (a
// command reaching outside the workspace).
func (a *Agent) describeCall(ctx context.Context, t tool.Tool, u *llm.Block, call *ToolCall) (mustAsk bool) {
	if pv, ok := t.(tool.Previewer); ok {
		if p, err := pv.Preview(ctx, u.Input); err == nil {
			call.Preview = p
		} else {
			call.Preview = "preview unavailable: " + err.Error()
		}
	}
	if w, ok := t.(tool.Warner); ok {
		call.Warning = w.Warn(u.Input)
	}
	if g, ok := t.(tool.Guard); ok && t.Mutating() {
		if ask, why := g.Guard(u.Input); ask {
			mustAsk = true
			if why != "" && !strings.Contains(call.Warning, why) {
				call.Warning = strings.TrimPrefix(call.Warning+"; "+why, "; ")
			}
		}
	}
	return mustAsk
}

// ruleDecision is what the persistent permission rules say about this
// call, with the subject they matched on (which the ask step needs too).
func (a *Agent) ruleDecision(u *llm.Block) (subject string, verdict perm.Verdict, rule perm.Rule, hard string) {
	subject = perm.SubjectIn(u.Name, u.Input, a.displayRoot())
	verdict, rule, hard = a.decideCall(a.permCall(u.Name, u.Input, subject))
	return subject, verdict, rule, hard
}

// permCall is one call as the permission rules see it.
func (a *Agent) permCall(name string, input json.RawMessage, subject string) perm.Call {
	return perm.Call{Tool: name, Subject: subject, Input: input, Root: a.displayRoot()}
}

// mustPrompt reports whether the user still has to be asked: only for a
// mutating tool, and only when no approval shortcut covers it. A guarded
// input (mustAsk) overrides every shortcut.
func (a *Agent) mustPrompt(t tool.Tool, u *llm.Block, mustAsk bool, verdict perm.Verdict, hookAllowed bool) bool {
	if !t.Mutating() {
		return false
	}
	if mustAsk {
		return true
	}
	modes := a.Settings()
	autoEdit := (modes.AutoEdits || modes.AutoMode) && editTool(u.Name)
	autoSafe := modes.AutoMode && a.autoAllows(*u)
	return !modes.AutoApprove && !autoEdit && !autoSafe && !a.allowed[u.Name] && verdict != perm.Allowed && !hookAllowed
}

// ask puts the call to the user and records what came back. suggested is
// the rule AllowAlways would save, which applyDecision needs.
func (a *Agent) ask(ctx context.Context, u *llm.Block, call *ToolCall, subject string, mustAsk bool) (Decision, string, perm.Rule, error) {
	if a.Impact != nil && editTool(u.Name) && !a.Off["impact"] {
		call.Impact = a.Impact(ctx, *call)
	}
	// suggested is the rule AllowAlways would save, and its string
	// form is the shape the ledger tracks approvals and refusals
	// under (see perm.Suggest): computed once and reused for both.
	// A zero rule is a command no rule can capture safely: nothing is
	// offered, learned or recorded for it.
	suggested := perm.Suggest(u.Name, subject)
	shape := suggested.String()
	call.Rule = shape
	call.Learned = a.learnedNote(shape, mustAsk, call.Warning)
	// The ladder the user may move along before saving: each rung with
	// the ledger's note about that exact rule, so the "approved N×" note
	// always counts the rule shown beside it. Off["ladder"] keeps the
	// single rule Suggest proposes and nothing to walk — the prompt
	// looks as it did before rungs existed.
	var rungs []perm.Rule
	if a.Off["ladder"] {
		if !suggested.IsZero() {
			rungs = []perm.Rule{suggested}
			call.Rungs = []RuleRung{{Rule: shape, Learned: call.Learned}}
		}
		call.RuleIndex = 0
	} else {
		rungs = perm.SuggestLadder(u.Name, subject)
		call.RuleIndex = perm.DefaultRung(rungs, suggested)
		call.Rungs = nil
		for _, r := range rungs {
			call.Rungs = append(call.Rungs, RuleRung{Rule: r.String(), Learned: a.learnedNote(r.String(), mustAsk, call.Warning)})
		}
	}
	var (
		d      Decision
		reason string
		chosen string
		err    error
	)
	asked := time.Now()
	switch ap := a.Approver.(type) {
	case RuleApprover:
		d, reason, chosen, err = ap.ApproveWithRule(ctx, *call)
	case ReasonApprover:
		d, reason, err = ap.ApproveWithReason(ctx, *call)
	default:
		d, err = a.Approver.Approve(ctx, *call)
	}
	a.waited(ctx, time.Since(asked))
	if err != nil {
		return 0, "", perm.Rule{}, err
	}
	// Saving a wider or narrower rung than the default makes that rung
	// the shape this answer is about, so it is the one the ledger counts.
	if d == AllowAlways && chosen != "" {
		for _, r := range rungs {
			if r.String() == chosen {
				suggested, shape = r, chosen
				break
			}
		}
	}
	// Recorded before applyDecision, which reports a refusal: the ledger
	// has to see a refusal too, not only the allow variants.
	a.stats.approval(d == Deny)
	ledger := a.PermLedger()
	if shape != "" && (d != Deny || reason != CancelledReason) {
		ledger.Record(shape, d != Deny)
	}
	if shape != "" && call.Warning != "" && d != Deny {
		ledger.MarkWarned(shape)
	}
	return d, reason, suggested, nil
}

// learnedNote is the ledger's note for one rule shape, or "" when the
// shape is new, has ever been refused, or the prompt is unusual enough
// that a "you have allowed this before" note would be misleading.
func (a *Agent) learnedNote(shape string, mustAsk bool, warning string) string {
	if shape == "" || mustAsk || warning != "" {
		return ""
	}
	if t := a.PermLedger().Get(shape); t.Allowed >= perm.LearnAfter && t.Denied == 0 {
		return fmt.Sprintf("approved %d× before, never refused", t.Allowed)
	}
	return ""
}

// applyDecision carries out what the user chose: widening the allow for
// the session or saving a rule, or reporting the refusal the model sees.
// A guarded call (mustAsk) is never widened — the answer covers this call
// only.
func (a *Agent) applyDecision(d Decision, reason string, suggested perm.Rule, name string, mustAsk bool, emit func(Event)) (refusal string) {
	switch d {
	case AllowSession:
		if !mustAsk {
			a.allowSession(name)
		}
	case AllowAlways:
		if !mustAsk && !suggested.IsZero() {
			a.AddRule(perm.Allowed, suggested)
			if a.SaveRule != nil {
				if err := a.SaveRule(suggested); err != nil {
					emit(Event{Kind: EvNotice, Text: "could not save permission rule " + suggested.String() + ": " + err.Error()})
				} else {
					emit(Event{Kind: EvNotice, Text: "saved permission rule: " + suggested.String()})
				}
			}
		}
	case Allow:
	default:
		if reason == CancelledReason {
			return CancelledMessage
		}
		msg := DeniedMessage
		if reason = strings.TrimSpace(reason); reason != "" {
			msg += " The user said: " + reason
		}
		return msg
	}
	return ""
}

func (a *Agent) executeTool(ctx context.Context, u llm.Block, emit func(Event)) (string, []llm.Block, bool, error) {
	// The intent is the model's own one-line reason for the call. It is
	// taken off before anything else looks at the input, because the tools
	// decode with unknown fields disallowed.
	intent, stripped := tool.TakeIntent(u.Input)
	// accept_large_output is not declared on any schema — it is named only
	// in the refusal that needs it — so it must come off before a tool
	// that disallows unknown fields sees it.
	accepted := tookLargeOutput(stripped)
	u.Input = takeLargeOutput(stripped)
	call := ToolCall{ID: u.ID, Name: u.Name, Input: u.Input, Intent: intent}
	refuse := func(msg string, denied bool) (string, []llm.Block, bool, error) {
		a.refuseCall(&call, msg, denied, emit)
		return msg, nil, true, nil
	}
	t, ok := a.Tools.Get(u.Name)
	if !ok {
		if why, withheld := a.Tools.Withheld(u.Name); withheld {
			return refuse(fmt.Sprintf("tool %q is withheld by policy (%s). It is not available in this session; do not search for a workaround.", u.Name, why), false)
		}
		return refuse(fmt.Sprintf("Unknown tool %q. Available: %s", u.Name, a.toolNames()), false)
	}
	if reason, denied := a.toolGate(t, u.Input); denied {
		return refuse(reason, true)
	}
	hookNotes, deny, err := a.authorize(ctx, t, &u, &call, emit)
	if err != nil {
		return "", nil, true, err
	}
	if deny != "" {
		return refuse(deny, true)
	}
	emit(Event{Kind: EvToolRun, Call: &call})
	a.logEvent(session.Event{Type: session.TypeToolCall, CallID: u.ID, Name: u.Name, Arguments: string(u.Input), Intent: intent})
	// A rule scoped to these paths joins the conversation before the
	// next model call, once for the session.
	a.applyPathRules(t, u.Input, emit)
	if t.Mutating() {
		a.snapshotBefore(ctx, t, u.Input)
	}
	start := time.Now()
	var pend []pending
	var tamperedBefore string
	if t.Mutating() {
		// A locked file changed outside any tool call is put back before
		// this call runs, so the note after it never blames this call.
		tamperedBefore = a.acceptRestoreTampered("", emit)
		pend = a.before(t, u.Input)
	}
	// A bash call that looks like `git commit` gets HEAD read before it
	// runs, so a move can be detected after: see checkCommitAttach. The
	// read only happens for a command that already matched, so an
	// ordinary bash call costs nothing extra.
	checkingCommit := u.Name == "bash" && looksLikeGitCommit(shell.CommandOf(string(u.Input)))
	var commitPreHead string
	if checkingCommit {
		commitPreHead = a.headForCommitCheck(ctx)
	}
	out, images, err := runSafely(ctx, t, u.Input)
	dur := time.Since(start)
	if err == nil && len(pend) > 0 {
		a.after(u, intent, pend)
	}
	if ctx.Err() != nil && err != nil {
		// The call was interrupted before it produced a result.
		return "", nil, true, ctx.Err()
	}
	// When the tool completed (or ignored the context) but the turn was
	// cancelled right after, its effects stand: reporting "cancelled"
	// here told the model the call never happened — an invitation to run
	// it again on the next turn. Fall through with the real output; the
	// loop ends the turn on the cancelled context one step later.
	isErr := false
	if err != nil {
		out = "Error: " + err.Error()
		isErr = true
	} else if t.Mutating() {
		a.mutated = true
		a.mutations++
		if editTool(u.Name) {
			a.locks().TrackEdit(t, u.Input)
		}
		// The content guarantee behind the lock: whatever just ran, and
		// whatever the pre-execution checks did or did not recognise
		// about it, a locked file that moved is restored right now, not
		// merely reported the next time the gate happens to run.
		for _, note := range []string{tamperedBefore, a.acceptRestoreTampered(u.Name, emit)} {
			if note == "" {
				continue
			}
			if out == "" {
				out = note
			} else {
				out += "\n\n" + note
			}
		}
	}
	if checkingCommit && !isErr {
		a.checkCommitAttach(ctx, commitPreHead, emit)
	}
	if out == "" {
		out = "(no output)"
	}
	if hooks := a.HookRunner(); hooks.Has(hook.PostTool) {
		res := hooks.Run(ctx, hook.Input{Event: hook.PostTool, Cwd: hooks.Cwd, Tool: u.Name, Input: u.Input, Output: out, IsError: isErr})
		hookNotes = append(hookNotes, res.Notes...)
	}
	if a.Diagnose != nil && !isErr && editTool(u.Name) {
		if note := a.Diagnose(ctx, perm.Subject(u.Name, u.Input)); note != "" {
			out += "\n" + note
		}
	}
	// A result that would take an unreasonable share of what is left is
	// withheld with its price rather than truncated: truncation bills the
	// caller for a prefix that is rarely the answer.
	if !isErr && !a.Off["oversized"] {
		if msg := oversized(u.Name, subjectOf(u.Name, u.Input), out, a.EstimateTokens(), a.Settings().ContextWindow, accepted); msg != "" {
			out, isErr = msg, true
		}
	}
	out = a.stash(ctx, u.ID, u.Name, subjectOf(u.Name, u.Input), out)
	for _, n := range hookNotes {
		out += "\n[hook: " + n + "]"
	}
	if a.Off["coach"] {
		// still record edits so the syntax check runs when only coaching is off
	} else if t.Mutating() { // edit coaching only touches serial (mutating) calls, so no lock is needed
		out += a.coach.after(ctx, a.Tools, t, u.Name, u.Input, isErr)
	} else if u.Name == "read_file" {
		a.coachMu.Lock()
		a.coach.after(ctx, a.Tools, t, u.Name, u.Input, isErr)
		a.coachMu.Unlock()
	}
	if !a.Off["loops"] {
		a.loopMu.Lock()
		note, stop := a.loops.record(u.Name, u.Input, isErr, t.Mutating())
		if stop {
			a.stuck = true
		}
		a.loopMu.Unlock()
		out += note
	}
	if t.Mutating() { // serial: safe to read the counters
		out += a.critique(emit)
	}
	emit(Event{Kind: EvToolResult, Call: &call, Output: out, IsError: isErr, Duration: dur})
	a.logEvent(session.Event{Type: session.TypeToolResult, CallID: u.ID, Name: u.Name, Arguments: string(u.Input), Output: out, IsError: isErr, DurationMs: dur.Milliseconds()})
	if isErr {
		images = nil
	}
	return out, images, isErr, nil
}

func runSafely(ctx context.Context, t tool.Tool, input json.RawMessage) (out string, images []llm.Block, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("tool %s panicked: %v\n%s", t.Name(), r, debug.Stack())
			images = nil
		}
	}()
	if rich, ok := t.(tool.Rich); ok {
		res, rerr := rich.RunRich(ctx, input)
		return res.Text, res.Images, rerr
	}
	out, err = t.Run(ctx, input)
	return out, nil, err
}

func (a *Agent) toolNames() string {
	defs := a.Tools.Defs()
	s := ""
	for i, d := range defs {
		if i > 0 {
			s += ", "
		}
		s += d.Name
	}
	if n := len(a.Tools.Deferred()); n > 0 {
		s += fmt.Sprintf(" (and %d deferred tools; find them with tool_search)", n)
	}
	return s
}

// autoAllows is the AUTO-mode fast path for a mutating tool that is not
// a file edit: known-safe read-only shell. File edits are handled by
// autoEdit. Guard/mustAsk still wins above this.
func (a *Agent) autoAllows(u llm.Block) bool {
	if u.Name != "bash" {
		return false
	}
	var in struct {
		Command string `json:"command"`
	}
	if json.Unmarshal(u.Input, &in) != nil {
		return false
	}
	return shell.SafeRead(in.Command)
}

// editTool reports the tools AutoEdits covers: the ones that change files
// through the harness, never the shell.
func editTool(name string) bool { return name == "write_file" || name == "edit_file" }

// LastTurnMutated reports whether the most recent turn ran a mutating tool
// successfully, i.e. whether it may have decided something worth
// remembering.
func (a *Agent) LastTurnMutated() bool { return a.mutated }

// runNested runs one tool call made from inside a script. It is
// executeTool without the parts that only make sense for a call the model
// made itself: the output is not stashed or truncated, because the script
// is what reads it, and there is no edit coaching or loop detection,
// because the script's loop is its own business. Everything that protects
// the user — the hooks, the permission rules, the approval prompt, the
// snapshot, the event log — is the same.
//
// The third return, denied, says whether a failure was authorize itself
// refusing the call (a permission rule, a missing or negative approver, a
// hook block, read-only mode) rather than the tool running and failing on
// its own merits. RunRecipe uses it to tell a permission denial apart from
// an ordinary step failure, since only the latter is worth handing to the
// model — a call the harness itself refused will be refused again with
// the same words.
func (a *Agent) runNested(ctx context.Context, id, name string, input json.RawMessage) (string, bool, bool) {
	emit := a.emitter()
	// A script sees the same tool schemas the model does, so it passes
	// intent= as a keyword argument. It has to come off here too, or every
	// such call fails on an unknown field.
	intent, stripped := tool.TakeIntent(input)
	input = stripped
	call := ToolCall{ID: id, Name: name, Input: input, Intent: intent}
	t, ok := a.Tools.Get(name)
	if !ok {
		return fmt.Sprintf("Unknown tool %q. Available: %s", name, a.toolNames()), true, false
	}
	// The same mode gate as executeTool: a script is a way to save round
	// trips, not a way around read-only, plan or accept mode.
	if reason, denied := a.toolGate(t, input); denied {
		a.refuseCall(&call, reason, true, emit)
		return reason, true, true
	}
	u := llm.Block{ID: id, Name: name, Input: input}
	_, deny, err := a.authorize(ctx, t, &u, &call, emit)
	if err != nil {
		return "Error: " + err.Error(), true, false
	}
	if deny != "" {
		a.refuseCall(&call, deny, true, emit)
		return deny, true, true
	}
	emit(Event{Kind: EvToolRun, Call: &call})
	a.logEvent(session.Event{Type: session.TypeToolCall, CallID: id, Name: name, Arguments: string(u.Input), Intent: intent})
	var pend []pending
	var tamperedBefore string
	if t.Mutating() {
		tamperedBefore = a.acceptRestoreTampered("", emit)
		a.snapshotBefore(ctx, t, u.Input)
		pend = a.before(t, u.Input)
	}
	start := time.Now()
	out, _, runErr := runSafely(ctx, t, u.Input)
	dur := time.Since(start)
	isErr := runErr != nil
	if isErr {
		out = "Error: " + runErr.Error()
	} else if t.Mutating() {
		a.after(u, intent, pend)
		a.mutated = true
		a.mutations++
		// Same content guarantee as executeTool: a locked file this
		// nested call moved, by any means, is restored right now.
		for _, note := range []string{tamperedBefore, a.acceptRestoreTampered(name, emit)} {
			if note == "" {
				continue
			}
			if out == "" {
				out = note
			} else {
				out += "\n\n" + note
			}
		}
	}
	emit(Event{Kind: EvToolResult, Call: &call, Output: out, IsError: isErr, Duration: dur})
	a.logEvent(session.Event{Type: session.TypeToolResult, CallID: id, Name: name, Arguments: string(u.Input), Output: out, IsError: isErr, DurationMs: dur.Milliseconds()})
	// The script gets the whole output; the archive gets a copy, so what a
	// script read is still findable afterwards through expand_output even
	// though it never entered the conversation.
	if !isErr {
		a.stash(ctx, id, name, subjectOf(name, u.Input), out)
	}
	return out, isErr, false
}
