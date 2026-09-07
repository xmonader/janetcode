// Package agent runs the model/tool loop: send the conversation, execute the
// tool calls the model asks for (subject to permission), feed results back,
// repeat until the model ends its turn.
package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"runtime/debug"
	"sync"
	"time"

	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/tool"
)

// Decision is the user's answer to a permission request.
type Decision int

const (
	// Deny rejects this call; the model is told the user refused.
	Deny Decision = iota
	// Allow permits this one call.
	Allow
	// AllowSession permits this call and every later call of the same tool.
	AllowSession
)

// ToolCall is a pending invocation shown to the user for approval.
type ToolCall struct {
	ID    string
	Name  string
	Input json.RawMessage
	// Preview describes the effect of the call (a diff for edits). Set once
	// the input is complete, before approval and execution.
	Preview string
	// Warning flags a risky input (destructive shell commands).
	Warning string
}

// Approver decides whether a mutating tool call may run. Implementations
// block until the user answers; ctx cancellation aborts the turn.
type Approver interface {
	Approve(ctx context.Context, call ToolCall) (Decision, error)
}

// ApproverFunc adapts a function to Approver.
type ApproverFunc func(ctx context.Context, call ToolCall) (Decision, error)

// Approve implements Approver.
func (f ApproverFunc) Approve(ctx context.Context, call ToolCall) (Decision, error) {
	return f(ctx, call)
}

// EventKind identifies what happened in the loop.
type EventKind string

const (
	EvText          EventKind = "text"           // Text: streamed assistant text
	EvThinking      EventKind = "thinking"       // Text: streamed reasoning
	EvToolStart     EventKind = "tool_start"     // Call: model began a tool call
	EvToolInput     EventKind = "tool_input"     // Text: partial tool input JSON
	EvToolRun       EventKind = "tool_run"       // Call: input complete, about to execute
	EvToolResult    EventKind = "tool_result"    // Call, Output, IsError, Duration
	EvTurnDone      EventKind = "turn_done"      // Usage, StopReason
	EvTextBlockDone EventKind = "text_block_end" // one assistant message finished streaming
)

// Event is a loop notification for the UI.
type Event struct {
	Kind       EventKind
	Text       string
	Call       *ToolCall
	Output     string
	IsError    bool
	Duration   time.Duration
	Usage      llm.Usage
	StopReason llm.StopReason
}

// Agent holds one conversation and the machinery to advance it.
type Agent struct {
	Provider llm.Provider
	Tools    *tool.Registry
	Approver Approver
	System   string
	Model    string
	// MaxTokens per model call. Zero lets the provider choose.
	MaxTokens int
	// MaxIterations caps model calls per user turn. Zero means 50.
	MaxIterations int
	// AutoApprove skips the Approver for every tool.
	AutoApprove bool
	// CompactAt is the estimated token count that triggers automatic
	// compaction before a model call. Zero means DefaultCompactAt; negative
	// disables it.
	CompactAt int

	// Messages is the conversation history. It is only ever appended to and
	// always ends in a state a provider will accept.
	Messages []llm.Message
	// Usage accumulates over the whole conversation.
	Usage llm.Usage

	allowed map[string]bool

	// Real input size of the last call, and how many messages it covered,
	// so EstimateTokens only has to guess about what came after.
	lastInputTokens int
	lastInputMsgs   int
}

// ErrMaxIterations is returned when a turn exceeds MaxIterations model calls.
var ErrMaxIterations = errors.New("agent: turn exceeded maximum tool iterations")

// Run sends userText and drives the loop until the model ends its turn or
// an error occurs. History stays consistent on every return path.
func (a *Agent) Run(ctx context.Context, userText string, emit func(Event)) error {
	emit = serialize(emit)
	if a.Provider == nil {
		return errors.New("agent: no provider")
	}
	if a.Tools == nil {
		a.Tools = tool.NewRegistry()
	}
	if a.allowed == nil {
		a.allowed = map[string]bool{}
	}
	maxIter := a.MaxIterations
	if maxIter <= 0 {
		maxIter = 50
	}
	a.appendUserText(userText)

	compactAt := a.CompactAt
	if compactAt == 0 {
		compactAt = DefaultCompactAt
	}
	for iter := 0; iter < maxIter; iter++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		if compactAt > 0 && a.EstimateTokens() > compactAt {
			if err := a.Compact(ctx, emit); err != nil && !errors.Is(err, ErrNothingToCompact) {
				return err
			}
		}
		resp, err := a.call(ctx, emit)
		if err != nil && isContextOverflow(err) && compactAt > 0 {
			// The estimate was wrong; summarise and try once more.
			if cerr := a.Compact(ctx, emit); cerr != nil && !errors.Is(cerr, ErrNothingToCompact) {
				return fmt.Errorf("%w (compaction also failed: %v)", err, cerr)
			}
			resp, err = a.call(ctx, emit)
		}
		if err != nil {
			// Nothing appended: the trailing user message stays and the next
			// Run merges into it.
			return err
		}
		a.lastInputTokens = resp.Usage.InputTokens + resp.Usage.CacheReadTokens + resp.Usage.CacheWriteTokens
		a.lastInputMsgs = len(a.Messages)
		a.Messages = append(a.Messages, resp.Message)
		a.Usage.Add(resp.Usage)
		emit(Event{Kind: EvTextBlockDone})

		if resp.StopReason != llm.StopToolUse {
			emit(Event{Kind: EvTurnDone, Usage: resp.Usage, StopReason: resp.StopReason})
			return nil
		}
		uses := resp.Message.ToolUses()
		if len(uses) == 0 {
			// Provider said tool_use but sent none; treat as end of turn
			// rather than looping forever.
			emit(Event{Kind: EvTurnDone, Usage: resp.Usage, StopReason: llm.StopEndTurn})
			return nil
		}
		results, err := a.executeAll(ctx, uses, emit)
		a.Messages = append(a.Messages, llm.ToolResultMessage(results...))
		if err != nil {
			return err
		}
	}
	return ErrMaxIterations
}

// CompactThreshold is the effective token count at which compaction runs,
// or 0 when disabled.
func (a *Agent) CompactThreshold() int {
	switch {
	case a.CompactAt < 0:
		return 0
	case a.CompactAt == 0:
		return DefaultCompactAt
	}
	return a.CompactAt
}

// MaxParallelTools bounds concurrent read-only tool execution.
const MaxParallelTools = 8

// executeAll runs the turn's tool calls: read-only tools concurrently, then
// mutating (permission-gated) tools one at a time in order. It always
// returns one result per call so the history stays valid, even when ctx is
// cancelled part-way; the error is then ctx.Err().
func (a *Agent) executeAll(ctx context.Context, uses []llm.Block, emit func(Event)) ([]llm.Block, error) {
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
				results[i] = llm.ToolResult(u.ID, out, isErr)
				filled[i] = true
			}
		}(i, u)
	}
	wg.Wait()

	cancelled := func() ([]llm.Block, error) {
		for i, u := range uses {
			if !filled[i] {
				results[i] = llm.ToolResult(u.ID, "Cancelled by user before the tool finished.", true)
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
		results[i] = llm.ToolResult(u.ID, out, isErr)
		filled[i] = true
	}
	return results, nil
}

// serialize makes emit safe to call from tool goroutines.
func serialize(emit func(Event)) func(Event) {
	if emit == nil {
		return func(Event) {}
	}
	var mu sync.Mutex
	return func(e Event) {
		mu.Lock()
		defer mu.Unlock()
		emit(e)
	}
}

func (a *Agent) call(ctx context.Context, emit func(Event)) (*llm.Response, error) {
	return a.Provider.Stream(ctx, llm.Request{
		Model:     a.Model,
		System:    a.System,
		Messages:  a.Messages,
		Tools:     a.Tools.Defs(),
		MaxTokens: a.MaxTokens,
	}, func(e llm.Event) { forward(e, emit) })
}

// appendUserText adds text as a new user message, or into the trailing user
// message if the last turn ended with one (tool results from an aborted turn,
// or a message whose model call failed).
func (a *Agent) appendUserText(text string) {
	if n := len(a.Messages); n > 0 && a.Messages[n-1].Role == llm.RoleUser {
		a.Messages[n-1].Blocks = append(a.Messages[n-1].Blocks, llm.Block{Type: llm.BlockText, Text: text})
		return
	}
	a.Messages = append(a.Messages, llm.TextMessage(llm.RoleUser, text))
}

// execute runs one tool call. The returned error is non-nil only for
// context cancellation; tool failures come back as (message, true, nil).
func (a *Agent) execute(ctx context.Context, u llm.Block, emit func(Event)) (string, bool, error) {
	call := ToolCall{ID: u.ID, Name: u.Name, Input: u.Input}
	t, ok := a.Tools.Get(u.Name)
	if !ok {
		msg := fmt.Sprintf("Unknown tool %q. Available: %s", u.Name, a.toolNames())
		emit(Event{Kind: EvToolResult, Call: &call, Output: msg, IsError: true})
		return msg, true, nil
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
	if t.Mutating() && !a.AutoApprove && !a.allowed[u.Name] {
		if a.Approver == nil {
			msg := "Permission denied: no approver configured for mutating tools."
			emit(Event{Kind: EvToolResult, Call: &call, Output: msg, IsError: true})
			return msg, true, nil
		}
		d, err := a.Approver.Approve(ctx, call)
		if err != nil {
			return "", true, err
		}
		switch d {
		case AllowSession:
			a.allowed[u.Name] = true
		case Allow:
		default:
			msg := "The user denied permission for this tool call. Ask before retrying or choose another approach."
			emit(Event{Kind: EvToolResult, Call: &call, Output: msg, IsError: true})
			return msg, true, nil
		}
	}
	emit(Event{Kind: EvToolRun, Call: &call})
	start := time.Now()
	out, err := runSafely(ctx, t, u.Input)
	dur := time.Since(start)
	if ctx.Err() != nil {
		return "", true, ctx.Err()
	}
	isErr := false
	if err != nil {
		out = "Error: " + err.Error()
		isErr = true
	}
	if out == "" {
		out = "(no output)"
	}
	emit(Event{Kind: EvToolResult, Call: &call, Output: out, IsError: isErr, Duration: dur})
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
	return s
}

func forward(e llm.Event, emit func(Event)) {
	switch e.Type {
	case llm.EventTextDelta:
		emit(Event{Kind: EvText, Text: e.Text})
	case llm.EventThinkingDelta:
		emit(Event{Kind: EvThinking, Text: e.Text})
	case llm.EventToolUseStart:
		emit(Event{Kind: EvToolStart, Call: &ToolCall{ID: e.Block.ID, Name: e.Block.Name}})
	case llm.EventToolUseDelta:
		emit(Event{Kind: EvToolInput, Text: e.Text})
	}
}
