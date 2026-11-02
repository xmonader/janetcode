package agent

import (
	"sync"
	"sync/atomic"

	"github.com/xmonader/janetcode/internal/llm"
)

// turnState is the collaborator that holds what belongs to the turn in
// progress: whether one is running at all, what it has changed, what the
// user and the harness have queued for it, the tool list it froze, where
// its events go, and the goal bookkeeping that decides whether its round
// is kept. Everything here is reset by begin and means nothing between
// turns.
//
// It is embedded in Agent by value — the zero value is the right state
// for "no turn has run yet" — so the fields keep their names at the call
// sites all over the package.
//
// Ownership, which is the reason this is one struct rather than eight
// fields scattered down Agent:
//
//   - running is atomic: a front-end tests it from its own goroutine
//     before a command that would rewrite the conversation.
//   - steer and harness are guarded by steerMu. They are written from
//     the UI goroutine (Steer) and from the turn goroutine (path rules)
//     and drained by the turn goroutine before each model call.
//   - emitNow is guarded by emitMu: a tool that dispatches tool calls of
//     its own reads it while the turn goroutine is setting it.
//   - everything else — mutated, mutations, proofCommits, tools and the
//     goal fields — belongs to the turn goroutine alone. Mutating tools
//     run serially, so the counters need no lock; nothing off the turn
//     goroutine may read them.
type turnState struct {
	// running is set between beginTurn and endTurn, so a command that
	// rewrites the conversation ("/undo turn n", see rewind.go) can
	// refuse rather than race the turn goroutine that is appending to
	// it. The front-ends keep such commands off the key handler while a
	// turn runs (slash.idleOnly); this is the agent's own check.
	running atomic.Bool

	// mutated says a mutating tool ran successfully this turn, and
	// mutations counts how many did.
	mutated   bool
	mutations int
	// proofCommits are the commits this turn attached a proof note to
	// (see checkCommitAttach), refreshed once the turn's gates have run.
	proofCommits []string

	// keepReverts is how many times this goal has had a round put back
	// in a row; lastGoalOut is the previous round's verdict, for the
	// comparison; steeredThisTurn is set when the user steered, so a
	// turn they directed is never reverted (see keepOrRevert). Only
	// steeredThisTurn is per-turn; the other two carry across the rounds
	// of one goal, which is why begin leaves them alone.
	keepReverts     int
	lastGoalOut     TurnOutcome
	goalOutSet      bool
	steeredThisTurn bool

	// tools is this turn's tool list, held so that a server registering
	// mid-turn cannot rewrite the cached prefix. Cleared at each turn.
	tools []llm.ToolDef

	steerMu sync.Mutex
	steer   []string // notes for the running turn (see Steer)
	harness []string // harness text for the running turn (path rules)

	// emitNow is the current turn's event sink. A tool that dispatches
	// further tool calls of its own — a script does — needs it to show
	// them as they happen, and it is not on the Tool interface.
	emitMu  sync.Mutex
	emitNow func(Event)
}

// begin resets the per-turn fields and marks a turn as running. The goal
// fields that span a goal's rounds (keepReverts, lastGoalOut) are not
// reset: the point of them is to compare this round with the last.
func (s *turnState) begin() {
	s.running.Store(true)
	s.mutated, s.mutations = false, 0
	s.proofCommits = nil
	s.steeredThisTurn = false
}

// queueSteer adds a note for the running turn, from any goroutine.
func (s *turnState) queueSteer(text string) {
	s.steerMu.Lock()
	s.steer = append(s.steer, text)
	s.steerMu.Unlock()
}

// interjectSteer puts a note ahead of any already waiting.
func (s *turnState) interjectSteer(text string) {
	s.steerMu.Lock()
	s.steer = append([]string{text}, s.steer...)
	s.steerMu.Unlock()
}

// queueHarness adds harness text for the running turn, from any
// goroutine: a path-scoped project rule a tool call brought in.
func (s *turnState) queueHarness(text string) {
	s.steerMu.Lock()
	s.harness = append(s.harness, text)
	s.steerMu.Unlock()
}

// takeSteering drains everything queued for the turn.
func (s *turnState) takeSteering() (notes, harness []string) {
	s.steerMu.Lock()
	defer s.steerMu.Unlock()
	notes, harness = s.steer, s.harness
	s.steer, s.harness = nil, nil
	return notes, harness
}

// pendingSteer is how many notes arrived too late for this turn's last
// model call, so the turn can say they go with the next message.
func (s *turnState) pendingSteer() int {
	s.steerMu.Lock()
	defer s.steerMu.Unlock()
	return len(s.steer)
}

// setEmitter points the turn's tool calls at sink; nil after the turn, so
// emitter's promise of a sink that drops events holds.
func (s *turnState) setEmitter(sink func(Event)) {
	s.emitMu.Lock()
	s.emitNow = sink
	s.emitMu.Unlock()
}

// emitter is the current turn's event sink, or a sink that drops events
// when there is none (a tool run outside a turn, as in a test).
func (s *turnState) emitter() func(Event) {
	s.emitMu.Lock()
	defer s.emitMu.Unlock()
	if s.emitNow == nil {
		return func(Event) {}
	}
	return s.emitNow
}
