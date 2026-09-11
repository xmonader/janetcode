// Package agent runs the model/tool loop: send the conversation, execute the
// tool calls the model asks for (subject to permission), feed results back,
// repeat until the model ends its turn.
package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"runtime/debug"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/xmonader/janetcode/internal/hook"
	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/perm"
	"github.com/xmonader/janetcode/internal/session"
	"github.com/xmonader/janetcode/internal/snapshot"
	"github.com/xmonader/janetcode/internal/tool"
	"github.com/xmonader/janetcode/internal/verify"
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
	// AllowAlways permits this call and saves a rule (see perm.Suggest) so
	// matching calls never ask again, in this or any later session.
	AllowAlways
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
	// Intent is the model's own one-line reason for making this call,
	// when the registry asks for one. Shown to the user as it runs.
	Intent string
}

// Approver decides whether a mutating tool call may run. Implementations
// block until the user answers; ctx cancellation aborts the turn.
type Approver interface {
	Approve(ctx context.Context, call ToolCall) (Decision, error)
}

// ReasonApprover is an Approver that can also pass the user's reason for a
// denial back to the model, so it can adjust instead of guessing.
type ReasonApprover interface {
	Approver
	ApproveWithReason(ctx context.Context, call ToolCall) (Decision, string, error)
}

// DeniedMessage is the tool result the model sees after a denial. The user's
// reason, when given, follows it.
const DeniedMessage = "The user denied permission for this tool call. Ask before retrying or choose another approach."

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
	EvVerdict       EventKind = "verdict"        // Text: what the turn demonstrated
	// EvDiscard says the partial assistant text streamed so far is void
	// and must be taken off the screen: the call it came from failed and
	// is being retried on another model, so nothing of it reaches the
	// conversation either.
	EvDiscard EventKind = "discard"
	// EvPhase names what the turn is waiting on right now — a retry and
	// its countdown, a compaction, a subagent. It is a status line, never
	// a transcript entry, and the next event supersedes it.
	EvPhase EventKind = "phase"
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
	stats statsCounter
	// summaryFailedAt starts the summaryCooldown after a failed summary.
	summaryFailedAt time.Time
	// working is the typed record carried across compactions.
	working WorkingMemory
	// Predictor, when set, scores the running turn from its trajectory
	// features (see Trajectory) and warns the model once per turn when
	// the score is low. Trained from eval runs with Train.
	Predictor *Predictor
	turnStart time.Time
	warned    bool
	lastScore float64

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
	// AutoEdits skips the Approver for file edits only: write_file and
	// edit_file go through, commands still ask. The natural mode for a
	// refactor, where every edit would be approved anyway.
	AutoEdits bool
	// Rules are persistent permission rules consulted before the Approver:
	// a deny refuses the call outright, an allow skips the prompt.
	Rules perm.Rules
	// Off disables named behaviours for ablation runs: "coach" (edit
	// coaching), "syntax" (syntax check and covering-test pointer after
	// edits), "loops" (loop nudges and stuck detection), "prune"
	// (condensing old tool results before a summary), "verify" (the
	// verification gate), "repomap" (honoured by the caller that installs
	// the Prepare hook), "working" (the typed working-memory record; off
	// means a prose handoff note), "plan" (checking the names in the
	// model's plan against the repository), "critic" (the trajectory
	// predictor's notes). Unknown names are ignored.
	Off map[string]bool
	// Verify, when set, runs the project's definition of done after a turn
	// that changed something. Failures go back to the model before the
	// turn ends, up to verify.MaxRounds times.
	Verify *verify.Runner
	// ReadOnly hides mutating tools from the model and refuses them if
	// called anyway. PlanMode does the same and asks for a plan.
	ReadOnly bool
	PlanMode bool
	// Fallbacks are models tried in order when a call fails in a way
	// another model could survive: an exhausted quota, or rate limits
	// and server errors that outlived the provider's own retries. At
	// most MaxFallbacks are used; the one that works becomes the
	// session's model.
	Fallbacks []string
	// Route, when set, resolves a model name to the provider that serves
	// it, so a fallback can be on a different backend. Without it a
	// fallback is sent to the provider already in use.
	Route func(model string) (llm.Provider, string, error)
	// Pins are user notes kept in the system prompt across compaction.
	Pins []string
	// Jobs, when set, is polled before each model call so finished
	// background commands are reported to the model as they complete.
	Jobs *tool.Jobs
	// ContextWindow is the model's input limit when known; with CompactAt
	// unset, compaction runs at three quarters of it (capped, see
	// CompactThreshold) instead of DefaultCompactAt.
	ContextWindow int
	// PlanCheck, when set, is given the text the model wrote before a
	// batch of tool calls (its plan) and returns a note about names in
	// it the repository does not have, or "".
	PlanCheck func(ctx context.Context, text string) string
	// Diagnose, when set, is called with the path a file tool just wrote
	// and returns a note (language-server diagnostics) appended to the
	// tool result, or "".
	Diagnose func(ctx context.Context, path string) string
	// mutations counts successful mutating tool calls this turn.
	mutations int
	// SaveRule, when set, persists a rule the user created with AllowAlways.
	SaveRule func(perm.Rule) error
	// CompactAt is the estimated token count that triggers automatic
	// compaction before a model call. Zero means DefaultCompactAt; negative
	// disables it.
	CompactAt int
	// Debug, when set, receives a timestamped line per event and model call.
	Debug io.Writer

	// Messages is the conversation history. It is only ever appended to and
	// always ends in a state a provider will accept.
	Messages []llm.Message
	// Usage accumulates over the whole conversation.
	Usage llm.Usage
	// Price, when set, lets the UI estimate spend for providers that do not
	// report cost.
	Price *llm.Price
	// UndoPath, when set, persists undo checkpoints next to the session so
	// /undo survives a resume.
	UndoPath string
	// Log, when set, is the append-only trajectory of this conversation.
	// Resume reconstructs counts by folding it. Nil means in-memory stats only.
	Log          session.Log
	turnN, stepN int
	// undoRemoved is how many messages the last Undo removed.
	undoRemoved int
	// Archive, when set, keeps full tool outputs longer than InlineOutput
	// on disk so the model can page through them with expand_output.
	Archive *Archive
	// Snapshots, when set, snapshots the git work tree before the first
	// mutating tool of each turn so Undo also reverts shell changes.
	Snapshots *snapshot.Repo
	// Hooks, when set, runs user commands around tool calls, compaction
	// and turn end (see package hook). Nil means no hooks.
	Hooks *hook.Runner
	// SystemExtra, when set, is appended to System. It is evaluated once
	// and reused until compaction or RefreshExtra, so the cached prompt
	// prefix stays valid while memory and skills change underneath.
	SystemExtra func() string
	extraText   string
	extraFrozen bool
	// PathRules, when set, is asked after each tool call which project
	// rules the paths it touched bring in, and returns the text of those
	// not sent yet (see package rules). Each arrives as harness text
	// before the next model call, so instructions can be added to a
	// session without being added to every request of it.
	PathRules func(paths []string) []string
	// Prepare, when set, may add blocks to a user turn before it is sent
	// (skills attach themselves this way). The returned note, if any, is
	// emitted as a notice.
	Prepare func(blocks []llm.Block) ([]llm.Block, string)

	mutated bool // a mutating tool ran successfully this turn

	allowed map[string]bool
	// callIDs is every tool-call id handed out this conversation, so a
	// provider that repeats or omits one cannot collide with it.
	callIDs map[string]bool
	cp      checkpointer
	coach   coach
	coachMu sync.Mutex // read_file runs in parallel goroutines
	loops   loopDetector
	loopMu  sync.Mutex
	usageMu sync.Mutex  // the status bar reads Usage while a turn runs
	steerMu sync.Mutex  // guards steer, written from the UI goroutine
	steer   []string    // notes for the running turn (see Steer)
	harness []string    // harness text for the running turn (path rules)
	turnUse llm.Usage   // what the turn in progress has cost so far
	cache   cacheLedger // prefix paid for twice, and why
	prefix  prefixWatch // where the cached prefix stopped matching
	// historyRewritten says a prune or compaction edited messages since
	// the last model call, so the next cache miss is expected and is not
	// counted as waste. It is separate from integ.shrunkOK, which the
	// integrity check consumes.
	historyRewritten bool
	// tools is this turn's tool list, held so that a server registering
	// mid-turn cannot rewrite the cached prefix. Cleared at each turn.
	tools    []llm.ToolDef
	estimate atomic.Int64 // last EstimateTokens, published for the status bar
	stuck    bool
	integ    integrity

	// emitNow is the current turn's event sink. A tool that dispatches
	// further tool calls of its own — a script does — needs it to show
	// them as they happen, and it is not on the Tool interface.
	emitMu  sync.Mutex
	emitNow func(Event)

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
	return a.RunBlocks(ctx, []llm.Block{{Type: llm.BlockText, Text: userText}}, emit)
}

// RunBlocks is Run for a user turn made of several blocks (text plus
// images).
func (a *Agent) RunBlocks(ctx context.Context, blocks []llm.Block, emit func(Event)) error {
	emit = serialize(a.traced(emit))
	if a.Provider == nil {
		return errors.New("agent: no provider")
	}
	a.stats.turn()
	a.startTurn()
	a.tools = nil // pick up any tool that registered since the last turn
	a.coach.noSyntax = a.Off["syntax"]
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
	if a.Prepare != nil {
		var note string
		blocks, note = a.Prepare(blocks)
		if note != "" {
			emit(Event{Kind: EvNotice, Text: note})
		}
	}
	// The checkpoint remembers the conversation as it stood before this
	// turn's prompt, so an undo can take the whole turn out, prompt
	// included; a prompt left behind reads as a request to do it again.
	before := len(a.Messages)
	merged := a.appendUserBlocks(blocks...)
	a.turnN++
	a.stepN = 0
	a.logEvent(session.Event{Type: session.TypeTurnStart, Turn: a.turnN})
	a.logUserMessage(merged)
	a.cp.beginTurn(a.turnN, before)
	a.mutated, a.mutations = false, 0
	defer a.endTurn()
	// A note typed with ctrl+s reaches the model before the turn's next
	// call. A turn that ends with its final answer has no next call, so
	// the note stays queued for the turn after this one. Say so, rather
	// than letting the user watch what they typed disappear.
	defer func() {
		a.steerMu.Lock()
		n := len(a.steer)
		a.steerMu.Unlock()
		if n > 0 {
			emit(Event{Kind: EvNotice, Text: fmt.Sprintf(
				"%d steering note(s) arrived after the turn's last model call; they go with your next message",
				n)})
		}
	}()
	a.loops.reset()
	a.turnStart, a.warned = time.Now(), false
	a.integ.userText = ""
	for _, b := range blocks {
		if b.Type == llm.BlockText && b.Text != "" {
			a.integ.userText = b.Text
			break
		}
	}

	compactAt := a.CompactThreshold()
	stops, needs := 0, 0
	corrections := 0
	gate := gateState{}
	for iter := 0; iter < maxIter; iter++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		// Recomputed every step: it also publishes the estimate the status
		// bar reads while the turn runs.
		if est := a.EstimateTokens(); compactAt > 0 && est > compactAt {
			if err := a.Compact(ctx, emit); err != nil && !errors.Is(err, ErrNothingToCompact) {
				return err
			}
		}
		a.checkIntegrity(emit)
		a.reportJobs(emit)
		a.deliverSteering(emit)
		a.stepN++
		a.logEvent(session.Event{Type: session.TypeStepStart, Turn: a.turnN, Step: a.stepN})
		resp, err := a.call(ctx, emit)
		if err != nil && isContextOverflow(err) && compactAt > 0 {
			// The estimate was wrong; summarise and try once more. When the
			// error names the model's real limit, compact earlier from now on.
			if w := windowFromError(err); w > 0 && w*3/4 < compactAt {
				compactAt = w * 3 / 4
				a.CompactAt = compactAt
			}
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
		// A model that wrote its call as prose meant to call the tool;
		// reading it back costs nothing and saves the turn.
		a.recoverToolCalls(resp, emit)
		// Every call gets an id of its own, whatever the server numbered
		// it: the transcript, the permission prompt and the log all key
		// by it. The reply still goes back under the server's own id.
		if a.callIDs == nil {
			a.callIDs = map[string]bool{}
		}
		normalizeCallIDs(&resp.Message, a.callIDs, a.turnN, a.stepN)
		a.lastInputTokens = resp.Usage.InputTokens + resp.Usage.CacheReadTokens + resp.Usage.CacheWriteTokens
		a.lastInputMsgs = len(a.Messages)
		a.Messages = append(a.Messages, resp.Message)
		a.integ.lastLen = len(a.Messages)
		a.addUsage(resp.Usage)
		msg := resp.Message
		u := resp.Usage
		a.logEvent(session.Event{Type: session.TypeAssistant, Turn: a.turnN, Step: a.stepN, Surface: true, Message: &msg, Usage: &u})
		emit(Event{Kind: EvTextBlockDone})
		// A model that keeps writing the same message has stopped reading
		// its own results; the ladder is the same as for a repeated call.
		// The note rides with this step's tool results, because nothing
		// may come between a tool call and its answer.
		repeatNote := ""
		if !a.Off["loops"] {
			a.loopMu.Lock()
			note, stop := a.loops.recordText(resp.Message.Text())
			a.loopMu.Unlock()
			if stop {
				emit(Event{Kind: EvNotice, Text: "turn stopped: " + a.loops.reason})
				a.logEvent(session.Event{Type: session.TypeNotice, Output: "turn stopped: " + a.loops.reason})
				a.logEvent(session.Event{Type: session.TypeTurnEnd, Turn: a.turnN, Reason: "stuck"})
				return ErrStuck
			}
			repeatNote = note
		}

		// A response cut off at the output limit may have had its tool
		// arguments truncated mid-JSON and then repaired into something
		// that parses and validates but is missing content — a half-
		// written file reported as a success. The calls are refused
		// rather than run, and answered so the history stays paired and
		// the model knows to reissue them.
		if resp.StopReason == llm.StopMaxTokens {
			if uses := resp.Message.ToolUses(); len(uses) > 0 {
				results := make([]llm.Block, 0, len(uses))
				for _, u := range uses {
					call := ToolCall{ID: u.ID, Name: u.Name, Input: u.Input}
					msg := fmt.Sprintf("%s was not run: the response hit the output token limit, so its arguments may be cut short. Reissue it with complete arguments, and make them smaller if they were long.", u.Name)
					results = append(results, llm.ToolResult(replyID(u), msg, true))
					emit(Event{Kind: EvToolResult, Call: &call, Output: msg, IsError: true})
					a.logEvent(session.Event{Type: session.TypeToolResult, CallID: u.ID, Name: u.Name, Arguments: string(u.Input), Output: msg, IsError: true})
				}
				tr := llm.ToolResultMessage(results...)
				a.Messages = append(a.Messages, tr)
				a.logEvent(session.Event{Type: session.TypeToolResult, Turn: a.turnN, Step: a.stepN, Surface: true, Message: &tr})
				a.logEvent(session.Event{Type: session.TypeStepEnd, Turn: a.turnN, Step: a.stepN})
				continue
			}
		}
		if resp.StopReason != llm.StopToolUse {
			if more := a.faultNeeds(resp.Message.Text(), &needs, emit); more {
				continue
			}
			if more := a.stopHook(ctx, resp.Message.Text(), &stops, emit); more {
				continue
			}
			if more := !a.Off["verify"] && a.verifyGate(ctx, &gate, emit); more {
				continue
			}
			// What the turn actually demonstrated, decided from what the
			// harness watched rather than from what was written. A claim
			// the turn does not support is put back to the model once.
			outcome := a.Outcome()
			if !a.Off["verdict"] && outcome.Overclaimed() && corrections == 0 {
				corrections++
				emit(Event{Kind: EvNotice, Text: "the answer claims more than this turn shows: " + outcome.Why})
				a.appendHarnessText(outcome.Correction())
				continue
			}
			a.logOutcome(outcome)
			if !a.Off["verdict"] && outcome.Verdict != VerdictNoChanges {
				emit(Event{Kind: EvVerdict, Text: outcome.Line(), IsError: !outcome.OK()})
			}
			a.logEvent(session.Event{Type: session.TypeStepEnd, Turn: a.turnN, Step: a.stepN})
			a.logEvent(session.Event{Type: session.TypeTurnEnd, Turn: a.turnN, Reason: string(resp.StopReason)})
			emit(Event{Kind: EvTurnDone, Usage: a.TurnUsage(), StopReason: resp.StopReason})
			return nil
		}
		uses := resp.Message.ToolUses()
		if len(uses) == 0 {
			// Provider said tool_use but sent none; treat as end of turn
			// rather than looping forever.
			a.logEvent(session.Event{Type: session.TypeStepEnd, Turn: a.turnN, Step: a.stepN})
			a.logEvent(session.Event{Type: session.TypeTurnEnd, Turn: a.turnN, Reason: string(llm.StopEndTurn)})
			emit(Event{Kind: EvTurnDone, Usage: a.TurnUsage(), StopReason: llm.StopEndTurn})
			return nil
		}
		planNote := ""
		if a.PlanCheck != nil && !a.Off["plan"] {
			if text := strings.TrimSpace(resp.Message.Text()); text != "" {
				planNote = a.PlanCheck(ctx, text)
			}
		}
		results, err := a.executeAll(ctx, uses, emit)
		if repeatNote != "" && len(results) > 0 {
			results[len(results)-1].Text += repeatNote
			emit(Event{Kind: EvNotice, Text: strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(repeatNote), "[harness]"))})
		}
		if planNote != "" && len(results) > 0 {
			results[len(results)-1].Text += "\n\n[harness] " + planNote
			emit(Event{Kind: EvNotice, Text: planNote})
		}
		tr := llm.ToolResultMessage(results...)
		a.Messages = append(a.Messages, tr)
		a.logEvent(session.Event{Type: session.TypeToolResult, Turn: a.turnN, Step: a.stepN, Surface: true, Message: &tr})
		a.logEvent(session.Event{Type: session.TypeStepEnd, Turn: a.turnN, Step: a.stepN})
		if err != nil {
			return err
		}
		if a.stuck {
			a.stuck = false
			emit(Event{Kind: EvNotice, Text: "turn stopped: " + a.loops.reason})
			a.logEvent(session.Event{Type: session.TypeNotice, Output: "turn stopped: " + a.loops.reason})
			a.logEvent(session.Event{Type: session.TypeTurnEnd, Turn: a.turnN, Reason: "stuck"})
			return ErrStuck
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
	case a.CompactAt > 0:
		return a.CompactAt
	case a.ContextWindow > 0:
		t := a.ContextWindow * 3 / 4
		if t > MaxAutoCompactAt {
			t = MaxAutoCompactAt
		}
		if t < 8_000 {
			t = 8_000
		}
		return t
	}
	return DefaultCompactAt
}

// MaxAutoCompactAt caps the window-derived threshold: past it, long
// histories cost more and models attend worse, so compaction runs
// earlier than the model strictly requires unless compact_at says so.
const MaxAutoCompactAt = 200_000

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

// turnTools is the tool list for this turn, built once and reused for
// every model call in it.
//
// MCP servers connect in the background and register their tools whenever
// they are ready, which can be in the middle of a turn. The tools array is
// part of the cached request prefix, so a tool appearing between two calls
// of the same turn re-bills the whole prompt — and it is not only the
// tools themselves: tool_search names the deferred tools in its own
// description, so even a deferred registration rewrites the array. A tool
// that arrives mid-turn becomes available on the next one, which costs
// nothing anybody notices.
func (a *Agent) turnTools() []llm.ToolDef {
	if a.tools != nil {
		return a.tools
	}
	if a.readOnly() {
		a.tools = a.Tools.DefsIf(func(t tool.Tool) bool { return !t.Mutating() })
	} else {
		a.tools = a.Tools.Defs()
	}
	return a.tools
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

// call sends the conversation to the session's model, falling back to the
// configured alternatives when the failure is one another model could
// survive (see fallback).
func (a *Agent) call(ctx context.Context, emit func(Event)) (*llm.Response, error) {
	resp, err := a.callOn(ctx, a.Provider, a.Model, emit)
	if err == nil {
		return resp, nil
	}
	return a.fallback(ctx, err, emit)
}

func (a *Agent) callOn(ctx context.Context, prov llm.Provider, model string, emit func(Event)) (*llm.Response, error) {
	start := time.Now()
	a.debugf("call start: %d messages, ~%d tokens", len(a.Messages), a.EstimateTokens())
	system := a.systemPrompt()
	defs := a.turnTools()
	a.usageMu.Lock()
	a.prefix.note(a.turnN, system, a.Messages, defs)
	a.usageMu.Unlock()
	resp, err := prov.Stream(ctx, llm.Request{
		Model:     model,
		System:    system,
		Messages:  a.Messages,
		Tools:     defs,
		MaxTokens: a.MaxTokens,
	}, func(e llm.Event) { forward(e, emit) })
	a.stats.model(time.Since(start), err != nil)
	if err != nil {
		a.debugf("call failed after %s: %v", time.Since(start).Round(time.Millisecond), err)
	} else {
		a.debugf("call done in %s: stop=%s usage=%+v", time.Since(start).Round(time.Millisecond), resp.StopReason, resp.Usage)
	}
	return resp, err
}

// recoverToolCalls rescues a turn whose tool calls arrived as text. Some
// models — small ones, quantised ones, anything behind a gateway that
// dropped the tool schema — write <invoke name=...> or a <tool_call> JSON
// block into their answer instead of emitting a call. Left alone the turn
// ends having done nothing; parsed, it does what the model asked for.
// Only calls naming a tool this session actually has are taken, so prose
// that merely looks like a call is still prose.
func (a *Agent) recoverToolCalls(resp *llm.Response, emit func(Event)) {
	if resp == nil || resp.StopReason == llm.StopToolUse || len(resp.Message.ToolUses()) > 0 {
		return
	}
	text := resp.Message.Text()
	calls, remaining, ok := llm.RecoverToolCalls(text)
	if !ok {
		return
	}
	kept := make([]llm.RecoveredCall, 0, len(calls))
	for _, c := range calls {
		if _, have := a.Tools.Get(c.Name); have {
			kept = append(kept, c)
		}
	}
	if len(kept) == 0 {
		return
	}
	blocks := make([]llm.Block, 0, len(resp.Message.Blocks)+len(kept))
	for _, b := range resp.Message.Blocks {
		if b.Type != llm.BlockText {
			blocks = append(blocks, b)
		}
	}
	if remaining != "" {
		blocks = append(blocks, llm.Block{Type: llm.BlockText, Text: remaining})
	}
	blocks = append(blocks, llm.RecoveredBlocks(kept, fmt.Sprintf("recovered_%d_%d", a.turnN, a.stepN))...)
	resp.Message.Blocks = blocks
	resp.StopReason = llm.StopToolUse
	a.stats.recovered(len(kept))
	names := make([]string, 0, len(kept))
	for _, c := range kept {
		names = append(names, c.Name)
	}
	emit(Event{Kind: EvNotice, Text: fmt.Sprintf("the model wrote %s as text instead of calling it; recovered and run", strings.Join(names, ", "))})
}

func (a *Agent) debugf(format string, args ...any) {
	if a.Debug == nil {
		return
	}
	fmt.Fprintf(a.Debug, "%s "+format+"\n", append([]any{time.Now().Format("15:04:05.000")}, args...)...)
}

// traced wraps emit with debug logging of every event.
func (a *Agent) traced(emit func(Event)) func(Event) {
	if a.Debug == nil {
		return emit
	}
	if emit == nil {
		emit = func(Event) {}
	}
	return func(e Event) {
		name := ""
		if e.Call != nil {
			name = " " + e.Call.Name
		}
		a.debugf("event %s%s len=%d", e.Kind, name, len(e.Text)+len(e.Output))
		emit(e)
	}
}

// AddContext records text the user produced outside the model's view (a
// shell command they ran, a note) as user content for the next turn. It
// mutates the history and is only safe between turns; use Steer to say
// something to a turn that is already running.
func (a *Agent) AddContext(text string) { a.appendUserText(text) }

// Steer queues a note for the running turn. The history belongs to the
// turn goroutine while it runs, so the note is held here and delivered by
// that goroutine before its next model call — which is the difference
// between steering a turn and starting another one.
func (a *Agent) Steer(text string) {
	if strings.TrimSpace(text) == "" {
		return
	}
	a.steerMu.Lock()
	a.steer = append(a.steer, text)
	a.steerMu.Unlock()
}

// deliverSteering hands any queued notes to the model. It runs on the
// turn goroutine, just before the call that will read them.
func (a *Agent) deliverSteering(emit func(Event)) {
	a.steerMu.Lock()
	notes, harness := a.steer, a.harness
	a.steer, a.harness = nil, nil
	a.steerMu.Unlock()
	if len(harness) > 0 {
		// Harness text — a path-scoped rule this turn brought in — is
		// delivered the same way but marked synthetic, so nothing
		// downstream mistakes it for the user's words.
		a.deliverText(strings.Join(harness, "\n\n"), true)
	}
	if len(notes) == 0 {
		return
	}
	text := strings.Join(notes, "\n\n")
	a.deliverText(text, false)
	emit(Event{Kind: EvNotice, Text: "steering the turn: " + strings.SplitN(text, "\n", 2)[0]})
}

// deliverText adds text to the conversation for the next model call. It
// rides with the trailing tool results when there are any: two user
// messages in a row are not something every provider accepts.
func (a *Agent) deliverText(text string, synthetic bool) {
	n := len(a.Messages)
	if n > 0 && a.Messages[n-1].Role == llm.RoleUser {
		a.Messages[n-1].Blocks = append(a.Messages[n-1].Blocks, llm.Block{Type: llm.BlockText, Text: text, Synthetic: synthetic})
		a.logUserMessage(true)
		return
	}
	if synthetic {
		a.appendHarnessText(text)
		return
	}
	a.appendUserText(text)
}

// appendUserText adds text as a new user message. It never mutates a message
// already sent to the provider: editing a cached prefix (especially a tool-
// result turn) makes DashScope/OpenRouter re-bill the whole history as fresh.
func (a *Agent) appendUserText(text string) {
	a.logUserMessage(a.appendUserBlocks(llm.Block{Type: llm.BlockText, Text: text}))
}

// appendHarnessText adds text the harness itself wrote — a correction, a
// check's output, a job update — as a user message marked synthetic, so
// nothing downstream mistakes it for something the user said.
func (a *Agent) appendHarnessText(text string) {
	a.logUserMessage(a.appendUserBlocks(llm.Block{Type: llm.BlockText, Text: text, Synthetic: true}))
}

// AddHarnessContext is AddContext for text the UI's own machinery
// produces (a finished background job), not the user.
func (a *Agent) AddHarnessContext(text string) { a.appendHarnessText(text) }

// addUsage accumulates the usage of one model call. The status bar reads it
// from the UI goroutine while the turn runs, so it is guarded.
func (a *Agent) addUsage(u llm.Usage) {
	a.usageMu.Lock()
	a.Usage.Add(u)
	a.turnUse.Add(u)
	// A prefix paid for twice is invisible in the totals; the ledger
	// compares each call with the one before it. A history the harness
	// rewrote (a prune or a compaction) invalidates the prefix for a
	// reason, so it is not counted as waste.
	a.cache.record(u, a.Model, a.turnN, time.Now(), a.historyRewritten)
	a.historyRewritten = false
	a.usageMu.Unlock()
}

// TurnUsage is what the current turn has cost across all of its model
// calls. The last call's usage is not the turn's: a turn that took four
// calls to finish paid for four, and reporting only the last one makes a
// long turn look like a short one.
func (a *Agent) TurnUsage() llm.Usage {
	a.usageMu.Lock()
	defer a.usageMu.Unlock()
	return a.turnUse
}

// startTurn resets the per-turn usage.
func (a *Agent) startTurn() {
	a.usageMu.Lock()
	a.turnUse = llm.Usage{}
	a.usageMu.Unlock()
}

// UsageSnapshot is a copy of the usage so far, safe to read while a turn
// is running.
func (a *Agent) UsageSnapshot() llm.Usage {
	a.usageMu.Lock()
	defer a.usageMu.Unlock()
	return a.Usage
}

// logUserMessage records the trailing user message on the log. When the
// blocks merged into a message that is already logged (input typed after a
// failed call, or a note added before the turn ran) the event replaces that
// one, so the fold holds the grown message once instead of twice.
func (a *Agent) logUserMessage(merged bool) {
	n := len(a.Messages)
	if a.Log == nil || n == 0 {
		return
	}
	msg := a.Messages[n-1]
	e := session.Event{Type: session.TypeUserMessage, Surface: true, Message: &msg}
	if merged {
		if seqs := session.CurrentSeqs(a.Log.Events()); len(seqs) == n {
			e.SurfaceOp = session.SurfaceOpReplace
			e.Replaces = []int{seqs[n-1]}
		}
	}
	a.logEvent(e)
}

// appendUserBlocks adds a user turn, reporting whether it merged into the
// trailing message rather than making a new one.
func (a *Agent) appendUserBlocks(blocks ...llm.Block) bool {
	if len(blocks) == 0 {
		return false
	}
	n := len(a.Messages)
	// Merge only into a trailing plain-text user turn (typed input before
	// the first model call). Tool-result messages have already gone over
	// the wire; appending to them would change bytes the prompt cache keyed on.
	if n > 0 && a.Messages[n-1].Role == llm.RoleUser && !hasToolResult(a.Messages[n-1]) {
		a.Messages[n-1].Blocks = append(a.Messages[n-1].Blocks, blocks...)
		return true
	}
	a.Messages = append(a.Messages, llm.Message{Role: llm.RoleUser, Blocks: blocks})
	return false
}

// execute runs one tool call. The returned error is non-nil only for
// context cancellation; tool failures come back as (message, true, nil).
func (a *Agent) execute(ctx context.Context, u llm.Block, emit func(Event)) (string, bool, error) {
	start := time.Now()
	out, isErr, err := a.executeTool(ctx, u, emit)
	a.stats.tool(u.Name, time.Since(start), isErr || err != nil)
	return out, isErr, err
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
	if a.Hooks.Has(hook.PreTool) {
		res := a.Hooks.Run(ctx, hook.Input{Event: hook.PreTool, Cwd: a.Hooks.Cwd, Tool: u.Name, Input: u.Input})
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
	subject := perm.Subject(u.Name, u.Input)
	verdict, rule := a.Rules.Decide(u.Name, subject)
	if verdict == perm.Denied {
		return nil, fmt.Sprintf("Denied by permission rule %s. Do not retry this call; choose another approach or ask the user to change the rule.", rule), nil
	}
	switch {
	case verdict == perm.Allowed:
		call.Warning = strings.TrimPrefix(call.Warning+"; allowed by rule "+rule.String(), "; ")
	case hookAllowed:
		call.Warning = strings.TrimPrefix(call.Warning+"; allowed by a hook", "; ")
	}
	autoEdit := a.AutoEdits && editTool(u.Name)
	if t.Mutating() && (mustAsk || !a.AutoApprove && !autoEdit && !a.allowed[u.Name] && verdict != perm.Allowed && !hookAllowed) {
		if a.Approver == nil {
			return nil, "Permission denied: no approver configured for mutating tools.", nil
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
		switch d {
		case AllowSession:
			if !mustAsk {
				a.allowed[u.Name] = true
			}
		case AllowAlways:
			if !mustAsk {
				r := perm.Suggest(u.Name, subject)
				a.Rules.Add(perm.Allowed, r)
				if a.SaveRule != nil {
					if err := a.SaveRule(r); err != nil {
						emit(Event{Kind: EvNotice, Text: "could not save permission rule " + r.String() + ": " + err.Error()})
					} else {
						emit(Event{Kind: EvNotice, Text: "saved permission rule: " + r.String()})
					}
				}
			}
		case Allow:
		default:
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
	t, ok := a.Tools.Get(u.Name)
	if !ok {
		msg := fmt.Sprintf("Unknown tool %q. Available: %s", u.Name, a.toolNames())
		emit(Event{Kind: EvToolResult, Call: &call, Output: msg, IsError: true})
		return msg, true, nil
	}
	if a.readOnly() && t.Mutating() {
		msg := fmt.Sprintf("%s is not available: this session is read-only (%s). Describe what you would do instead.", u.Name, a.modeName())
		emit(Event{Kind: EvToolResult, Call: &call, Output: msg, IsError: true})
		return msg, true, nil
	}
	hookNotes, deny, err := a.authorize(ctx, t, &u, &call, emit)
	if err != nil {
		return "", true, err
	}
	if deny != "" {
		emit(Event{Kind: EvToolResult, Call: &call, Output: deny, IsError: true})
		return deny, true, nil
	}
	emit(Event{Kind: EvToolRun, Call: &call})
	a.logEvent(session.Event{Type: session.TypeToolCall, CallID: u.ID, Name: u.Name, Arguments: string(u.Input), Intent: intent})
	// A rule scoped to these paths joins the conversation before the
	// next model call, once for the session.
	a.applyPathRules(t, u.Input, emit)
	if t.Mutating() {
		a.cp.snapshot(a.Snapshots, t, u.Input)
	}
	start := time.Now()
	var pend []pending
	if t.Mutating() {
		pend = a.before(t, u.Input)
	}
	out, err := runSafely(ctx, t, u.Input)
	dur := time.Since(start)
	if err == nil && len(pend) > 0 {
		a.after(u, pend)
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
	}
	if out == "" {
		out = "(no output)"
	}
	if a.Hooks.Has(hook.PostTool) {
		res := a.Hooks.Run(ctx, hook.Input{Event: hook.PostTool, Cwd: a.Hooks.Cwd, Tool: u.Name, Input: u.Input, Output: out, IsError: isErr})
		hookNotes = append(hookNotes, res.Notes...)
	}
	if a.Diagnose != nil && !isErr && (u.Name == "write_file" || u.Name == "edit_file") {
		if note := a.Diagnose(ctx, perm.Subject(u.Name, u.Input)); note != "" {
			out += "\n" + note
		}
	}
	// A result that would take an unreasonable share of what is left is
	// withheld with its price rather than truncated: truncation bills the
	// caller for a prefix that is rarely the answer.
	if !isErr && !a.Off["oversized"] {
		if msg := oversized(u.Name, subjectOf(u.Name, u.Input), out, a.EstimateTokens(), a.ContextWindow, accepted); msg != "" {
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

// LastTurnMutated reports whether the most recent turn ran a mutating tool
// successfully, i.e. whether it may have decided something worth
// remembering.
func (a *Agent) LastTurnMutated() bool { return a.mutated }

// stopHook gives stop hooks a chance to send the model back to work. It
// reports whether the turn continues.
func (a *Agent) stopHook(ctx context.Context, text string, stops *int, emit func(Event)) bool {
	if !a.Hooks.Has(hook.Stop) || *stops >= maxStopContinues {
		return false
	}
	res := a.Hooks.Run(ctx, hook.Input{Event: hook.Stop, Cwd: a.Hooks.Cwd, Text: text})
	if res.Continue == "" {
		return false
	}
	*stops++
	emit(Event{Kind: EvNotice, Text: fmt.Sprintf("stop hook sent the model back to work (%d/%d): %s", *stops, maxStopContinues, res.Continue)})
	a.Messages = append(a.Messages, llm.HarnessMessage(llm.RoleUser, "[A stop hook did not accept this as done]\n"+res.Continue))
	return true
}

func (a *Agent) readOnly() bool { return a.ReadOnly || a.PlanMode }

// reportJobs tells the model (and the user) about background jobs that
// ended since the last check.
func (a *Agent) reportJobs(emit func(Event)) {
	for _, n := range a.Jobs.Finished() {
		emit(Event{Kind: EvNotice, Text: strings.SplitN(n, "\n", 2)[0]})
		a.appendHarnessText("[background job update]\n" + n)
	}
}

func (a *Agent) modeName() string {
	if a.PlanMode {
		return "plan mode"
	}
	return "read-only mode"
}

const readOnlySystem = "\n\n# Read-only\nThis session is read-only: you can read, search and run read-only tools, but nothing that changes files or runs commands is available, and asking for it will fail. Do not pretend a change was made."

const planSystem = "\n\n# Plan mode\nThe user wants a plan before any change. Explore the code as needed, then write a concrete, numbered plan: which files change and how, in what order, how each step is verified, and what could go wrong. Put it under a heading `## Plan`. The user will approve it, edit it, or ask questions; changes start only after approval."

// maxStopContinues bounds how often stop hooks may extend one turn.
const maxStopContinues = 3

// gateState tracks verification within one turn.
type gateState struct {
	rounds int
	seen   int // a.mutations when the checks last ran
}

// verifyGate runs the project's verification commands when the model ends
// a turn after changing something. A failure is fed back and the turn
// continues; a pass is reported. Checks rerun only after further changes,
// and at most verify.MaxRounds failures are sent back.
func (a *Agent) verifyGate(ctx context.Context, g *gateState, emit func(Event)) bool {
	if !a.Verify.Enabled() || a.mutations == 0 || a.mutations == g.seen {
		return false
	}
	g.seen = a.mutations
	emit(Event{Kind: EvNotice, Text: "verifying: " + strings.Join(a.Verify.Config.Commands, " · ")})
	res := a.Verify.Run(ctx)
	if ctx.Err() != nil {
		return false
	}
	a.working.Verify = res.Summary()
	if res.OK() {
		emit(Event{Kind: EvNotice, Text: res.Summary()})
		return false
	}
	g.rounds++
	if g.rounds > verify.MaxRounds {
		emit(Event{Kind: EvNotice, Text: res.Summary() + " · giving up after " + fmt.Sprint(verify.MaxRounds) + " rounds"})
		return false
	}
	emit(Event{Kind: EvNotice, Text: fmt.Sprintf("%s · sending the model back to fix it (%d/%d)", res.Summary(), g.rounds, verify.MaxRounds)})
	a.Messages = append(a.Messages, llm.HarnessMessage(llm.RoleUser, res.Feedback()))
	return true
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
	case llm.EventStatus:
		emit(Event{Kind: EvPhase, Text: e.Text})
	}
}

// editTool reports the tools AutoEdits covers: the ones that change files
// through the harness, never the shell.
func editTool(name string) bool { return name == "write_file" || name == "edit_file" }
