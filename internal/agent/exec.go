package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/xmonader/janetcode/internal/egress"
	"github.com/xmonader/janetcode/internal/hook"
	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/perm"
	"github.com/xmonader/janetcode/internal/progress"
	"github.com/xmonader/janetcode/internal/session"
	"github.com/xmonader/janetcode/internal/shell"
	"github.com/xmonader/janetcode/internal/textfmt"
	"github.com/xmonader/janetcode/internal/tool"
)

// MaxParallelTools bounds concurrent read-only tool execution.
const MaxParallelTools = 8

// cancelledResult is the result a call the user's cancel interrupted or
// never reached gets, in the history and in its result event alike.
const cancelledResult = "Cancelled by user before the tool finished."

// dedupedResult is the answer a repeated read-only call in one batch
// gets: the identical call above it already produced this output, so
// running it again would bill the model for the same bytes twice.
const dedupedResult = "[deduped: identical call above returned this output]"

// executeAll runs the turn's tool calls: the read-only calls that come
// before the first call that cannot run in parallel run concurrently, and
// everything from that call on runs one at a time in the model's order. A
// read-only call identical to one already answered in the same batch is
// not run again (see dedupedResult). It always
// returns one result per call so the history stays valid, even when the
// batch stops part-way — cancelled, or with an approver that could not
// answer — and the error that stopped it is returned rather than dropped.
func (a *Agent) executeAll(ctx context.Context, uses []llm.Block, emit func(Event)) ([]llm.Block, []llm.Block, error) {
	a.setEmitter(emit)
	defer a.noteSubagentMerges()
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
		return ok && !tool.MutatingCall(t, u.Input)
	}

	// In-step dedup: a model sometimes issues the same read-only call
	// several times in one batch (one measured session burned 1.08M
	// tokens on repeated reads in a single step). A non-mutating call
	// identical to one already answered in the batch — same tool,
	// semantically same input — is not run again; its result points at
	// the first answer. A mutating or unknown call is a barrier: it may
	// have moved the world, so the keys seen before it are dropped and
	// reads after it dedup only among themselves.
	dedup := map[string]bool{}
	dedupKey := func(u llm.Block) string { return u.Name + "\x00" + canonical(u.Input) }
	// The synthetic result surfaces the way answerUnrun's do: a result
	// event and a log record, so a front-end and a resumed session see
	// the same answer the history holds.
	// isErr carries the first call's error flag: a duplicate of a read
	// that failed failed too, and must not read as a success.
	answerDeduped := func(i int, u llm.Block, isErr bool) {
		results[i] = llm.ToolResult(replyID(u), dedupedResult, isErr)
		filled[i] = true
		call := ToolCall{ID: u.ID, Name: u.Name, Input: u.Input}
		emit(Event{Kind: EvToolResult, Call: &call, Output: dedupedResult, IsError: isErr})
		a.logEvent(session.Event{Type: session.TypeToolResult, CallID: u.ID, Name: u.Name, Arguments: string(u.Input), Output: dedupedResult, IsError: isErr})
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

	// Only the leading read-only calls run in parallel. A read the model
	// placed after an edit is reading the edit: running every read first
	// handed [edit_file a, read_file a] the content from before the edit,
	// and the model then reasoned from a file that no longer existed. So
	// the first call that is not a known read-only tool (a mutating one,
	// or one that will be refused) is a barrier, and from it on the batch
	// runs in the order the model wrote it.
	lead := 0
	for lead < len(uses) && parallel(uses[lead]) {
		lead++
	}
	// Duplicates in the lead are marked before any goroutine starts, so
	// the once-per-key decision needs no lock: the first of a key runs,
	// the rest are answered from it once it has.
	dup := make([]bool, lead)
	first := make([]int, lead)
	firstOf := map[string]int{}
	for i, u := range uses[:lead] {
		k := dedupKey(u)
		if j, ok := firstOf[k]; ok {
			dup[i], first[i] = true, j
		} else {
			firstOf[k] = i
			dedup[k] = true
		}
	}
	var wg sync.WaitGroup
	sem := make(chan struct{}, MaxParallelTools)
	for i, u := range uses[:lead] {
		if dup[i] {
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
				if out != "" {
					results[i] = llm.ToolResult(replyID(u), out, true)
					filled[i] = true
				}
				fail(err)
				return
			}
			results[i] = llm.ToolResult(replyID(u), out, isErr)
			filled[i] = true
			addImages(imgs)
		}(i, u)
	}
	wg.Wait()
	for i, u := range uses[:lead] {
		if dup[i] && filled[first[i]] {
			answerDeduped(i, u, results[first[i]].IsError)
		}
	}
	// A read that failed is not "this output" for a later duplicate,
	// which is what the serial path below already holds to.
	for k, j := range firstOf {
		if !filled[j] || results[j].IsError {
			delete(dedup, k)
		}
	}

	// Every call is answered whatever happened: nothing may come between
	// a tool_use and its tool_result, so a call that never ran gets a
	// result saying so.
	stop := func(err error) ([]llm.Block, []llm.Block, error) {
		msg := cancelledResult
		if !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
			msg = "Not run: the turn stopped before this call could be made (" + err.Error() + ")."
		}
		for i, u := range uses {
			if !filled[i] {
				results[i] = llm.ToolResult(replyID(u), msg, true)
				call := ToolCall{ID: u.ID, Name: u.Name, Input: u.Input}
				emit(Event{Kind: EvToolResult, Call: &call, Output: msg, IsError: true})
				a.logEvent(session.Event{Type: session.TypeToolResult, CallID: u.ID, Name: u.Name, Arguments: string(u.Input), Output: msg, IsError: true})
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
		ro := parallel(u)
		if !ro {
			// The barrier: a mutating or unknown call may have moved
			// the world, so a read after it cannot reuse what a read
			// before it saw. This also drops the lead's keys.
			dedup = map[string]bool{}
		} else if dedup[dedupKey(u)] {
			answerDeduped(i, u, false)
			continue
		}
		out, imgs, isErr, err := a.execute(ctx, u, emit)
		if err != nil {
			if out != "" {
				results[i] = llm.ToolResult(replyID(u), out, true)
				filled[i] = true
			}
			return stop(err)
		}
		results[i] = llm.ToolResult(replyID(u), out, isErr)
		filled[i] = true
		addImages(imgs)
		if ro && !isErr {
			// Only a successful read is worth pointing a duplicate at:
			// a refusal or a failure is not "this output".
			dedup[dedupKey(u)] = true
		}
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
	var thenRun string
	if editTool(u.Name) && !a.Off["thenrun"] {
		thenRun, u.Input = tool.TakeThenRun(u.Input)
	}
	out, images, isErr, err := a.executeTool(ctx, u, emit)
	a.stats.tool(u.Name, time.Since(start)-wait.total(), isErr || err != nil)
	if thenRun != "" && err == nil {
		out, err = a.runThenRun(ctx, u, thenRun, out, isErr, emit)
	}
	return out, images, isErr, err
}

// runThenRun runs an edit's then_run command (see tool.ThenRunKey) as a
// bash call of its own, under a derived id, so it passes every gate a bash
// call the model made directly would: the hooks, the permission rules and
// the prompt, the sandbox, the shell cache. Its result is appended to the
// edit's. An edit that failed runs nothing: the command was meant to check
// a change that did not happen.
func (a *Agent) runThenRun(ctx context.Context, u llm.Block, cmd, out string, editFailed bool, emit func(Event)) (string, error) {
	if editFailed {
		return out + "\n\n[then_run not run: the edit failed]", nil
	}
	if _, ok := a.Tools.Get("bash"); !ok {
		return out + "\n\n[then_run not run: bash is not available in this session]", nil
	}
	input, _ := json.Marshal(map[string]string{"command": cmd})
	run := llm.Block{Type: llm.BlockToolUse, ID: u.ID + "~then_run", Name: "bash", Input: input}
	start := time.Now()
	ctx, wait := meterWaits(ctx)
	res, _, isErr, err := a.executeTool(ctx, run, emit)
	a.stats.tool(run.Name, time.Since(start)-wait.total(), isErr || err != nil)
	if err != nil {
		return out, err
	}
	return out + "\n\n[then_run] $ " + cmd + "\n" + res, nil
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
func (a *Agent) authorize(ctx context.Context, t tool.Tool, u *llm.Block, call *ToolCall, emit func(Event)) (notes []string, refusal string, err error) {
	// basis is what settled the call, for the audit record written on
	// the way out (see logPermission).
	var basis string
	defer func() { a.logPermission(t, u, basis, refusal, err) }()
	// A planted honeytoken in the arguments is never a call to put to
	// anyone: nothing legitimate holds one. The run stops here, before a
	// hook could see or rewrite it.
	if err := a.canaryCall(*u, emit); err != nil {
		basis = "honeytoken"
		return nil, "", err
	}
	wasMutating := tool.MutatingCall(t, u.Input)
	hookNotes, hookAllowed, hookMustAsk, hookRefusal := a.preToolHook(ctx, u, call, emit)
	if hookRefusal != "" {
		basis = "pre-tool hook"
		return nil, hookRefusal, nil
	}
	// Scheduling and in-batch dedup used the original mutation class.
	// A hook may refine arguments, but cannot promote a parallel read
	// into a write. The model must submit that mutation as a new call.
	if !wasMutating && tool.MutatingCall(t, u.Input) {
		basis = "pre-tool hook"
		return nil, "Not run: a pre_tool hook changed a read-only call into a mutating call. Submit the mutation as a separate tool call.", nil
	}
	if reason, denied := a.toolGate(t, u.Input); denied {
		basis = "session mode"
		return nil, reason, nil
	}
	mustAsk := a.describeCall(ctx, t, u, call)
	// Allow all (--yolo, /yolo) waives the local guards: the tool's own
	// guard (a command reaching outside the workspace, a destructive
	// one-liner) and the protected paths below. Their warning still shows.
	// The egress and taint gates, a hook's ask and an ask rule are not
	// local judgements, and they still put the call to the user.
	yolo := a.yoloChosen()
	if yolo {
		mustAsk = false
	}
	if hookMustAsk {
		// describeCall writes call.Warning from the tool's own Warner, so
		// this goes on afterwards or it would be overwritten.
		mustAsk = true
		call.Source, call.Why, call.Stop = "hook", "a pre_tool hook asked, or a repository hook rewrote this call's input", stopHook
		call.Warning = strings.TrimPrefix(call.Warning+"; a hook from this repository rewrote this call's input", "; ")
	}
	// A write to a protected path always asks, under /edits, /auto and an
	// allow rule alike (Allow all waives it, above), and the prompt says which path it is
	// asking about. It rides on the warning line the prompt already has,
	// so both front-ends show it without knowing about any of this.
	// The egress policy: a blocked host is refused here, and a host it
	// asks about is asked about whatever the mode — for a read-only call
	// too (web search, a read-only MCP tool), since what it sends is
	// the point, not what it changes.
	egressAsk := false
	switch refusal, why := a.egressGate(ctx, t, *u); {
	case refusal != "":
		basis = "egress policy"
		return nil, refusal, nil
	case why != "":
		mustAsk, egressAsk = true, true
		setAsk(call, "egress", "egress policy: "+why, stopEgress)
		call.Warning = strings.TrimPrefix(call.Warning+"; "+why, "; ")
	}
	// A tainted turn asks about the calls an injection is after — the
	// network, CI and credential files, janetcode's own settings —
	// whatever the mode, and for a read-only call too (see taint.go). The
	// warning says where the argument came from when it can.
	taintWhy, taintHosts := a.taintGate(t, *u)
	taintAsk := taintWhy != ""
	if taintAsk {
		mustAsk = true
		setAsk(call, "taint", "this session read untrusted content (taint), and "+taintWhy, stopTaint)
		call.Warning = strings.TrimPrefix(call.Warning+"; "+taintWhy, "; ")
	}
	if tool.MutatingCall(t, u.Input) && !a.Off["protected"] {
		if why := a.Protected.Reason(perm.Call{Tool: u.Name, Input: u.Input, Root: a.displayRoot()}); why != "" && !yolo {
			mustAsk = true
			setAsk(call, "protected", "a "+why, stopProtected)
			if !strings.Contains(call.Warning, why) {
				call.Warning = strings.TrimPrefix(call.Warning+"; "+why, "; ")
			}
		}
	}
	// A locked accept-test file is refused like a denied permission rule:
	// before anything asks the user, and with no way for AutoApprove or a
	// saved rule to skip past it (see accept.Mode.Denial).
	if tool.MutatingCall(t, u.Input) {
		if reason := a.locks().Denial(t, u.Input); reason != "" {
			basis = "accept lock"
			return nil, reason, nil
		}
		// toolGate judged the phase rule on the input the model sent; a
		// hook's rewrite is what runs, so it is judged again here, or a
		// test-file write rewritten into an implementation write would
		// pass a gate its own input never faced.
		if reason := a.locks().WritingDenial(t, u.Input); reason != "" {
			basis = "accept lock"
			return nil, reason, nil
		}
	}
	if reason := a.testFileDenial(*u); reason != "" {
		basis = "test guard"
		return nil, reason, nil
	}
	subject, verdict, rule, hard := a.ruleDecision(u)
	// The hard deny tier: a command that destroys the machine is refused
	// before the rules are consulted at all, so no allow rule and no mode
	// can put it in front of the user as something to approve.
	if hard != "" {
		basis = "hard deny"
		return nil, fmt.Sprintf("Refused: this command %s. janetcode never runs it, not even with auto-approve or an allow rule; do not retry it in another form.", hard), nil
	}
	if verdict == perm.Denied {
		basis = "rule " + rule.String()
		return nil, fmt.Sprintf("Denied by permission rule %s%s. Do not retry this call; choose another approach or ask the user to change the rule.", rule, a.outranked(u, subject)), nil
	}
	// An ask rule marks the call as sensitive: it is put to the user
	// whatever the mode, a session allowance or an allow rule would do,
	// and even when the tool only reads. The answer covers this call
	// only (mustAsk), and the prompt says which rule asked and which
	// allow rule it outranked.
	sensitive := verdict == perm.Ask && !rule.IsZero()
	if sensitive {
		mustAsk = true
		why := "asks because of rule " + rule.String() + a.outranked(u, subject)
		setAsk(call, "rule", "your ask rule "+rule.String(), stopRule)
		call.Warning = strings.TrimPrefix(call.Warning+"; "+why, "; ")
	}
	switch {
	case verdict == perm.Allowed:
		call.Warning = strings.TrimPrefix(call.Warning+"; allowed by rule "+rule.String(), "; ")
	case hookAllowed:
		call.Warning = strings.TrimPrefix(call.Warning+"; allowed by a hook", "; ")
	}
	// A pre-tool hook that asked (or a repository hook that rewrote the
	// input) is put to the user for a read too: mustPrompt only ever
	// prompts for a mutating call, and "ask" is the hook saying this one
	// call needs a person whatever it is.
	if !sensitive && !egressAsk && !taintAsk && !hookMustAsk && !a.mustPrompt(t, u, mustAsk, verdict, hookAllowed) {
		basis = a.shortcutBasis(t, u, verdict, rule, hookAllowed)
		return hookNotes, "", nil
	}
	switch allowed, hr := a.permissionHook(ctx, u, call, mustAsk, emit); {
	case hr != "":
		basis = "permission hook"
		return nil, hr, nil
	case allowed:
		basis = "permission hook"
		return hookNotes, "", nil
	}
	if a.Approver == nil {
		basis = "no approver"
		// Say what made it need approval: a headless run refused
		// `sed -n ... $(go env GOMODCACHE)/...` as a "mutating tool",
		// and the model could not tell the read apart from a write.
		msg := "Permission denied: no approver configured for mutating tools."
		if call.Warning != "" {
			msg += " It needed approval because: " + call.Warning + ". Stay inside the workspace, or use read_file for a read."
		}
		return nil, msg, nil
	}
	if r := a.awayRefusal(u.Name, subject, call.Warning); r != "" {
		basis = "away"
		return nil, r, nil
	}
	a.notifyHook("permission_prompt", "janetcode needs your permission to use "+u.Name)
	d, reason, suggested, askErr := a.ask(ctx, u, call, subject, mustAsk)
	if askErr != nil {
		basis = "the user (no answer)"
		return nil, "", askErr
	}
	basis = a.answerBasis(d)
	if r := a.applyDecision(d, reason, suggested, u.Name, mustAsk, emit); r != "" {
		return nil, r, nil
	}
	a.taintApprove(taintHosts)
	return hookNotes, "", nil
}

// preToolHook runs the pre-tool hook, which may refuse the call outright,
// allow it past the prompt, or rewrite the input everything after this
// point — the preview, the approval and the call itself — works on.
func (a *Agent) preToolHook(ctx context.Context, u *llm.Block, call *ToolCall, emit func(Event)) (notes []string, allowed, mustAsk bool, refusal string) {
	if !a.HookRunner().Has(hook.PreTool) {
		return nil, false, false, ""
	}
	in := a.hookInput(hook.PreTool)
	in.Tool, in.Input = u.Name, u.Input
	res := a.runHook(ctx, in, emit)
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
	if res.Ask {
		// permissionDecision "ask": the user confirms this call whatever
		// the mode would otherwise do.
		mustAsk = true
	}
	return append(notes, res.Notes...), res.Allowed && !res.Ask, mustAsk, ""
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
	if g, ok := t.(tool.Guard); ok && tool.MutatingCall(t, u.Input) {
		if ask, why := g.Guard(u.Input); ask {
			mustAsk = true
			setAsk(call, "tool", toolGuardWhy(why), stopTool)
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

// outranked is the note a refusal or a forced prompt adds when an allow
// rule would have covered the call: " (it outranks allow rule X)", so a
// conflict between rules says which one won and why.
func (a *Agent) outranked(u *llm.Block, subject string) string {
	rs := a.RulesSnapshot()
	if r, ok := rs.AllowedBy(a.permCall(u.Name, u.Input, subject)); ok {
		return " (it outranks allow rule " + r.String() + ")"
	}
	return ""
}

// permCall is one call as the permission rules see it.
func (a *Agent) permCall(name string, input json.RawMessage, subject string) perm.Call {
	return perm.Call{Tool: name, Subject: subject, Input: input, Root: a.displayRoot()}
}

// mustPrompt reports whether the user still has to be asked: only for a
// mutating tool, and only when no approval shortcut covers it. A guarded
// input (mustAsk) overrides every shortcut.
func (a *Agent) mustPrompt(t tool.Tool, u *llm.Block, mustAsk bool, verdict perm.Verdict, hookAllowed bool) bool {
	if !tool.MutatingCall(t, u.Input) {
		return false
	}
	if mustAsk {
		return true
	}
	modes := a.Settings()
	autoEdit := (modes.AutoEdits || modes.AutoMode) && editTool(u.Name)
	autoSafe := modes.AutoMode && a.autoAllows(*u)
	return !modes.AutoApprove && !autoEdit && !autoSafe && !a.sessionAllows(u.Name) && verdict != perm.Allowed && !hookAllowed
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
	a.modeAsk(call)
	c := *call
	a.lastAsk.Store(&c)
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
		if a.Policy != nil && a.Policy.ManagedRulesOnly {
			// Only the managed policy's allow rules apply, so "always"
			// is this call only, and the user is told why.
			emit(Event{Kind: EvNotice, Text: "allowed once: " + a.Policy.Refusal("a saved allow rule").Error()})
			break
		}
		if !mustAsk && !suggested.IsZero() {
			a.AddRule(perm.Allowed, suggested)
			if a.SaveRule != nil {
				if err := a.SaveRule(suggested); err != nil {
					emit(Event{Kind: EvNotice, Text: "could not save permission rule " + suggested.String() + ": " + err.Error()})
				} else {
					emit(Event{Kind: EvNotice, Text: "won't ask again for " + suggested.String() + " · saved as a permission rule (/permissions lists them)"})
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
	res, isErr, _, err := a.executeToolResult(ctx, u, emit, false)
	return res.Text, res.Images, isErr, err
}

// executeToolResult owns the policy checks and execution lifecycle for both
// direct and nested calls. Nested calls retain full structured output; only
// conversation presentation and model-facing coaching differ.
func (a *Agent) executeToolResult(ctx context.Context, u llm.Block, emit func(Event), nested bool) (tool.Result, bool, bool, error) {
	if err := a.persistenceErr(); err != nil {
		return tool.Result{}, true, true, err
	}
	if err := ctx.Err(); err != nil {
		return tool.Result{}, true, false, err
	}
	// The intent is the model's own one-line reason for the call. It is
	// taken off before anything else looks at the input, because the tools
	// decode with unknown fields disallowed.
	intent, stripped := tool.TakeIntent(u.Input)
	// accept_large_output is not declared on any schema — it is named only
	// in the refusal that needs it — so it must come off before a tool
	// that disallows unknown fields sees it.
	accepted := tookLargeOutput(stripped)
	u.Input = takeLargeOutput(stripped)
	if !a.Off["pathrepair"] {
		u.Input = tool.RepairPaths(ctx, a.Workspace, a.RootDir(), u.Input)
	}
	call := ToolCall{ID: u.ID, Name: u.Name, Input: u.Input, Intent: intent}
	refuse := func(msg string, denied bool) (tool.Result, bool, bool, error) {
		a.refuseCall(&call, msg, denied, emit)
		return tool.Result{Text: msg}, true, denied, nil
	}
	if raw, bad := llm.UnparsedArguments(u.Input); bad {
		return refuse(fmt.Sprintf("%s was not run: its arguments are not valid JSON (%.120q). Resend the call with the arguments as one JSON object.", u.Name, raw), false)
	}
	t, ok := a.Tools.Get(u.Name)
	if !ok {
		if why, withheld := a.Tools.Withheld(u.Name); withheld {
			return refuse(fmt.Sprintf("tool %q is withheld by policy (%s). It is not available in this session; do not search for a workaround.", u.Name, why), false)
		}
		return refuse(a.unknownTool(u.Name), false)
	}
	if reason, denied := a.toolGate(t, u.Input); denied {
		return refuse(reason, true)
	}
	if out, handled := a.dryIntercept(ctx, t, &u, &call, intent, emit); handled {
		return tool.Result{Text: out}, false, false, nil
	}
	hookNotes, deny, err := a.authorize(ctx, t, &u, &call, emit)
	if err != nil {
		return tool.Result{}, true, false, err
	}
	if deny != "" {
		return refuse(deny, true)
	}
	emit(Event{Kind: EvToolRun, Call: &call})
	a.logEvent(session.Event{Type: session.TypeToolCall, CallID: u.ID, Name: u.Name, Arguments: string(u.Input), Intent: intent})
	// A rule scoped to these paths joins the conversation before the
	// next model call, once for the session.
	a.applyPathRules(t, u.Input, emit)
	// A dry run lets through only calls that read (see dryIntercept):
	// they need no checkpoint and do not make the turn a mutating one.
	var late *lateCall
	if tool.MutatingCall(t, u.Input) && !a.DryRunOn() {
		late = a.snapshotBefore(ctx, t, u.Input, emit)
	}
	start := time.Now()
	var pend []pending
	var tamperedBefore string
	var shellBefore bashAuditState
	var admitSnapBefore *admitSnap
	if tool.MutatingCall(t, u.Input) && !a.DryRunOn() {
		// A locked file changed outside any tool call is put back before
		// this call runs, so the note after it never blames this call.
		tamperedBefore = a.acceptRestoreTampered("", emit)
		pend = a.before(t, u.Input)
		shellBefore = a.bashAuditBefore(ctx, u)
		admitSnapBefore = a.admitBefore(ctx, u)
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
	manifests := a.manifestsBefore(t, u)
	squats := a.squatBefore(t, u)
	if err := a.persistenceErr(); err != nil {
		return tool.Result{}, true, false, err
	}
	res, err := a.runSafelyResult(progress.With(ctx, func(line string) { emit(Event{Kind: EvPhase, Text: line}) }), t, u.Input)
	dur := time.Since(start)
	out := res.Text
	isErr := err != nil
	if isErr {
		out = "Error: " + err.Error()
		if ctx.Err() != nil {
			out = cancelledResult
		}
	}
	a.bashAuditAfter(ctx, u, shellBefore)
	out += a.admitAfter(ctx, admitSnapBefore)
	// A failure, panic or cancellation does not imply that nothing changed.
	// Finalize all attempted mutations before any error or canary return.
	if tool.MutatingCall(t, u.Input) && !a.DryRunOn() {
		a.mutated = true
		a.mutations++
		if isErr {
			out += "\nThe tool may have made partial changes before failing; inspect the current state before retrying."
		}
		for _, note := range []string{tamperedBefore, a.acceptRestoreTampered(u.Name, emit)} {
			if note != "" {
				out += "\n\n" + note
			}
		}
		// Capture the actual final files, including partial writes and any
		// lock restoration, rather than inferring effects from the error.
		changed := a.after(u, intent, pend)
		if editTool(u.Name) && (!isErr || changed) {
			a.locks().TrackEdit(t, u.Input)
		}
		out += a.scopeAfter(u, intent, pend)
	}
	late.end(ctx, t, u)
	if hit := a.proxyCanaryHit(emit); hit != nil {
		res.Text, res.Images, res.Data = "Error: "+hit.Error(), nil, nil
		emit(Event{Kind: EvToolResult, Call: &call, Output: res.Text, IsError: true, Duration: dur})
		a.logEvent(session.Event{Type: session.TypeToolResult, CallID: u.ID, Name: u.Name, Arguments: string(u.Input), Output: res.Text, IsError: true, DurationMs: dur.Milliseconds()})
		return res, true, false, hit
	}
	if ctx.Err() != nil && isErr {
		res.Text, res.Images, res.Data = out, nil, nil
		emit(Event{Kind: EvToolResult, Call: &call, Output: out, IsError: true, Duration: dur})
		a.logEvent(session.Event{Type: session.TypeToolResult, CallID: u.ID, Name: u.Name, Arguments: string(u.Input), Output: out, IsError: true, DurationMs: dur.Milliseconds()})
		return res, true, false, ctx.Err()
	}
	out += a.checkDeps(ctx, manifests, emit)
	out += a.squatCheck(squats, emit)
	if !isErr {
		out += a.dryShellNote(u.Name)
	}
	if checkingCommit && !isErr {
		a.checkCommitAttach(ctx, commitPreHead, emit)
	}
	if out == "" && !nested {
		out = "(no output)"
	}
	for _, ev := range []string{hook.PostTool, hook.PostToolFailure} {
		if ev == hook.PostToolFailure && !isErr {
			continue
		}
		in := a.hookInput(ev)
		in.Tool, in.Input, in.Output, in.IsError = u.Name, u.Input, out, isErr
		if isErr {
			in.Error = out
		}
		hookNotes = append(hookNotes, a.runHook(ctx, in, emit).Notes...)
	}
	if a.ShellChanged != nil && u.Name == "bash" {
		go a.ShellChanged(context.WithoutCancel(ctx))
	}
	if a.Diagnose != nil && !isErr && editTool(u.Name) {
		path := perm.Subject(u.Name, u.Input)
		if a.Off["asyncdiag"] {
			note := a.Diagnose(ctx, path)
			a.postDiag.mu.Lock()
			note = a.postDiag.unavailableOnce(note)
			a.postDiag.mu.Unlock()
			if note != "" {
				out += "\n" + note
			}
		} else {
			a.startDiagnose(ctx, path)
		}
	}
	// A result that would take an unreasonable share of what is left is
	// withheld with its price rather than truncated: truncation bills the
	// caller for a prefix that is rarely the answer.
	if !nested && !isErr && !a.Off["oversized"] {
		if msg := oversized(u.Name, subjectOf(u.Name, u.Input), out, a.EstimateTokens(), a.Settings().ContextWindow, accepted); msg != "" {
			out, isErr = msg, true
		}
	}
	// Read off the whole output before stash may archive it: the failing
	// test lines are what localization starts from.
	located := a.localizeBash(ctx, u.Name, u.Input, out, isErr, emit)
	blamed := a.readBlame(ctx, u.Name, u.Input, out, isErr)
	if nested {
		// Archive a copy; the script receives the complete output.
		if !isErr {
			a.stash(u.ID, u.Name, subjectOf(u.Name, u.Input), out)
		}
	} else {
		out = a.stash(u.ID, u.Name, subjectOf(u.Name, u.Input), out)
	}
	out += located + blamed
	for _, n := range hookNotes {
		out += "\n[hook: " + n + "]"
	}
	if nested || a.Off["coach"] {
		// still record edits so the syntax check runs when only coaching is off
	} else if tool.MutatingCall(t, u.Input) { // edit coaching only touches serial (mutating) calls, so no lock is needed
		out += a.coach.after(ctx, a.Tools, t, u.Name, u.Input, isErr)
	} else if u.Name == "read_file" {
		a.coachMu.Lock()
		a.coach.after(ctx, a.Tools, t, u.Name, u.Input, isErr)
		a.coachMu.Unlock()
	}
	if !nested && !a.Off["loops"] {
		a.loopMu.Lock()
		note, stop := a.loops.record(u.Name, u.Input, isErr, tool.MutatingCall(t, u.Input))
		if stop {
			a.stuck = true
		}
		a.loopMu.Unlock()
		out += note
	}
	if tool.MutatingCall(t, u.Input) { // serial: safe to read the counters
		out += a.critique(emit)
	}
	emit(Event{Kind: EvToolResult, Call: &call, Output: out, IsError: isErr, Duration: dur})
	a.logEvent(session.Event{Type: session.TypeToolResult, CallID: u.ID, Name: u.Name, Arguments: string(u.Input), Output: out, IsError: isErr, DurationMs: dur.Milliseconds()})
	a.noteToolEgress(ctx, u.Name, u.Input, out)
	a.noteTaint(u.Name, u.Input, out, emit)
	res.Text = out
	if isErr {
		res.Images, res.Data = nil, nil
	}
	return res, isErr, false, nil
}

// runSafely is the one gate every tool result passes through, and so
// the one place credentials can be taken out of it. What a tool read —
// a shell's whole environment, a .env, a 401 body quoting the header it
// rejected — flows from here into the model's context, the tool-result
// event every front-end and the Jev wire render, the session log on
// disk and an eval's recorded output. Redacting downstream would mean
// finding all of those and never missing the next one; redacting here
// means the plaintext never leaves this function. The variable names
// and the surrounding lines survive (see secret.Redact), so the
// model still reads the output it asked for, minus the key.
func (a *Agent) runSafely(ctx context.Context, t tool.Tool, input json.RawMessage) (string, []llm.Block, error) {
	res, err := a.runSafelyResult(ctx, t, input)
	return res.Text, res.Images, err
}

func runSafely(ctx context.Context, t tool.Tool, input json.RawMessage) (string, []llm.Block, error) {
	res, err := runSafelyResult(ctx, t, input)
	return res.Text, res.Images, err
}

// unknownTool is the answer to a call naming no tool of this session. A
// name typed from memory is usually a near miss — "Bash", "readfile",
// "read-file" — so the closest real name comes first, on one line, ahead
// of the whole list a model would otherwise have to search.
func (a *Agent) unknownTool(name string) string {
	if near := textfmt.NearestName(name, a.Tools.Names()); near != "" {
		return fmt.Sprintf("Unknown tool %q; did you mean %q? Available: %s", name, near, a.toolNames())
	}
	var deferred []string
	for _, d := range a.Tools.Deferred() {
		deferred = append(deferred, d.Name)
	}
	if near := textfmt.NearestName(name, deferred); near != "" {
		return fmt.Sprintf("Unknown tool %q; did you mean %q? It is deferred: load it with tool_search first. Available: %s", name, near, a.toolNames())
	}
	return fmt.Sprintf("Unknown tool %q. Available: %s", name, a.toolNames())
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
func editTool(name string) bool {
	return name == "write_file" || name == "edit_file" || name == "notebook_edit" || name == "ast_edit" || name == "apply_patch"
}

// LastTurnMutated reports whether the most recent turn attempted a mutating
// tool. Failed tools may leave partial changes worth checking or remembering.
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
	res, isErr, denied := a.runNestedResult(ctx, id, name, input)
	return res.Text, isErr, denied
}

func (a *Agent) runNestedResult(ctx context.Context, id, name string, input json.RawMessage) (tool.Result, bool, bool) {
	emit := a.emitter()
	var thenRun string
	if editTool(name) && !a.Off["thenrun"] {
		thenRun, input = tool.TakeThenRun(input)
	}
	u := llm.Block{ID: id, Name: name, Input: input}
	start := time.Now()
	ctx, wait := meterWaits(ctx)
	res, isErr, denied, err := a.executeToolResult(ctx, u, emit, true)
	a.stats.tool(name, time.Since(start)-wait.total(), isErr || err != nil)
	if thenRun != "" && err == nil {
		res.Text, err = a.runThenRun(ctx, u, thenRun, res.Text, isErr, emit)
	}
	if err != nil {
		if errors.Is(err, egress.ErrCanary) {
			// Even a script that catches its nested error cannot suppress
			// the canary stop on the enclosing script call.
			where := "a script's " + name + " call"
			a.canaryHit.Store(&where)
		}
		if res.Text == "" {
			res.Text = "Error: " + err.Error()
		}
		res.Images, res.Data = nil, nil
		isErr = true
	}
	return res, isErr, denied
}
