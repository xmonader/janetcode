package session

import (
	"encoding/json"
	"sort"
	"sync"
	"time"

	"github.com/xmonader/janetcode/internal/llm"
)

// Event types persisted in the session JSONL. Unknown types are skipped on Open.
const (
	TypeTurnStart      = "turn/start"
	TypeTurnEnd        = "turn/end"
	TypeStepStart      = "step/start"
	TypeStepEnd        = "step/end"
	TypeUserMessage    = "user/message"
	TypeAssistant      = "assistant/message"
	TypeToolCall       = "tool/call"
	TypeToolResult     = "tool/result"
	TypeNotice         = "notice"
	TypeCritic         = "critic/score"
	TypeCompactStart   = "compaction/start"
	TypeCompactSummary = "compaction/summary"
	TypeCompactEnd     = "compaction/end"
	TypePlanMode       = "plan/mode"
	TypeSpillSave      = "spill/save"
	// TypeRetrieval records one lookup in the archive: what was asked,
	// what came back, and whether anything did. Without it a failed
	// recall cannot be told from a failed search.
	TypeRetrieval  = "archive/retrieval"
	TypeGoalCreate = "goal/create"
	TypeGoalPhase  = "goal/phase"
	TypeGoalRound  = "goal/round"
	// TypeTodoWrite carries the whole plan each time it is rewritten, so
	// the latest one is the plan and it outlives the tool result.
	TypeTodoWrite = "todo/write"
	// TypeVerdict records what a turn that changed code actually
	// demonstrated, as against what the model said about it.
	TypeVerdict = "turn/verdict"
	// TypeMutation records one mutation-testing run's result (see
	// internal/mutate.Report.Event): Turn names the turn whose changed
	// lines were mutated, and Extra carries {killed, survived, total,
	// command}. A proof record folds the latest one per turn into
	// proof.Turn.Mutation.
	TypeMutation = "verify/mutation"
	// TypeGate records one command a turn-end gate ran on the harness's
	// own initiative — the project's verify commands, or the locked
	// /accept tests. Name is the command, IsError whether it failed,
	// Output what it printed when that was kept, and Source which gate
	// ran it (GateVerify or GateAccept). The verdict fold counts it as a
	// check the turn ran, so a green gate after a red run is not lost.
	TypeGate = "verify/gate"
)

// Gate sources on a TypeGate event.
const (
	GateVerify = "verify"
	GateAccept = "accept"
)

// TurnStartAccept prefixes a TypeTurnStart event's Reason when the turn
// began with /accept mode on, followed by the phase ("accept:writing"),
// so a fold read back later knows red tests in the writing phase were
// the expected state rather than a failure.
const TurnStartAccept = "accept:"

const (
	SurfaceOpReplace = "replace"
	SourceGoal       = "goal"
	SourceInject     = "inject"
)

// Log is the append-only event sink the agent writes during a turn.
// Append returns the seq the event was given, so a later event can replace it.
type Log interface {
	Append(Event) (int, error)
	Events() []Event
}

// Event is one append-only log record. Surface events (Surface=true and a
// Message) form the model-visible history after FoldSurface; the rest are
// log-only (compaction, notices, plan/mode, goals).
type Event struct {
	Seq       int          `json:"seq"`
	Time      time.Time    `json:"at"`
	Type      string       `json:"t"`
	Turn      int          `json:"turn,omitempty"`
	Step      int          `json:"step,omitempty"`
	Surface   bool         `json:"surface,omitempty"`
	SurfaceOp string       `json:"surface_op,omitempty"`
	Replaces  []int        `json:"replaces,omitempty"`
	Shadowed  []int        `json:"shadowed,omitempty"`
	Message   *llm.Message `json:"message,omitempty"`
	Usage     *llm.Usage   `json:"usage,omitempty"`
	CallID    string       `json:"call_id,omitempty"`
	Name      string       `json:"name,omitempty"`
	Arguments string       `json:"arguments,omitempty"`
	// Intent is the model's own one-line reason for a tool call.
	Intent  string `json:"intent,omitempty"`
	Output  string `json:"output,omitempty"`
	IsError bool   `json:"is_error,omitempty"`
	// Denied marks a tool/result that is the harness refusing the call —
	// a permission rule, a mode, a hook, the user saying no or cancelling
	// — rather than the tool running. Nothing ran, so nothing that reads
	// the log for what a turn did may count it as a run.
	Denied     bool   `json:"denied,omitempty"`
	DurationMs int64  `json:"duration_ms,omitempty"`
	Reason     string `json:"reason,omitempty"`
	Active     *bool  `json:"active,omitempty"`
	Source     string `json:"source,omitempty"`
	TTFTMs     int64  `json:"ttft_ms,omitempty"`
	DecodeMs   int64  `json:"decode_ms,omitempty"`
	Tokens     int    `json:"tokens,omitempty"`
	// Model is the model that actually answered a TypeAssistant event's
	// call: Agent.Model on an ordinary step, a fallback's model after a
	// failure, or the experimental step router's RouteCheap (see
	// internal/agent/steproute.go) when the step was routed there. Empty
	// on every event that is not one model call.
	Model string          `json:"model,omitempty"`
	Extra json.RawMessage `json:"extra,omitempty"`
}

// live returns the indexes of the surface events that still reach the
// model, in the order the model sees them. A replacement event (a
// compaction summary, or a message rewritten in place by pruning) is
// appended to the log long after what it replaces, but it stands *where*
// that content stood, so it is ordered by the earliest position it
// replaces, following chains of replacements.
func live(events []Event) []int {
	hidden := shadowedSet(events)
	pos := make([]int, len(events))
	at := make(map[int]int, len(events)) // seq → index
	for i, e := range events {
		pos[i] = i
		if e.SurfaceOp == SurfaceOpReplace {
			best := -1
			for _, r := range e.Replaces {
				if j, ok := at[r]; ok && (best < 0 || pos[j] < best) {
					best = pos[j]
				}
			}
			if best >= 0 {
				pos[i] = best
			}
		}
		at[e.Seq] = i
	}
	var out []int
	for i, e := range events {
		if e.Surface && e.Message != nil && !hidden[e.Seq] {
			out = append(out, i)
		}
	}
	sort.SliceStable(out, func(a, b int) bool { return pos[out[a]] < pos[out[b]] })
	return out
}

// FoldSurface returns the model-visible messages: surface events whose seq
// was not shadowed by a later replacement, in model order.
func FoldSurface(events []Event) []llm.Message {
	idx := live(events)
	out := make([]llm.Message, 0, len(idx))
	for _, i := range idx {
		out = append(out, *events[i].Message)
	}
	if len(out) == 0 {
		return nil
	}
	return answerOrphans(out)
}

// answerOrphans gives every tool_use a tool_result.
//
// A session killed between a tool call and its result — ^C during a long
// command, a crash, a machine going to sleep — logs the assistant message
// and never logs the result. Providers reject a history where a tool_use
// is unanswered, so resuming such a session fails on its first request
// with nothing to tell the user what is wrong. The missing results are
// synthesised here, in the position they would have occupied, saying what
// actually happened.
func answerOrphans(msgs []llm.Message) []llm.Message {
	answered := map[string]bool{}
	for _, m := range msgs {
		for _, b := range m.Blocks {
			if b.Type == llm.BlockToolResult {
				answered[b.ToolUseID] = true
			}
		}
	}
	out := make([]llm.Message, 0, len(msgs)+1)
	for _, m := range msgs {
		out = append(out, m)
		var missing []llm.Block
		for _, b := range m.Blocks {
			if b.Type == llm.BlockToolUse && !answered[b.ID] {
				answered[b.ID] = true // a repeated id is answered once
				missing = append(missing, llm.ToolResult(b.ID,
					"No result: the session ended while this call was running, so whether it took effect is unknown. Check before assuming either way.", true))
			}
		}
		if len(missing) > 0 {
			out = append(out, llm.ToolResultMessage(missing...))
		}
	}
	return out
}

// FoldPlanMode is the last logged plan/mode value, or false if none.
func FoldPlanMode(events []Event) bool {
	on := false
	for _, e := range events {
		if e.Type == TypePlanMode && e.Active != nil {
			on = *e.Active
		}
	}
	return on
}

// SurfaceKind classifies an event for query: current model context,
// compacted-away context, or log-only.
type SurfaceKind string

const (
	SurfaceCurrent  SurfaceKind = "current"
	SurfaceShadowed SurfaceKind = "shadowed"
	SurfaceLogOnly  SurfaceKind = "log-only"
)

// Shadowed is the set of seqs replaced by a later event. Callers that
// classify many events build it once and pass it to ClassifyWith; Classify
// rebuilds it for a single lookup.
func Shadowed(events []Event) map[int]bool { return shadowedSet(events) }

// ClassifyWith reports whether e is on the current surface, given the
// shadowed set from Shadowed.
func ClassifyWith(hidden map[int]bool, e Event) SurfaceKind {
	if !e.Surface {
		return SurfaceLogOnly
	}
	if hidden[e.Seq] {
		return SurfaceShadowed
	}
	return SurfaceCurrent
}

// Classify reports whether e is on the current surface.
func Classify(events []Event, e Event) SurfaceKind {
	return ClassifyWith(shadowedSet(events), e)
}

// CurrentSeqs is the seqs of the surface events that still reach the model,
// in the same order as FoldSurface, so index i names message i.
func CurrentSeqs(events []Event) []int {
	idx := live(events)
	seq := make([]int, 0, len(idx))
	for _, i := range idx {
		seq = append(seq, events[i].Seq)
	}
	if len(seq) == 0 {
		return nil
	}
	return seq
}

func shadowedSet(events []Event) map[int]bool {
	h := map[int]bool{}
	for _, e := range events {
		for _, n := range e.Shadowed {
			h[n] = true
		}
		if e.SurfaceOp == SurfaceOpReplace {
			for _, n := range e.Replaces {
				h[n] = true
			}
		}
	}
	return h
}

// Memory is an in-memory Log, used by eval runs and tests. Like Session it
// is written from the parallel tool goroutines of a turn.
type Memory struct {
	mu sync.Mutex
	Ev []Event
}

// Append implements Log.
func (m *Memory) Append(e Event) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if e.Seq == 0 {
		e.Seq = len(m.Ev) + 1
	}
	if e.Time.IsZero() {
		e.Time = time.Now().UTC()
	}
	m.Ev = append(m.Ev, e)
	return e.Seq, nil
}

// Events implements Log.
func (m *Memory) Events() []Event {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]Event(nil), m.Ev...)
}
