package agent

import (
	"bytes"
	"context"
	"encoding/json"
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
// returns one result per call so the history stays valid, even when ctx is
// cancelled part-way; the error is then ctx.Err().
func (a *Agent) executeAll(ctx context.Context, uses []llm.Block, emit func(Event)) ([]llm.Block, error) {
	a.emitMu.Lock()
	a.emitNow = emit
	a.emitMu.Unlock()
	results := make([]llm.Block, len(uses))
	filled := make([]bool, len(uses))
	parallel := func(u llm.Block) bool {
		t, ok := a.Tools.Get(u.Name)
		return ok && !t.Mutating()
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
				return
			}
			out, isErr, err := a.execute(ctx, u, emit)
			if err == nil {
				results[i] = llm.ToolResult(replyID(u), out, isErr)
				filled[i] = true
			}
		}(i, u)
	}
	wg.Wait()

	cancelled := func() ([]llm.Block, error) {
		for i, u := range uses {
			if !filled[i] {
				results[i] = llm.ToolResult(replyID(u), "Cancelled by user before the tool finished.", true)
			}
		}
		return results, ctx.Err()
	}
	if ctx.Err() != nil {
		return cancelled()
	}
	for i, u := range uses {
		if filled[i] {
			continue
		}
		out, isErr, err := a.execute(ctx, u, emit)
		if err != nil {
			return cancelled()
		}
		results[i] = llm.ToolResult(replyID(u), out, isErr)
		filled[i] = true
	}
	return results, nil
}

// execute runs one tool call. The returned error is non-nil only for
// context cancellation; tool failures come back as (message, true, nil).
func (a *Agent) execute(ctx context.Context, u llm.Block, emit func(Event)) (string, bool, error) {
	start := time.Now()
	out, isErr, err := a.executeTool(ctx, u, emit)
	a.stats.tool(u.Name, time.Since(start), isErr || err != nil)
	return out, isErr, err
}

// allowSession marks name as allowed for the rest of this session,
// initialising the map on first use. RunBlocks also initialises it before
// its own loop starts, but that is a convenience for the common path, not
// the thing that makes this safe: RunRecipe (see recipe.go) drives calls
// through authorize without ever going through RunBlocks's turn setup, so
// initialising only there left a straight-line path — a fresh agent whose
// very first action is RunRecipe, with an approver that answers
// AllowSession — straight into an assignment on a nil map. Initialising
// here, at the point the map is actually written, is safe under every
// caller rather than only the ones remembered to set it up first.
func (a *Agent) allowSession(name string) {
	if a.allowed == nil {
		a.allowed = map[string]bool{}
	}
	a.allowed[name] = true
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
func (a *Agent) authorize(ctx context.Context, t tool.Tool, u *llm.Block, call *ToolCall, emit func(Event)) ([]string, string, error) {
	var hookNotes []string
	hookAllowed := false
	hooks := a.HookRunner()
	if hooks.Has(hook.PreTool) {
		res := hooks.Run(ctx, hook.Input{Event: hook.PreTool, Cwd: hooks.Cwd, Tool: u.Name, Input: u.Input})
		if res.Denied {
			return nil, "Blocked by a hook: " + res.Reason, nil
		}
		hookAllowed = res.Allowed
		if !bytes.Equal(res.Input, u.Input) {
			// The rewritten input is what gets previewed, approved and run.
			u.Input = res.Input
			call.Input = res.Input
			hookNotes = append(hookNotes, "a hook rewrote this call's input to: "+string(res.Input))
		}
		hookNotes = append(hookNotes, res.Notes...)
	}
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
	// A guarded input (a command reaching outside the workspace) asks even
	// under auto-approve or a session-wide allow.
	mustAsk := false
	if g, ok := t.(tool.Guard); ok && t.Mutating() {
		if ask, why := g.Guard(u.Input); ask {
			mustAsk = true
			if why != "" && !strings.Contains(call.Warning, why) {
				call.Warning = strings.TrimPrefix(call.Warning+"; "+why, "; ")
			}
		}
	}
	// A locked accept-test file is refused like a denied permission rule:
	// before anything asks the user, and with no way for AutoApprove or a
	// saved rule to skip past it (see acceptDenial in accept_lock.go).
	if t.Mutating() {
		if reason := a.acceptDenial(t, u.Input); reason != "" {
			return nil, reason, nil
		}
	}
	subject := perm.SubjectIn(u.Name, u.Input, a.displayRoot())
	verdict, rule := a.decideRule(u.Name, subject)
	if verdict == perm.Denied {
		return nil, fmt.Sprintf("Denied by permission rule %s. Do not retry this call; choose another approach or ask the user to change the rule.", rule), nil
	}
	switch {
	case verdict == perm.Allowed:
		call.Warning = strings.TrimPrefix(call.Warning+"; allowed by rule "+rule.String(), "; ")
	case hookAllowed:
		call.Warning = strings.TrimPrefix(call.Warning+"; allowed by a hook", "; ")
	}
	modes := a.Settings()
	autoEdit := (modes.AutoEdits || modes.AutoMode) && editTool(u.Name)
	autoSafe := modes.AutoMode && a.autoAllows(*u)
	if t.Mutating() && (mustAsk || !modes.AutoApprove && !autoEdit && !autoSafe && !a.allowed[u.Name] && verdict != perm.Allowed && !hookAllowed) {
		if a.Approver == nil {
			return nil, "Permission denied: no approver configured for mutating tools.", nil
		}
		if a.Impact != nil && editTool(u.Name) {
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
		if tally := a.Ledger.Get(shape); shape != "" && tally.Allowed >= perm.LearnAfter && tally.Denied == 0 && !mustAsk && call.Warning == "" {
			call.Learned = fmt.Sprintf("approved %d× before, never refused", tally.Allowed)
		}
		var (
			d      Decision
			reason string
			err    error
		)
		if ra, ok := a.Approver.(ReasonApprover); ok {
			d, reason, err = ra.ApproveWithReason(ctx, *call)
		} else {
			d, err = a.Approver.Approve(ctx, *call)
		}
		if err != nil {
			return nil, "", err
		}
		// Recorded before the switch below, which returns early for a
		// denial: the ledger has to see a refusal too, not only the
		// allow variants.
		a.stats.approval(d == Deny)
		if shape != "" && (d != Deny || reason != CancelledReason) {
			a.Ledger.Record(shape, d != Deny)
		}
		if shape != "" && call.Warning != "" && d != Deny {
			a.Ledger.MarkWarned(shape)
		}
		switch d {
		case AllowSession:
			if !mustAsk {
				a.allowSession(u.Name)
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
				return nil, CancelledMessage, nil
			}
			msg := DeniedMessage
			if reason = strings.TrimSpace(reason); reason != "" {
				msg += " The user said: " + reason
			}
			return nil, msg, nil
		}
	}
	return hookNotes, "", nil
}

func (a *Agent) executeTool(ctx context.Context, u llm.Block, emit func(Event)) (string, bool, error) {
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
	refuse := func(msg string, denied bool) (string, bool, error) {
		a.refuseCall(&call, msg, denied, emit)
		return msg, true, nil
	}
	t, ok := a.Tools.Get(u.Name)
	if !ok {
		return refuse(fmt.Sprintf("Unknown tool %q. Available: %s", u.Name, a.toolNames()), false)
	}
	if reason, denied := a.toolGate(t, u.Input); denied {
		return refuse(reason, true)
	}
	hookNotes, deny, err := a.authorize(ctx, t, &u, &call, emit)
	if err != nil {
		return "", true, err
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
		a.cp.snapshot(ctx, a.Snapshots, t, u.Input)
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
	checkingCommit := u.Name == "bash" && looksLikeGitCommit(commandOf(string(u.Input)))
	var commitPreHead string
	if checkingCommit {
		commitPreHead = a.headForCommitCheck(ctx)
	}
	out, err := runSafely(ctx, t, u.Input)
	dur := time.Since(start)
	if err == nil && len(pend) > 0 {
		a.after(u, intent, pend)
	}
	if ctx.Err() != nil {
		return "", true, ctx.Err()
	}
	isErr := false
	if err != nil {
		out = "Error: " + err.Error()
		isErr = true
	} else if t.Mutating() {
		a.mutated = true
		a.mutations++
		if editTool(u.Name) {
			a.acceptTrackEdit(t, u.Input)
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
	out = a.stash(u.ID, u.Name, subjectOf(u.Name, u.Input), out)
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
	return out, isErr, nil
}

func runSafely(ctx context.Context, t tool.Tool, input json.RawMessage) (out string, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("tool %s panicked: %v\n%s", t.Name(), r, debug.Stack())
		}
	}()
	return t.Run(ctx, input)
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
		a.cp.snapshot(ctx, a.Snapshots, t, u.Input)
		pend = a.before(t, u.Input)
	}
	start := time.Now()
	out, runErr := runSafely(ctx, t, u.Input)
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
		a.stash(id, name, subjectOf(name, u.Input), out)
	}
	return out, isErr, false
}

// emitter is the current turn's event sink, or a sink that drops events
// when there is none (a tool run outside a turn, as in a test).
func (a *Agent) emitter() func(Event) {
	a.emitMu.Lock()
	defer a.emitMu.Unlock()
	if a.emitNow == nil {
		return func(Event) {}
	}
	return a.emitNow
}
