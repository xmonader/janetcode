package agent

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/xmonader/janetcode/internal/hook"
	"github.com/xmonader/janetcode/internal/llm"
	agentprofile "github.com/xmonader/janetcode/internal/skill/profile"
)

// hookInput is the part of every hook's stdin the agent knows about any
// event: where, which session, under which mode.
func (a *Agent) hookInput(event string) hook.Input {
	in := hook.Input{Event: event, Mode: a.Settings().ModeName()}
	if h := a.HookRunner(); h != nil {
		in.Cwd = h.Cwd
	}
	in.SessionID, in.TranscriptPath = a.SessionRef()
	return in
}

// SessionRef is the session's id and the path of its transcript, or two
// empty strings for a session that is not written down.
func (a *Agent) SessionRef() (id, path string) {
	p, ok := a.Log.(interface{ Path() string })
	if !ok || p.Path() == "" {
		return "", ""
	}
	return strings.TrimSuffix(filepath.Base(p.Path()), ".jsonl"), p.Path()
}

// ModeName names the session's permission mode the way hooks and the
// status line command read it.
func (s Settings) ModeName() string {
	switch {
	case s.ReadOnly:
		return "read_only"
	case s.PlanMode:
		return "plan"
	case s.AutoApprove:
		return "yolo"
	case s.AutoMode:
		return "auto"
	case s.AutoEdits:
		return "auto_edits"
	}
	return "ask"
}

// runHook runs an event's hooks and shows the user what they said to the
// user (systemMessage, "message"). It returns the combined result; with
// no hook bound it runs nothing and returns an empty one.
func (a *Agent) runHook(ctx context.Context, in hook.Input, emit func(Event)) hook.Result {
	h := a.HookRunner()
	if !h.Has(in.Event) {
		return hook.Result{Input: in.Input}
	}
	res := h.Run(ctx, in)
	if emit != nil {
		for _, m := range res.Messages {
			emit(Event{Kind: EvNotice, Text: in.Event + " hook: " + m})
		}
	}
	return res
}

// observeTimeout bounds an observing hook that runs after its moment has
// passed (a notification, a failed turn): nothing waits on it, but it
// must not outlive the session by much either.
const observeTimeout = 30 * time.Second

// fireHook runs an event's hooks in the background, for events whose
// hooks only observe: nothing they print changes what happens.
func (a *Agent) fireHook(in hook.Input) {
	h := a.HookRunner()
	if !h.Has(in.Event) {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), observeTimeout)
		defer cancel()
		h.Run(ctx, in)
	}()
}

// failureType classifies the error a turn ended on for stop_failure
// hooks, in the vocabulary other harnesses use for the same event.
func failureType(err error) string {
	switch {
	case errors.Is(err, ErrStuck):
		return "loop_detected"
	case errors.Is(err, ErrMaxIterations):
		return "max_turns"
	case llm.Truncated(err):
		return "server_error"
	}
	var se llm.StatusError
	status := 0
	if errors.As(err, &se) {
		status = se.HTTPStatus()
	}
	l := strings.ToLower(llm.ClassifyText(err))
	switch {
	case status == 401 || strings.Contains(l, "unauthorized") || strings.Contains(l, "invalid api key"):
		return "authentication_failed"
	case status == 402 || status == 403 || strings.Contains(l, "billing") || strings.Contains(l, "quota") || strings.Contains(l, "insufficient"):
		return "billing_error"
	case status == 429 || strings.Contains(l, "rate limit"):
		return "rate_limit"
	case status == 400 || status == 404 || status == 422:
		return "invalid_request"
	case status >= 500:
		return "server_error"
	}
	return "unknown"
}

// sessionHooks is where the session_start and session_end hooks stand
// for the conversation the agent holds.
type sessionHooks struct {
	mu sync.Mutex
	// started is set once session_start has fired for this session: it
	// fires as the session's first turn begins, which every front-end
	// reaches the same way.
	started bool
	// source is how the next session_start reports this session began,
	// set when OpenSession switches to it; "" means startup or resume,
	// read from whether it came with history.
	source string
}

// startSessionHook fires session_start before the first turn of a
// session, and returns what its hooks want the model to know.
func (a *Agent) startSessionHook(ctx context.Context, emit func(Event)) []string {
	a.sessHooks.mu.Lock()
	if a.sessHooks.started {
		a.sessHooks.mu.Unlock()
		return nil
	}
	a.sessHooks.started = true
	source := a.sessHooks.source
	a.sessHooks.mu.Unlock()
	if source == "" {
		source = "startup"
		if len(a.Messages) > 0 {
			source = "resume"
		}
	}
	in := a.hookInput(hook.SessionStart)
	in.Source = source
	return a.runHook(ctx, in, emit).Notes
}

// EndSession fires session_end for a session that ran a turn, and waits
// for its hooks: the process may exit right after. reason is exit, clear
// or resume.
func (a *Agent) EndSession(reason string) {
	a.sessHooks.mu.Lock()
	started := a.sessHooks.started
	a.sessHooks.started = false
	a.sessHooks.mu.Unlock()
	if !started {
		return
	}
	in := a.hookInput(hook.SessionEnd)
	in.Reason = reason
	ctx, cancel := context.WithTimeout(context.Background(), observeTimeout)
	defer cancel()
	a.runHook(ctx, in, nil)
}

// switchSessionHooks ends the session being left and says how the next
// one began.
func (a *Agent) switchSessionHooks(history []llm.Message) {
	next := "clear"
	if len(history) > 0 {
		next = "resume"
	}
	a.EndSession(next)
	a.sessHooks.mu.Lock()
	a.sessHooks.source = next
	a.sessHooks.mu.Unlock()
}

// ErrPromptBlocked is returned by a turn whose prompt a user_prompt hook
// refused; nothing was sent or recorded.
type ErrPromptBlocked struct{ Reason string }

func (e *ErrPromptBlocked) Error() string { return "blocked by a user_prompt hook: " + e.Reason }

// promptHooks runs session_start (on a session's first turn) and
// user_prompt before a turn opens. A refusal ends the turn before
// anything is recorded; what the hooks want the model to know rides with
// the prompt as a text block marked as theirs.
func (a *Agent) promptHooks(ctx context.Context, blocks []llm.Block, emit func(Event)) ([]llm.Block, error) {
	notes := a.startSessionHook(ctx, emit)
	if a.HookRunner().Has(hook.UserPrompt) {
		in := a.hookInput(hook.UserPrompt)
		in.Prompt = firstText(blocks)
		res := a.runHook(ctx, in, emit)
		if res.Denied {
			return nil, &ErrPromptBlocked{Reason: res.Reason}
		}
		notes = append(notes, res.Notes...)
	}
	if len(notes) == 0 {
		return blocks, nil
	}
	ctxBlock := llm.Block{Type: llm.BlockText, Text: "<hook-context>\n" + strings.Join(notes, "\n") + "\n</hook-context>"}
	return append(append([]llm.Block{}, blocks...), ctxBlock), nil
}

// permissionHook runs permission_request hooks as a call is about to be
// put to the user, so a hook can answer in their place. A deny refuses
// the call. An allow (never a project hook's, see hook.Runner) skips the
// prompt, except for a call that must be asked whatever else allows it:
// a protected path, or a rewrite the user has not seen.
func (a *Agent) permissionHook(ctx context.Context, u *llm.Block, call *ToolCall, mustAsk bool, emit func(Event)) (allowed bool, refusal string) {
	if !a.HookRunner().Has(hook.PermissionAsk) {
		return false, ""
	}
	in := a.hookInput(hook.PermissionAsk)
	in.Tool, in.Input = u.Name, u.Input
	res := a.runHook(ctx, in, emit)
	switch {
	case res.Denied:
		return false, "Blocked by a permission_request hook: " + res.Reason
	case res.Allowed && !mustAsk:
		call.Warning = strings.TrimPrefix(call.Warning+"; allowed by a permission_request hook", "; ")
		return true, ""
	}
	return false, ""
}

// notifyHook tells notification hooks the session is waiting on the
// user: kind is permission_prompt or idle_prompt.
func (a *Agent) notifyHook(kind, msg string) {
	if !a.HookRunner().Has(hook.Notification) {
		return
	}
	in := a.hookInput(hook.Notification)
	in.Kind, in.Message = kind, msg
	a.fireHook(in)
}

// subagentKind names a subagent for subagent hooks: its profile, or what
// kind of task started it.
func subagentKind(kid *subagent, profile *agentprofile.Profile) string {
	switch {
	case profile != nil:
		return profile.Name
	case kid.fork:
		return "fork"
	case kid.wt != nil:
		return "editor"
	}
	return "general"
}

// subagentStartHook runs subagent_start hooks as a subagent starts, and
// returns what they want the subagent told, ready to go after its brief.
func (a *Agent) subagentStartHook(ctx context.Context, id, kind string) string {
	if !a.HookRunner().Has(hook.SubagentStart) {
		return ""
	}
	in := a.hookInput(hook.SubagentStart)
	in.AgentID, in.Agent = id, kind
	notes := a.runHook(ctx, in, nil).Notes
	if len(notes) == 0 {
		return ""
	}
	return "\n\n<hook-context>\n" + strings.Join(notes, "\n") + "\n</hook-context>"
}

// subagentStopHook tells subagent_stop hooks a subagent finished, with
// the last thing it said.
func (a *Agent) subagentStopHook(id, kind string, child *Agent) {
	if !a.HookRunner().Has(hook.SubagentStop) {
		return
	}
	in := a.hookInput(hook.SubagentStop)
	in.AgentID, in.Agent, in.Text = id, kind, lastAssistantText(child.Messages)
	a.fireHook(in)
}

// turnEndHooks tells hooks how a turn ended: stop_failure when it failed
// (a turn the user cancelled did not fail), and an idle_prompt
// notification when it finished and the session waits on the user.
func (a *Agent) turnEndHooks(ctx context.Context, err error) {
	switch {
	case err == nil:
		a.notifyHook("idle_prompt", "janetcode is waiting for your input")
	case ctx.Err() == nil && a.HookRunner().Has(hook.StopFailure):
		in := a.hookInput(hook.StopFailure)
		in.ErrorType, in.Error = failureType(err), err.Error()
		a.fireHook(in)
	}
}
