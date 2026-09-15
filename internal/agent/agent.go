// Package agent runs the model/tool loop: send the conversation, execute the
// tool calls the model asks for (subject to permission), feed results back,
// repeat until the model ends its turn.
package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/xmonader/janetcode/internal/accept"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/xmonader/janetcode/internal/hook"
	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/session"
	"github.com/xmonader/janetcode/internal/textfmt"
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
	// Rule is the permission rule AllowAlways would save for this call
	// (see perm.Suggest), or "" when none can be offered safely — a
	// front-end offers its r key only when it is set. Set only when the
	// call is about to ask.
	Rule string
	// Rungs is the ladder of rules AllowAlways could save for this call,
	// narrowest first (see perm.SuggestLadder), each with its own ledger
	// note; Rule is Rungs[RuleIndex].String(). A front-end may let the
	// user move along it before saving; one rung (or none) means there is
	// nothing to choose and the prompt looks exactly as it always has.
	Rungs []RuleRung
	// RuleIndex is the rung a front-end starts on: the one Suggest
	// proposes, so an answer given without moving saves what it used to.
	RuleIndex int
	// Learned notes that the ledger has seen this call's shape (see
	// perm.Suggest) approved perm.LearnAfter times with no refusal, e.g.
	// "approved 4× before, never refused". Set only when the call is
	// about to ask and nothing else already makes the prompt unusual
	// (see authorize); a front-end may offer it next to the r key.
	Learned string
	// Impact is one line describing the reach of an edit_file or
	// write_file call to an existing file: the symbols its changed lines
	// redefine, how many places reference them, which tests cover them,
	// and which verify commands will run (see package impact). Set only
	// when the call is about to ask (see Agent.Impact); "" when there is
	// nothing to say, which is most non-edit calls and every non-code
	// file.
	Impact string
}

// RuleRung is one rule the user may choose to save at a permission
// prompt, with the ledger's note about that exact rule.
type RuleRung struct {
	Rule    string
	Learned string
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

// RuleApprover is a ReasonApprover that can also name which rung of the
// call's ladder an AllowAlways answer chose (see ToolCall.Rungs). An
// empty or unknown rule means the default rung, so an approver that does
// not offer the choice need not implement this at all.
type RuleApprover interface {
	ReasonApprover
	ApproveWithRule(ctx context.Context, call ToolCall) (d Decision, reason, rule string, err error)
}

// DeniedMessage is the tool result the model sees after a denial. The user's
// reason, when given, follows it.
const DeniedMessage = "The user denied permission for this tool call. Ask before retrying or choose another approach."

// CancelledReason is the reason a front-end gives with the denial that
// answers a prompt by cancelling the whole turn. It is not a verdict on the
// call, so the permission ledger does not count it as a refusal.
const CancelledReason = "the user cancelled the turn"

// CancelledMessage is the tool result the model sees for a call whose
// permission prompt was answered by cancelling the turn: not a refusal of
// the call, so none of DeniedMessage's advice about asking before retrying.
const CancelledMessage = "The user cancelled the turn."

// ApproverFunc adapts a function to Approver.
type ApproverFunc func(ctx context.Context, call ToolCall) (Decision, error)

// Approve implements Approver.
func (f ApproverFunc) Approve(ctx context.Context, call ToolCall) (Decision, error) {
	return f(ctx, call)
}

// Agent holds one conversation and the machinery to advance it.
type Agent struct {
	// modelRouter owns which model each call goes to: the fallback
	// chain and the experimental step router (see router.go). Embedded
	// so its exported fields and methods stay on Agent.
	modelRouter
	// contextManager owns the request context: what fills the window,
	// how big it is believed to be, and what happens when it fills up
	// (see contextmgr.go). Embedded for the same reason.
	contextManager
	// checkpoints owns undo, redo and the provenance record (see
	// checkpoints.go). Embedded for the same reason.
	checkpoints
	// permissions owns what a tool call must satisfy before it runs
	// (see permissions.go). Embedded for the same reason.
	permissions
	// turnMonitor watches the running turn: cost, counters, and the
	// three things that notice a turn going wrong (see turnmon.go).
	// Embedded for the same reason.
	turnMonitor

	// Model, MaxTokens, MaxIterations, CompactAt, AutoApprove, AutoEdits,
	// AutoMode, ReadOnly, PlanMode, ProofNotes, Rules and Pins are filled
	// in when the agent is built. Once a turn may be running they change
	// and are read only through settings.go (Settings, UpdateSettings,
	// SetPlanMode, RulesSnapshot, AddRule, PinsSnapshot, Pin), because a
	// front-end changes them while the turn goroutine reads them.
	Provider llm.Provider
	Tools    *tool.Registry
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
	// AutoMode skips the Approver for in-workspace file edits and for
	// shell commands that only read (see shell.SafeRead). Ordinary bash
	// and other mutating tools still ask. Distinct from AutoApprove
	// (everything) and AutoEdits (file tools only). Guard still forces
	// an ask for commands that leave the workspace or touch janetcode's
	// own settings.
	AutoMode bool
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
	// VerifyUntrusted is set at build time (see main.go) when the
	// project's .janetcode/verify.json defines checks but they are not
	// trusted (see trust.VerifyKey), so Verify itself is left nil.
	// undo_apply.go's reportChecks uses it to say why no checks ran rather
	// than staying silent, without re-reading the trust store itself.
	VerifyUntrusted bool
	// Root is the directory the session works in. Paths shown to the user
	// and the model by /undo about are relative to it; empty means the
	// process working directory, which is only right for a front-end
	// started in the project (the web UI may not be).
	Root string
	// ProofNotes gates the automatic side of internal/proof: when a bash
	// call this session runs looks like `git commit` and HEAD actually
	// moves, evidence for this session's part of that commit is attached
	// as a git note (see proof.go). It does not gate /proof or --proof,
	// which attach on request regardless. Config default is on.
	ProofNotes bool
	// ReadOnly hides mutating tools from the model and refuses them if
	// called anyway. PlanMode does the same and asks for a plan.
	ReadOnly bool
	PlanMode bool
	// Jobs, when set, is polled before each model call so finished
	// background commands are reported to the model as they complete.
	Jobs *tool.Jobs
	// PlanCheck, when set, is given the text the model wrote before a
	// batch of tool calls (its plan) and returns a note about names in
	// it the repository does not have, or "".
	PlanCheck func(ctx context.Context, text string) string
	// Diagnose, when set, is called with the path a file tool just wrote
	// and returns a note (language-server diagnostics) appended to the
	// tool result, or "".
	Diagnose func(ctx context.Context, path string) string
	// settingsMu guards the fields Settings mirrors, Rules and Pins: a
	// front-end changes them while the turn goroutine and subagents read
	// them (see settings.go).
	settingsMu sync.Mutex
	// mutations counts successful mutating tool calls this turn.
	mutations int
	// proofCommits are the commits this turn attached a proof note to
	// (see checkCommitAttach), refreshed once the turn's gates have run.
	proofCommits []string
	// Debug, when set, receives a timestamped line per event and model call.
	Debug io.Writer

	// Messages is the conversation history. It is only ever appended to and
	// always ends in a state a provider will accept.
	Messages []llm.Message
	// Price, when set, lets the UI estimate spend for providers that do not
	// report cost.
	Price *llm.Price
	// Log, when set, is the append-only trajectory of this conversation.
	// Resume reconstructs counts by folding it. Nil means in-memory stats only.
	Log          session.Log
	turnN, stepN int
	// Archive, when set, keeps full tool outputs longer than InlineOutput
	// on disk so the model can page through them with expand_output.
	Archive *Archive
	// Hooks, when set, runs user commands around tool calls, compaction
	// and turn end (see package hook). Nil means no hooks.
	Hooks *hook.Runner
	// Cron is the session-scoped scheduler; jobs die when the session ends.
	Cron *Cron
	// accept is the /accept mode (see package accept and acceptgate.go),
	// nil until the mode is first used this session.
	accept *accept.Mode
	// Forked is true when this agent is a fork of a parent conversation
	// and must not spawn another fork.
	Forked bool
	// Prepare, when set, may add blocks to a user turn before it is sent
	// (skills attach themselves this way). The returned note, if any, is
	// emitted as a notice.
	Prepare func(blocks []llm.Block) ([]llm.Block, string)

	mutated bool // a mutating tool ran successfully this turn

	// callIDs is every tool-call id handed out this conversation, so a
	// provider that repeats or omits one cannot collide with it.
	callIDs map[string]bool
	msgMu   sync.Mutex // guards Messages writes against off-turn readers (see MessagesSnapshot)
	steerMu sync.Mutex // guards steer, written from the UI goroutine
	steer   []string   // notes for the running turn (see Steer)
	harness []string   // harness text for the running turn (path rules)
	// tools is this turn's tool list, held so that a server registering
	// mid-turn cannot rewrite the cached prefix. Cleared at each turn.
	tools []llm.ToolDef

	// emitNow is the current turn's event sink. A tool that dispatches
	// further tool calls of its own — a script does — needs it to show
	// them as they happen, and it is not on the Tool interface.
	emitMu  sync.Mutex
	emitNow func(Event)
}

// beginTurn opens a logged turn whose user message is blocks: it appends
// the message, numbers the turn, logs its start, opens the undo checkpoint
// and resets the per-turn mutation counters. RunBlocks and RunRecipe both
// open their turns here; the caller defers endTurn.
func (a *Agent) beginTurn(ctx context.Context, blocks ...llm.Block) {
	// The checkpoint remembers the conversation as it stood before this
	// turn's prompt, so an undo can take the whole turn out, prompt
	// included; a prompt left behind reads as a request to do it again.
	before := len(a.Messages)
	merged := a.appendUserBlocks(blocks...)
	a.turnN++
	a.stepN = 0
	if a.turnN == 1 {
		a.recordStart(ctx)
	}
	start := session.Event{Type: session.TypeTurnStart, Turn: a.turnN}
	if phase, on := a.acceptPhase(); on {
		start.Reason = session.TurnStartAccept + string(phase)
	}
	a.logEvent(start)
	a.logUserMessage(merged)
	a.cp.beginTurn(a.turnN, before)
	a.mutated, a.mutations = false, 0
	a.proofCommits = nil
}

// ErrMaxIterations is returned when a turn exceeds MaxIterations model calls.
var ErrMaxIterations = errors.New("turn exceeded maximum tool iterations")

// Run sends userText and drives the loop until the model ends its turn or
// an error occurs. History stays consistent on every return path.
func (a *Agent) Run(ctx context.Context, userText string, emit func(Event)) error {
	return a.RunBlocks(ctx, []llm.Block{{Type: llm.BlockText, Text: userText}}, emit)
}

// RunBlocks is Run for a user turn made of several blocks (text plus
// images).
func (a *Agent) RunBlocks(ctx context.Context, blocks []llm.Block, emit func(Event)) error {
	emit = serialize(a.ledgered(a.traced(emit)))
	if a.Settings().Provider == nil {
		return errors.New("no provider")
	}
	a.stats.turn()
	a.startTurn()
	a.tools = nil // pick up any tool that registered since the last turn
	a.coach.noSyntax = a.Off["syntax"]
	if a.Tools == nil {
		a.Tools = tool.NewRegistry()
	}
	maxIter := a.Settings().MaxIterations
	if maxIter <= 0 {
		maxIter = 50
	}
	if a.Prepare != nil {
		var note string
		own := len(blocks)
		blocks, note = a.Prepare(blocks)
		var repeated int
		blocks, repeated = a.dropRepeatedAttachments(blocks, own)
		if note != "" {
			emit(Event{Kind: EvNotice, Text: note})
		}
		if repeated > 0 {
			emit(Event{Kind: EvNotice, Text: fmt.Sprintf(
				"%s of attachment was already in the context word for word and was not sent again", textfmt.Tokens(repeated))})
		}
	}
	a.beginTurn(ctx, blocks...)
	defer a.endTurn(ctx)
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
	a.modelRouter.newTurn()
	a.integ.userText = ""
	for _, b := range blocks {
		if b.Type == llm.BlockText && b.Text != "" {
			a.integ.userText = b.Text
			break
		}
	}

	compactAt := a.raiseCompactFloor(emit)
	stops, needs := 0, 0
	corrections := 0
	gate := gateState{}
	acceptGate := gateState{}
	acceptNudged := false
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
				a.UpdateSettings(func(s *Settings) { s.CompactAt = compactAt })
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
		a.appendMessage(resp.Message)
		a.integ.lastLen = len(a.Messages)
		a.addUsage(resp.Usage)
		msg := resp.Message
		u := resp.Usage
		a.logEvent(session.Event{Type: session.TypeAssistant, Turn: a.turnN, Step: a.stepN, Surface: true, Message: &msg, Usage: &u, Model: a.lastCallModel})
		emit(Event{Kind: EvTextBlockDone})
		// The ceiling is asked about here, while the call just paid for
		// is in the totals and before its tools are run.
		if spent, over := a.overBudget(); over {
			return a.stopForBudget(spent, resp.Message.ToolUses(), emit)
		}
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
				// The notice is logged by the ledger wrapper around emit.
				emit(Event{Kind: EvNotice, Text: "turn stopped: " + a.loops.reason})
				a.logEvent(session.Event{Type: session.TypeTurnEnd, Turn: a.turnN, Reason: "stuck"})
				return ErrStuck
			}
			repeatNote = note
			if note != "" {
				a.stepRoute.taint("a loop nudge fired this turn")
			}
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
				a.appendMessage(tr)
				a.logEvent(session.Event{Type: session.TypeToolResult, Turn: a.turnN, Step: a.stepN, Surface: true, Message: &tr})
				a.logEvent(session.Event{Type: session.TypeStepEnd, Turn: a.turnN, Step: a.stepN})
				a.stepRoute.prevMechanical = false
				continue
			}
		}
		if resp.StopReason != llm.StopToolUse {
			// The model ended its turn instead of making tool calls: not
			// the mechanical streak the router looks for, whatever sends
			// it back for another call below.
			a.stepRoute.prevMechanical = false
			if more := a.faultNeeds(resp.Message.Text(), &needs, emit); more {
				continue
			}
			if more := a.stopHook(ctx, resp.Message.Text(), &stops, emit); more {
				continue
			}
			if more := a.acceptCapture(resp.Message.Text(), &acceptNudged, emit); more {
				continue
			}
			if more := !a.Off["verify"] && a.verifyGate(ctx, &gate, emit); more {
				a.stepRoute.taint("a verification check failed and was sent back")
				continue
			}
			if more := a.acceptVerifyGate(ctx, &acceptGate, emit); more {
				a.stepRoute.taint("an acceptance check failed and was sent back")
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
			a.refreshProofNotes(ctx, emit)
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
		a.stepRoute.prevMechanical = allMechanicalStep(uses, results)
		if repeatNote != "" && len(results) > 0 {
			results[len(results)-1].Text += repeatNote
			emit(Event{Kind: EvNotice, Text: strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(repeatNote), "[harness]"))})
		}
		if planNote != "" && len(results) > 0 {
			results[len(results)-1].Text += "\n\n[harness] " + planNote
			emit(Event{Kind: EvNotice, Text: planNote})
		}
		tr := llm.ToolResultMessage(results...)
		a.appendMessage(tr)
		a.logEvent(session.Event{Type: session.TypeToolResult, Turn: a.turnN, Step: a.stepN, Surface: true, Message: &tr})
		a.logEvent(session.Event{Type: session.TypeStepEnd, Turn: a.turnN, Step: a.stepN})
		if err != nil {
			return err
		}
		if a.stuck {
			a.stuck = false
			emit(Event{Kind: EvNotice, Text: "turn stopped: " + a.loops.reason})
			a.logEvent(session.Event{Type: session.TypeTurnEnd, Turn: a.turnN, Reason: "stuck"})
			return ErrStuck
		}
		// Tools can cost money too (a subagent runs its own turns), and
		// the next model call is about to be made with a history this
		// step just grew. Ask again before paying for it.
		if spent, over := a.overBudget(); over {
			return a.stopForBudget(spent, nil, emit)
		}
	}
	return ErrMaxIterations
}

// CompactThreshold is the effective token count at which compaction runs,
// or 0 when disabled.
func (a *Agent) CompactThreshold() int {
	switch s := a.Settings(); {
	case s.CompactAt < 0:
		return 0
	case s.CompactAt > 0:
		return s.CompactAt
	case s.ContextWindow > 0:
		t := s.ContextWindow * 3 / 4
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
