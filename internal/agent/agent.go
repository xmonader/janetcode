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
	"sync/atomic"
	"time"

	"github.com/xmonader/janetcode/internal/hook"
	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/plan"
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
	// turnState is what belongs to the turn in progress and nothing
	// else: whether one is running, what it changed, what is queued for
	// it, its frozen tool list and its event sink (see turnstate.go).
	// Embedded for the same reason.
	turnState

	// Model, MaxTokens, Effort, MaxIterations, CompactAt, AutoApprove, AutoEdits,
	// AutoMode, ReadOnly, PlanMode, ProofNotes, Rules and Pins are filled
	// in when the agent is built. Once a turn may be running they change
	// and are read only through settings.go (Settings, UpdateSettings,
	// SetPlanMode, RulesSnapshot, AddRule, PinsSnapshot, Pin), because a
	// front-end changes them while the turn goroutine reads them.
	Provider llm.Provider
	Tools    *tool.Registry
	System   string
	Model    string
	// AdvisorMode is how the harness forces advisor consults:
	// AdvisorOff (the default) or AdvisorAlways. See advisorforce.go.
	AdvisorMode string
	// AdvisorMaxConsults caps forced consults per turn; 0 uses
	// advisorForcedCap.
	AdvisorMaxConsults int
	// TurnStartStyle is what the turn-start consult asks for:
	// TurnStartPlan (empty, the default) or TurnStartOrient. See
	// orient.go; orient runs even with AdvisorMode off.
	TurnStartStyle string
	// LikelyFiles, when set, names the files a request most probably
	// concerns, relative to the returned root, for the orient consult to
	// read. The composition root wires it to the repository map.
	LikelyFiles func(ctx context.Context, request string) (root string, paths []string)
	// VerifyBaseline runs the verify commands at turn start, before the
	// first change, so the gate can tell a failure the turn caused from
	// one that was already there. See baseline.go.
	VerifyBaseline bool
	// baseline is this turn's pre-edit verification run.
	baseline *baselineRun
	// ElideChecks runs the project's check after a step whose last change
	// was an unchecked edit and puts the result beside the step's tool
	// results, saving the round trip the model would spend asking for it.
	// Opt-in (config elide_checks). See elide.go.
	ElideChecks bool
	// elide is what round-trip elision carries between steps.
	elide elideState
	// StrongModel is the strong slot of the cheap/default/strong trio
	// (RouteCheap / Model / StrongModel). Empty means there is no tier to
	// escalate to and the cascade cannot fire.
	StrongModel string
	// EscalateAfter is how many verification rounds the session's own
	// model may fail before the rest of the turn moves to StrongModel.
	// Zero is off, which is the default: a cascade changes what a run
	// costs, so it is never on without being asked for.
	EscalateAfter int
	// escalateState is the cascade's per-session state (see escalate.go).
	escalateState escalateState

	// AdvisorModel, when set, is the model advisor consults. Empty uses
	// the session model. The tool itself is deferred.
	AdvisorModel string
	// Files is the I/O cache shared with subagents. Nil disables it.
	Files *tool.IOCache
	// MaxTokens per model call. Zero lets the provider choose.
	MaxTokens int
	// Effort is the reasoning tier every model call asks for, one of
	// llm.EffortTiers; empty lets the provider choose. It rides on each
	// llm.Request, so /effort changes what the very next call asks for.
	Effort string
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
	// Off disables named behaviours for ablation runs. The names
	// Switches reports are the list; unknown names are ignored. A
	// switch that is off restores the pre-feature behaviour, not a
	// degraded one: accept and recipe commands refuse, proof does not
	// attach notes, impact and protected-path prompting stay quiet,
	// the permission ladder collapses to Suggest's single rule,
	// routing never leaves Model, undo-about uses keyword overlap
	// instead of the model, and mermaid is drawn as source.
	Off map[string]bool
	// PlanBudget, when positive, is how many characters of reasoning a
	// reply may stream before it writes text or calls a tool; past it the
	// call ends and the model is told to act, up to maxPlanCuts times a
	// turn (planbudget.go).
	PlanBudget int
	// SpecReview, when set, sends a turn that changed files and passed its
	// checks back once to hold the change against the task (specreview.go).
	SpecReview bool
	// TestGuard, when set, refuses the first write to each test file
	// (testguard.go).
	TestGuard   bool
	testGuarded map[string]bool
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
	// Decisions, when set, is the optional decision provider (TypeSafe's
	// Jev, see internal/jev): typed answers to narrow questions around
	// the loop. It is advisory by contract — every consumer fails open
	// to the pre-feature behaviour on any error, and nothing here can
	// permit, refuse or edit. Off["decisions"] disables the calls
	// without dropping the field.
	Decisions DecisionProvider
	// decisionCircuitSeen keeps the circuit-open notice to one per
	// session. Written and read only from the turn goroutine, which is
	// the only place triageFailure runs.
	decisionCircuitSeen bool
	// DecisionsTriage enables the verify-failure triage consumer. The
	// Decisions field being set only means a client is configured; each
	// consumer is opted into separately (config decisions.triage).
	DecisionsTriage bool
	// DecisionsIntake enables the log-only intake classification of the
	// session's first prompt (config decisions.intake, Phase 3a). It
	// reaches no behaviour: see jevintake.go.
	DecisionsIntake bool
	// DecisionsIntakeRoute enables routing on that classification (config
	// decisions.intake_route): a small read-only first prompt may start on
	// route_cheap. It needs DecisionsIntake to have anything to read, and
	// route_cheap to have anywhere to go. See jevintakeroute.go.
	DecisionsIntakeRoute bool
	// StaleCheckNudge sends a turn back once when its checks passed, it
	// edited afterwards and it then claimed to be done (see
	// TurnOutcome.StaleCheck). Off by default: it changes what a turn
	// does, and every eval baseline here was measured without it.
	StaleCheckNudge bool
	// EffortControl lets the effort controller set each call's reasoning
	// tier from the turn's own signals, at or below Effort (effortctl.go).
	// Off by default: it changes what every call asks for, and it acts
	// only on a provider whose prompt cache does not key on the tier.
	EffortControl bool
	// DecisionsSessionOutcome enables the session-end outcome
	// classification (config decisions.session_outcome, item 9). It is
	// log-only: nothing in the harness changes because of the answer,
	// though sleep consolidation may read it. See jevoutcome.go.
	DecisionsSessionOutcome bool
	// intakeRoute is what the routing consumer made of the classification.
	// Zero (no route, no reason) whenever routing is off or the answer was
	// late. Written once per session on the turn goroutine, together with
	// the turn number it was made for: a route is good for that turn and
	// no other.
	intakeRoute     IntakeRoute
	intakeRouteTurn int
	// intakeCh carries that classification from the goroutine making the
	// call to the turn goroutine that logs it, and intakeDone says the
	// session has had its one classification. Both are touched only from
	// the turn goroutine.
	intakeCh       chan intakeOutcome
	intakeDone     bool
	intakeDeadline time.Time
	// Root is the directory the session works in. Paths shown to the user
	// and the model by /undo about are relative to it; empty means the
	// process working directory, which is only right for a front-end
	// started in the project (the web UI may not be). It is set once at
	// build time and thereafter only through SetRoot, which /cd calls
	// while a turn may be reading it; read it back with RootDir.
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
	// dry is the dry-run state (see dryrun.go), made on first use under
	// dryOnce; a fork uses its UndoOwner's.
	dry     *dryRun
	dryOnce sync.Once
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
	// language is the language the model answers in (language.go),
	// guarded by settingsMu.
	language string
	// offToggled holds the switches a front-end flipped mid-session
	// (ToggleOff), guarded by settingsMu. They are kept apart from Off
	// because Off is read without a lock all over the turn loop, and a
	// map written while another goroutine reads it is a race the runtime
	// can kill the process over.
	offToggled map[string]bool
	// featMu guards the feature knobs a front-end changes mid-session --
	// the cascade's round count, the forced advisor, the stale-check
	// nudge, the advisor slot, the decision provider and its consumers.
	// See features.go for the setters and getters that hold it.
	featMu sync.RWMutex
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
	// turnEnded is whether the running turn has logged its turn/end; a
	// turn that returns without one gets it on the way out, so only a
	// crash leaves a turn open on the log (see durable.go).
	turnEnded atomic.Bool
	// resume, when set, is the turn this one continues: the next turn
	// opens with no new user message (see ContinueInterrupted).
	resume *resumeState
	// resumeCalls and resumeOf are the continued turn's unanswered calls
	// and its number, between beginTurn and openTurn's recoverCalls.
	resumeCalls []session.PendingCall
	resumeOf    int
	// sessHooks tracks session_start and session_end for this session.
	sessHooks sessionHooks
	// vision is the vision_model slot and the image descriptions it made
	// (see vision.go).
	vision visionState
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
	// UndoOwner, when set, is the agent whose checkpoints this one's
	// changes belong to. A fork edits the parent's own workspace while
	// having no undo stack of its own: without this, files a background
	// fork wrote were absent from the session's undo manifest and /undo
	// could not put them back. Nil for an ordinary agent.
	UndoOwner *Agent
	// planComments are the remarks the user has left on the plan file and
	// not yet sent (see plancmd.go). The plan itself is never held here:
	// it lives in .janetcode/plan.md and is read back from there.
	planComments plan.Comments
	// Prepare, when set, may add blocks to a user turn before it is sent
	// (skills attach themselves this way). The returned note, if any, is
	// emitted as a notice. It is given the turn's own context: what it
	// does can reach a git command, an index rebuild or an embedding
	// call, and ^C has to stop those with the rest of the turn rather
	// than leave them running on a context of their own.
	Prepare func(ctx context.Context, blocks []llm.Block) ([]llm.Block, string)

	// callIDs is every tool-call id handed out this conversation, so a
	// provider that repeats or omits one cannot collide with it.
	callIDs map[string]bool
	msgMu   sync.Mutex // guards Messages writes against off-turn readers (see MessagesSnapshot)
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
	resume := a.resume
	a.resume = nil
	merged := false
	if resume == nil {
		merged = a.appendUserBlocks(blocks...)
	}
	a.turnN++
	a.stepN = 0
	a.turnEnded.Store(false)
	if a.turnN == 1 {
		a.recordStart(ctx)
	}
	start := session.Event{Type: session.TypeTurnStart, Turn: a.turnN, Extra: session.TurnLogsEnd}
	if phase, on := a.acceptPhase(); on {
		start.Reason = session.TurnStartAccept + string(phase)
	}
	if resume != nil {
		start.Reason = fmt.Sprintf("resume:%d", resume.turn)
	}
	a.logEvent(start)
	if resume == nil {
		a.logUserMessage(merged)
	} else {
		a.logResumedResults(resume)
	}
	a.cp.beginTurn(a.turnN, before)
	a.turnState.begin()
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
//
// The loop below is the whole harness in one place: one model call, then
// either the turn ends (finishTurn, which may send it back for another
// call) or its tool calls run (runToolStep). Everything each of those
// needs from the step before it is passed explicitly, and the counters
// that have to survive the iterations live in turnGates, so the loop
// itself holds no state of its own beyond the compaction threshold.
func (a *Agent) RunBlocks(ctx context.Context, blocks []llm.Block, emit func(Event)) (err error) {
	emit = serialize(a.ledgered(a.traced(emit)))
	if a.Settings().Provider == nil {
		return errors.New("no provider")
	}
	ctx, endEgress := a.egressTurn(ctx, blocks)
	defer endEgress()
	// A continuation has no prompt of its own: the prompt hooks saw the
	// dead turn's, and nothing new is submitted.
	if a.resume == nil {
		blocks, err = a.promptHooks(ctx, blocks, emit)
		if err != nil {
			return err
		}
	}
	defer func() { a.turnEndHooks(ctx, err) }()
	maxIter := a.openTurn(ctx, blocks, emit)
	turnBegan := time.Now()
	defer func() { a.stats.turnTook(time.Since(turnBegan)) }()
	defer a.endTurn(ctx)
	defer a.endBaseline()
	// Every way out of a turn but a crash leaves a turn/end on the log,
	// so a turn left open there is one the session died in (durable.go).
	defer func() {
		if a.turnEnded.Load() {
			return
		}
		reason := "error"
		switch {
		case errors.Is(err, context.Canceled):
			reason = "cancelled"
		case errors.Is(err, ErrMaxIterations):
			reason = "max-iterations"
		case err != nil:
			reason = "error: " + textfmt.TruncateTo(err.Error(), 200)
		}
		a.logEvent(session.Event{Type: session.TypeTurnEnd, Turn: a.turnN, Reason: reason})
	}()
	// Collected here, after the turn's work and before the turn closes:
	// by now the first model call has long outlived the decision call's
	// deadline, and an answer that still has not arrived is dropped
	// rather than waited for.
	defer a.drainIntake()
	// A note typed with ctrl+s reaches the model before the turn's next
	// call. A turn that ends with its final answer has no next call, so
	// the note stays queued for the turn after this one. Say so, rather
	// than letting the user watch what they typed disappear.
	defer func() {
		if n := a.pendingSteer(); n > 0 {
			emit(Event{Kind: EvNotice, Text: fmt.Sprintf(
				"%d steering note(s) arrived after the turn's last model call; they go with your next message",
				n)})
		}
	}()

	compactAt := a.raiseCompactFloor(emit)
	// A cascade is per turn: the next request starts on the cheap tier
	// again, which is the whole point of letting it try first.
	a.endTurnEscalation()
	defer a.endTurnEscalation()
	var gates turnGates
	for iter := 0; iter < maxIter; iter++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		a.escalateNearCap(iter, maxIter, emit)
		// Recomputed every step: it also publishes the estimate the status
		// bar reads while the turn runs.
		if err := a.maybeCompact(ctx, compactAt, emit); err != nil {
			return err
		}
		a.checkIntegrity(emit)
		a.reportJobs(emit)
		a.reportForks(emit)
		a.deliverSteering(emit)
		a.stepN++
		a.logEvent(session.Event{Type: session.TypeStepStart, Turn: a.turnN, Step: a.stepN})
		resp, err := a.callStep(ctx, &compactAt, emit)
		if err != nil {
			// Nothing appended: the trailing user message stays and the next
			// Run merges into it.
			return err
		}
		repeatNote, err := a.recordReply(resp, emit)
		if err != nil {
			return err
		}
		a.noteCut(resp)
		a.noteEffort(replyEffortSignal(resp, a.planCutNow))
		if a.refuseTruncatedCalls(resp, emit) {
			continue
		}
		if resp.StopReason != llm.StopToolUse {
			// A reply can carry tool calls and still stop for another
			// reason (a refusal mid-call, a paused turn). They are not
			// run, but they are in the history and must be answered, or
			// the next request is refused for an unpaired tool_use.
			a.answerUnrun(resp.Message.ToolUses(), fmt.Sprintf("the reply stopped (%s) instead of asking for tools", resp.StopReason), emit)
			if a.continueCutReply(resp, &gates, emit) {
				continue
			}
			if a.finishTurn(ctx, resp, &gates, emit) {
				return nil
			}
			continue
		}
		if done, err := a.runToolStep(ctx, resp, repeatNote, emit); done || err != nil {
			return err
		}
	}
	return ErrMaxIterations
}

// turnGates is the state the turn loop has to carry across its
// iterations: how many times each gate has already sent the model back,
// so none of them can send it back for ever.
type turnGates struct {
	stops, needs, corrections int
	verify, accept            gateState
	acceptNudged, todoNudged  bool
	diagRounds, cutReplies    int
	specReviewed              bool
}

// openTurn is everything that has to happen once, before the first model
// call: the turn is counted, the preparation hooks attach what they
// attach, the turn is opened on the log and the undo checkpoint, and the
// per-turn watchers are reset. It returns the iteration ceiling.
func (a *Agent) openTurn(ctx context.Context, blocks []llm.Block, emit func(Event)) int {
	a.stats.turn()
	a.startTurn()
	// First, so the checks run beside everything else that prepares the
	// turn; nothing has changed the tree yet.
	a.startBaseline(ctx)
	a.resetDiagnostics()
	a.resetElide()
	a.outBoost = 0
	a.planCuts, a.planCutNow = 0, false
	a.effort.start(a.Settings().Effort)
	a.reviewSteps = -1
	a.tools = nil // pick up any tool that registered since the last turn
	a.coach.noSyntax = a.Off["syntax"]
	if a.Tools == nil {
		a.Tools = tool.NewRegistry()
	}
	maxIter := a.Settings().MaxIterations
	if maxIter <= 0 {
		maxIter = 50
	}
	// Fired before Prepare so the classification runs beside the turn's
	// preparation rather than after it, and collected at the end of the
	// turn: the turn never waits on it.
	if a.turnN == 0 {
		a.startIntake(ctx, firstText(blocks))
	}
	if a.resume == nil {
		blocks = a.prepareBlocks(ctx, blocks, emit)
	}
	a.beginTurn(ctx, blocks...)
	a.recoverCalls(ctx, emit)
	// The first forced consult, after the request is in the history so
	// the advisor sees the same transcript the executor will.
	a.adviseAtTurnStart(ctx, firstText(blocks), emit)
	a.loops.reset()
	// A stop the detector called in a step that then ended on an error
	// was never consumed; it belongs to that turn, not this one.
	a.stuck = false
	a.turnStart, a.warned = time.Now(), false
	a.modelRouter.newTurn()
	a.integ.userText = firstText(blocks)
	// Last, so the classification has had the whole of Prepare to arrive
	// and the wait below is usually no wait at all. With the routing
	// consumer off this returns immediately and nothing is collected.
	a.awaitIntakeRouting(ctx)
	return maxIter
}

// firstText is the turn's user text: the first non-empty text block,
// ignoring the images a multimodal turn carries beside it.
func firstText(blocks []llm.Block) string {
	for _, b := range blocks {
		if b.Type == llm.BlockText && b.Text != "" {
			return b.Text
		}
	}
	return ""
}

// prepareBlocks runs the Prepare hook over the turn's blocks — skills,
// memory and the repository map attach themselves there — and drops
// anything it attached that is already in the context word for word.
func (a *Agent) prepareBlocks(ctx context.Context, blocks []llm.Block, emit func(Event)) []llm.Block {
	if a.Prepare == nil {
		return blocks
	}
	own := len(blocks)
	blocks, note := a.Prepare(ctx, blocks)
	blocks, repeated := a.dropRepeatedAttachments(blocks, own)
	if note != "" {
		emit(Event{Kind: EvNotice, Text: note})
	}
	if repeated > 0 {
		emit(Event{Kind: EvNotice, Text: fmt.Sprintf(
			"%s of attachment was already in the context word for word and was not sent again", textfmt.Tokens(repeated))})
	}
	return blocks
}

// callStep makes the step's model call, recovering once from a context
// overflow the token estimate did not see coming: the history is
// summarised and the call retried, and when the provider's error names
// the model's real limit, compactAt comes down for the rest of the
// session.
func (a *Agent) callStep(ctx context.Context, compactAt *int, emit func(Event)) (*llm.Response, error) {
	resp, err := a.call(ctx, emit)
	// A raised output limit the server refuses goes back to the configured
	// one, which it has always accepted, rather than ending the turn.
	var se llm.StatusError
	if err != nil && a.outBoost > 0 && errors.As(err, &se) && se.HTTPStatus() == 400 {
		a.outBoost = 0
		resp, err = a.call(ctx, emit)
	}
	if err == nil || !isContextOverflow(err) || *compactAt <= 0 {
		return resp, err
	}
	if w := windowFromError(err); w > 0 && w*3/4 < *compactAt {
		*compactAt = w * 3 / 4
		a.UpdateSettings(func(s *Settings) { s.CompactAt = *compactAt })
	}
	if cerr := a.Compact(ctx, emit); cerr != nil && !errors.Is(cerr, ErrNothingToCompact) {
		return nil, fmt.Errorf("%w (compaction also failed: %v)", err, cerr)
	}
	return a.call(ctx, emit)
}

// recordReply puts the model's reply into the conversation and asks the
// two questions that can end the turn before its tool calls are even
// looked at: has the spend ceiling been reached, and is the model writing
// the same message over and over. The note it returns rides with this
// step's tool results, because nothing may come between a tool call and
// its answer.
func (a *Agent) recordReply(resp *llm.Response, emit func(Event)) (repeatNote string, err error) {
	// A model that wrote its call as prose meant to call the tool;
	// reading it back costs nothing and saves the turn.
	a.recoverToolCalls(resp, emit)
	// Every call gets an id of its own, whatever the server numbered
	// it: the transcript, the permission prompt and the log all key
	// by it. The reply still goes back under the server's own id.
	if a.callIDs == nil {
		// The first reply of this agent: the history it was given
		// before it — a --resume, a fork's inheritance — counts.
		a.callIDs = map[string]bool{}
		noteCallIDs(a.Messages, a.callIDs)
	}
	normalizeCallIDs(&resp.Message, a.callIDs, a.turnN, a.stepN)
	a.lastInputTokens = resp.Usage.InputTokens + resp.Usage.CacheReadTokens + resp.Usage.CacheWriteTokens
	a.lastInputMsgs = len(a.Messages)
	a.appendMessage(resp.Message)
	a.integ.lastLen = len(a.Messages)
	a.addUsage(a.lastCallModel, resp.Usage)
	msg := resp.Message
	u := resp.Usage
	served, mismatch := a.servedForLog()
	a.logEvent(session.Event{Type: session.TypeAssistant, Turn: a.turnN, Step: a.stepN, Surface: true,
		Message: &msg, Usage: &u, Model: a.lastCallModel, Served: served, Mismatch: mismatch,
		Slot: a.lastCallSlot, Provider: a.lastCallProvider, Route: a.lastCallRoute, RouteWhy: a.lastCallWhy,
		TTFTMs: a.lastTTFT.Milliseconds(), DecodeMs: a.lastDecode.Milliseconds(), Effort: a.lastCallEffort})
	emit(Event{Kind: EvTextBlockDone})
	// The ceiling is asked about here, while the call just paid for
	// is in the totals and before its tools are run.
	if spent, over := a.overBudget(); over {
		return "", a.stopForBudget(spent, resp.Message.ToolUses(), emit)
	}
	if used, over := a.overToolLimit(resp.Message.ToolUses()); over {
		return "", a.stopForToolLimit(used, resp.Message.ToolUses(), emit)
	}
	// A model that keeps writing the same message has stopped reading
	// its own results; the ladder is the same as for a repeated call.
	if a.Off["loops"] {
		return "", nil
	}
	a.loopMu.Lock()
	note, stop := a.loops.recordText(resp.Message.Text())
	a.loopMu.Unlock()
	if stop {
		// The notice is logged by the ledger wrapper around emit.
		// The reply is in the history already, and its calls are
		// answered or the next turn cannot be sent.
		a.answerUnrun(resp.Message.ToolUses(), "the turn stopped because the model kept repeating itself", emit)
		emit(Event{Kind: EvNotice, Text: "turn stopped: " + a.loops.reason})
		a.logEvent(session.Event{Type: session.TypeTurnEnd, Turn: a.turnN, Reason: "stuck"})
		return "", ErrStuck
	}
	if note != "" {
		a.stepRoute.taint("a loop nudge fired this turn")
	}
	return note, nil
}

// refuseTruncatedCalls answers, without running, the tool calls of a
// response cut off at the output limit: their arguments may have been
// truncated mid-JSON and then repaired into something that parses and
// validates but is missing content — a half-written file reported as a
// success. Each gets a result, so the history stays paired and the model
// knows to reissue them. It reports whether it did so, in which case the
// step is over.
func (a *Agent) refuseTruncatedCalls(resp *llm.Response, emit func(Event)) bool {
	if resp.StopReason != llm.StopMaxTokens {
		return false
	}
	uses := resp.Message.ToolUses()
	if len(uses) == 0 {
		return false
	}
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
	return true
}

// maxOutputBoost is the most the output limit grows to within a turn.
const maxOutputBoost = 65536

// maxTokens is the output limit for the next call: the configured one,
// doubled for each reply of this turn that hit it, up to maxOutputBoost. A
// reply cut at the limit is thrown away — its tool calls are refused as
// possibly truncated, its plan is written again — so the call after it
// gets room to finish. The provider clamps to what the model serves.
func (a *Agent) maxTokens() int {
	m := a.Settings().MaxTokens
	if m <= 0 {
		return m
	}
	for i := 0; i < a.outBoost && m < maxOutputBoost; i++ {
		m *= 2
	}
	return min(m, max(maxOutputBoost, a.Settings().MaxTokens))
}

// noteCut grows the output limit for the rest of the turn after a reply
// that hit it.
func (a *Agent) noteCut(resp *llm.Response) {
	if a.planCutNow {
		// Not the output limit: the plan budget ended it (planbudget.go).
		return
	}
	if resp.StopReason == llm.StopMaxTokens && a.maxTokens() < maxOutputBoost {
		a.outBoost++
	}
}

// maxCutReplies bounds how often one turn is sent back for a reply the
// output limit cut off.
const maxCutReplies = 2

// continueCutReply sends the model back when its reply hit the output
// limit without calling a tool. Such a reply is not an answer: usually the
// model spent the whole budget planning in its reasoning (a whole small
// repository in view invites designing everything up front), and ending
// the turn there reported an empty result as a success. It reports whether
// the turn goes on.
func (a *Agent) continueCutReply(resp *llm.Response, g *turnGates, emit func(Event)) bool {
	if a.planCutNow {
		// The plan budget's cuts have a bound of their own.
		a.planCutNow = false
		emit(Event{Kind: EvNotice, Text: "the reply reasoned past the plan budget before it acted; asking the model to act"})
		a.appendHarnessText(planBudgetNudge)
		return true
	}
	if resp.StopReason != llm.StopMaxTokens || len(resp.Message.ToolUses()) > 0 || g.cutReplies >= maxCutReplies {
		return false
	}
	g.cutReplies++
	emit(Event{Kind: EvNotice, Text: "the reply hit the output limit before it acted; asking the model to go on"})
	a.appendHarnessText("[harness] Your last reply was cut off at the output token limit before it called a tool or finished. If you were planning, stop: take the next step now with a tool call and keep your reasoning short. If you were writing the answer, continue from where it stopped.")
	return true
}

// finishTurn runs the gates a model that says it is done has to get past:
// an unanswered need, the stop hook, acceptance capture, the project's
// checks, the acceptance checks, the verdict on what the turn actually
// demonstrated, and the goal comparison that can put a bad round back.
// Any of them may send the model back for another call, which is what a
// false result means. True means the turn is over and RunBlocks returns.
func (a *Agent) finishTurn(ctx context.Context, resp *llm.Response, g *turnGates, emit func(Event)) bool {
	// The model ended its turn instead of making tool calls: not the
	// mechanical streak the router looks for, whatever sends it back for
	// another call below.
	a.stepRoute.prevMechanical = false
	if a.faultNeeds(resp.Message.Text(), &g.needs, emit) {
		return false
	}
	if a.stopHook(ctx, resp.Message.Text(), &g.stops, emit) {
		return false
	}
	if a.acceptCapture(resp.Message.Text(), &g.acceptNudged, emit) {
		return false
	}
	if a.openTodoGate(resp.Message.Text(), &g.todoNudged, emit) {
		return false
	}
	if a.diagnosticsGate(&g.diagRounds, emit) {
		return false
	}
	if !a.Off["verify"] && a.verifyGate(ctx, &g.verify, emit) {
		a.stepRoute.taint("a verification check failed and was sent back")
		a.noteEffort(effortTrouble)
		return false
	}
	if a.acceptVerifyGate(ctx, &g.accept, emit) {
		a.stepRoute.taint("an acceptance check failed and was sent back")
		a.noteEffort(effortTrouble)
		return false
	}
	// What the turn actually demonstrated, decided from what the harness
	// watched rather than from what was written. A claim the turn does
	// not support is put back to the model once.
	outcome := a.Outcome()
	if !a.Off["verdict"] && outcome.Overclaimed() && g.corrections == 0 {
		g.corrections++
		emit(Event{Kind: EvNotice, Text: "the answer claims more than this turn shows: " + outcome.Why})
		a.appendHarnessText(outcome.Correction())
		return false
	}
	// The premature stop the verdict rules cannot see: checks passed, the
	// model edited afterwards, and then it said it was done. Opt-in,
	// because every baseline in this repository was measured without it,
	// and bounded by the same one-correction-a-turn counter, because the
	// expensive mistake here is nagging a turn that is genuinely finished
	// into another round of model calls.
	if a.StaleNudgeOn() && !a.Off["verdict"] && outcome.StaleCheck() && g.corrections == 0 {
		g.corrections++
		emit(Event{Kind: EvNotice, Text: "the last edit of this turn was never checked: " + outcome.Why})
		a.appendHarnessText(outcome.StaleCheckNote())
		return false
	}
	if a.specReview(outcome, &g.specReviewed, emit) {
		return false
	}
	a.logOutcome(outcome)
	// A goal closed in a turn whose checks came back red is put back to
	// active, once, so the loop answers for the red rather than stopping
	// beside it.
	a.ReopenCompletedGoal(outcome, emit)
	if a.keepOrRevert(ctx, outcome, emit) {
		a.stepRoute.taint("a goal round was reverted because it made things worse")
		return false
	}
	a.refreshProofNotes(ctx, emit)
	if !a.Off["verdict"] && outcome.Verdict != VerdictNoChanges {
		emit(Event{Kind: EvVerdict, Text: outcome.Line(), IsError: !outcome.OK()})
	}
	a.capturePlan(resp.Message.Text(), emit)
	a.logEvent(session.Event{Type: session.TypeStepEnd, Turn: a.turnN, Step: a.stepN})
	a.logEvent(session.Event{Type: session.TypeTurnEnd, Turn: a.turnN, Reason: string(resp.StopReason)})
	emit(Event{Kind: EvTurnDone, Usage: a.TurnUsage(), StopReason: resp.StopReason})
	return true
}

// runToolStep runs the step's tool calls and appends their results, with
// the notes this step owes the model attached to the last of them. done
// says the turn is over: because the provider asked for tools and sent
// none, because the loop detector gave up, because the spend ceiling was
// reached, or because err ended it.
func (a *Agent) runToolStep(ctx context.Context, resp *llm.Response, repeatNote string, emit func(Event)) (done bool, err error) {
	uses := resp.Message.ToolUses()
	if len(uses) == 0 {
		// Provider said tool_use but sent none; treat as end of turn
		// rather than looping forever.
		a.logEvent(session.Event{Type: session.TypeStepEnd, Turn: a.turnN, Step: a.stepN})
		a.logEvent(session.Event{Type: session.TypeTurnEnd, Turn: a.turnN, Reason: string(llm.StopEndTurn)})
		emit(Event{Kind: EvTurnDone, Usage: a.TurnUsage(), StopReason: llm.StopEndTurn})
		return true, nil
	}
	planNote := ""
	if a.PlanCheck != nil && !a.Off["plan"] {
		if text := strings.TrimSpace(resp.Message.Text()); text != "" {
			planNote = a.PlanCheck(ctx, text)
		}
	}
	results, images, runErr := a.executeAll(ctx, uses, emit)
	a.stepRoute.prevMechanical = allMechanicalStep(uses, results)
	if runErr == nil && len(results) > 0 {
		if note := a.elideAfterStep(ctx, uses, results, emit); note != "" {
			results[len(results)-1].Text += "\n\n" + note
		}
	}
	if note, _ := a.collectDiagnostics(0); note != "" && len(results) > 0 {
		results[len(results)-1].Text += "\n\n" + note
		emit(Event{Kind: EvNotice, Text: "diagnostics: " + firstLine(note)})
	}
	if repeatNote != "" && len(results) > 0 {
		results[len(results)-1].Text += repeatNote
		emit(Event{Kind: EvNotice, Text: strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(repeatNote), "[harness]"))})
	}
	if planNote != "" && len(results) > 0 {
		results[len(results)-1].Text += "\n\n[harness] " + planNote
		emit(Event{Kind: EvNotice, Text: planNote})
	}
	if a.burstRan > 0 && len(results) > 0 {
		results[len(results)-1].Text += "\n\n" + burstNote(a.burstRan, a.burstRepeat)
		emit(Event{Kind: EvNotice, Text: fmt.Sprintf("the reply kept starting tool calls; ran its first %d and dropped the rest", a.burstRan)})
	}
	a.burstRan, a.burstRepeat = 0, false
	if note := a.reviewStepNote(); note != "" && len(results) > 0 {
		results[len(results)-1].Text += "\n\n" + note
		emit(Event{Kind: EvNotice, Text: "the review has used its calls; asking the model to wrap up"})
	}
	if a.EffortControlOn() {
		a.noteEffort(stepEffortSignal(uses, results, a.effort.edited))
	}
	tr := llm.ToolResultMessage(results...)
	tr.Blocks = append(tr.Blocks, images...)
	a.appendMessage(tr)
	a.logEvent(session.Event{Type: session.TypeToolResult, Turn: a.turnN, Step: a.stepN, Surface: true, Message: &tr})
	a.logEvent(session.Event{Type: session.TypeStepEnd, Turn: a.turnN, Step: a.stepN})
	if runErr != nil {
		return true, runErr
	}
	if a.stuck {
		a.stuck = false
		emit(Event{Kind: EvNotice, Text: "turn stopped: " + a.loops.reason})
		a.logEvent(session.Event{Type: session.TypeTurnEnd, Turn: a.turnN, Reason: "stuck"})
		return true, ErrStuck
	}
	// Tools can cost money too (a subagent runs its own turns), and the
	// next model call is about to be made with a history this step just
	// grew. Ask again before paying for it.
	if spent, over := a.overBudget(); over {
		return true, a.stopForBudget(spent, nil, emit)
	}
	return false, nil
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
