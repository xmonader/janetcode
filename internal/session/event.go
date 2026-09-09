package session

import (
	"encoding/json"
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
	TypeGoalCreate     = "goal/create"
	TypeGoalPhase      = "goal/phase"
	TypeGoalRound      = "goal/round"
)

const (
	SurfaceOpReplace = "replace"
	SourceGoal       = "goal"
	SourceInject     = "inject"
)

// Log is the append-only event sink the agent writes during a turn.
type Log interface {
	Append(Event) error
	Events() []Event
}

// Event is one append-only log record. Surface events (Surface=true and a
// Message) form the model-visible history after FoldSurface; the rest are
// log-only (compaction, notices, plan/mode, goals).
type Event struct {
	Seq        int             `json:"seq"`
	Time       time.Time       `json:"at"`
	Type       string          `json:"t"`
	Turn       int             `json:"turn,omitempty"`
	Step       int             `json:"step,omitempty"`
	Surface    bool            `json:"surface,omitempty"`
	SurfaceOp  string          `json:"surface_op,omitempty"`
	Replaces   []int           `json:"replaces,omitempty"`
	Shadowed   []int           `json:"shadowed,omitempty"`
	Message    *llm.Message    `json:"message,omitempty"`
	Usage      *llm.Usage      `json:"usage,omitempty"`
	CallID     string          `json:"call_id,omitempty"`
	Name       string          `json:"name,omitempty"`
	Arguments  string          `json:"arguments,omitempty"`
	Output     string          `json:"output,omitempty"`
	IsError    bool            `json:"is_error,omitempty"`
	DurationMs int64           `json:"duration_ms,omitempty"`
	Reason     string          `json:"reason,omitempty"`
	Active     *bool           `json:"active,omitempty"`
	Source     string          `json:"source,omitempty"`
	TTFTMs     int64           `json:"ttft_ms,omitempty"`
	DecodeMs   int64           `json:"decode_ms,omitempty"`
	Tokens     int             `json:"tokens,omitempty"`
	Extra      json.RawMessage `json:"extra,omitempty"`
}

// FoldSurface returns the model-visible messages: surface events whose seq
// was not shadowed by a later compaction replace.
func FoldSurface(events []Event) []llm.Message {
	hidden := shadowedSet(events)
	var out []llm.Message
	for _, e := range events {
		if !e.Surface || e.Message == nil || hidden[e.Seq] {
			continue
		}
		out = append(out, *e.Message)
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

// Classify reports whether e is on the current surface.
func Classify(events []Event, e Event) SurfaceKind {
	if !e.Surface {
		return SurfaceLogOnly
	}
	if shadowedSet(events)[e.Seq] {
		return SurfaceShadowed
	}
	return SurfaceCurrent
}

// CurrentSeqs is the seqs of surface events that still reach the model.
func CurrentSeqs(events []Event) []int {
	hidden := shadowedSet(events)
	var seq []int
	for _, e := range events {
		if e.Surface && e.Message != nil && !hidden[e.Seq] {
			seq = append(seq, e.Seq)
		}
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

// Memory is an in-memory Log for tests.
type Memory struct {
	Ev []Event
}

// Append implements Log.
func (m *Memory) Append(e Event) error {
	if e.Seq == 0 {
		e.Seq = len(m.Ev) + 1
	}
	if e.Time.IsZero() {
		e.Time = time.Now().UTC()
	}
	m.Ev = append(m.Ev, e)
	return nil
}

// Events implements Log.
func (m *Memory) Events() []Event { return m.Ev }
