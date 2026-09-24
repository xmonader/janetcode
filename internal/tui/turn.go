package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/xmonader/janetcode/internal/agent"
	"github.com/xmonader/janetcode/internal/flavor"
	"github.com/xmonader/janetcode/internal/hints"
	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/prompt"
	"github.com/xmonader/janetcode/internal/textfmt"
)

// turnState is the turn the model is running right now: whether there
// is one, how to stop it, what it is doing, and what the user has lined
// up behind it. Every field is only meaningful while running is true,
// apart from the queue, which survives a turn precisely so finishTurn
// can send the next one.
type turnState struct {
	running    bool
	cancelling bool
	compacting bool
	cancel     context.CancelFunc
	startedAt  time.Time // when the running turn began
	// lastEventAt is when the turn last produced anything at all — a
	// token, a tool boundary, a phase change. The gap since says how long
	// the turn has been waiting on the provider, which on a slow endpoint
	// is most of the wall clock and was indistinguishable from a wedged
	// session: the spinner turns either way.
	lastEventAt time.Time
	phase       string // what the agent is doing right now
	// phaseUntil is when the thing the phase names is due, so a wait the
	// provider announced once counts down instead of standing still.
	phaseUntil time.Time
	queued     []string // prompts typed while a turn was running
	steering   []string // notes handed to the running turn (ctrl+s)
}

// start marks a turn as begun, dropping the steering notes that belonged
// to the turn before it.
func (t *turnState) start() {
	t.steering = nil
	t.running = true
	t.cancelling = false
	t.startedAt = time.Now()
	t.lastEventAt = t.startedAt
}

// stop cancels the running turn's context, if it has one.
func (t *turnState) stop() {
	if t.cancel != nil {
		t.cancel()
	}
	t.cancelling = true
}

// finish ends the turn and reports whether the thing that ended was a
// compaction rather than a conversational turn.
func (t *turnState) finish() bool {
	t.running, t.cancelling = false, false
	compacting := t.compacting
	t.compacting = false
	t.release()
	t.steering = nil
	return compacting
}

// release drops the turn's cancel function, cancelling it so the context
// it made is not left behind.
func (t *turnState) release() {
	if t.cancel != nil {
		t.cancel()
		t.cancel = nil
	}
}

// setPhase records what the turn is waiting on. A provider that is about
// to sleep says so once — "retrying 1/2 · rate limited · in 6s" — and
// that line then sat unchanged for six seconds next to a counter running
// the other way, which reads as a frozen screen. The deadline is kept so
// the line can count down to the moment something happens.
func (t *turnState) setPhase(text string) {
	t.phase, t.phaseUntil = text, time.Time{}
	if prefix, d, ok := textfmt.SplitCountdown(text); ok {
		t.phase, t.phaseUntil = prefix, time.Now().Add(d)
	}
}

// quietFor is how long the turn has produced nothing. Zero when the turn
// is not running or has never been stamped.
func (t *turnState) quietFor() time.Duration {
	if !t.running || t.lastEventAt.IsZero() {
		return 0
	}
	return time.Since(t.lastEventAt)
}

// phaseText is the phase with its countdown resolved to now.
func (t *turnState) phaseText() string {
	if t.phaseUntil.IsZero() {
		return flavor.ForPhase(t.phase, time.Now(), t.startedAt)
	}
	return textfmt.Countdown(t.phase, t.phaseUntil, time.Now())
}

// dequeue takes the next prompt waiting behind the finished turn.
func (t *turnState) dequeue() (string, bool) {
	if len(t.queued) == 0 {
		return "", false
	}
	next := t.queued[0]
	t.queued = t.queued[1:]
	return next, true
}

// takeQueued hands back everything queued and empties the queue, for the
// cancel that puts it in the input rather than throwing it away.
func (t *turnState) takeQueued() []string {
	parts := t.queued
	t.queued = nil
	return parts
}

func (m *model) submit(text string) tea.Cmd { return m.submitAs(text, "") }

// submitAs sends text to the model but shows note in its place when the
// prompt is machinery rather than something the user wrote. A goal round
// used to appear in the transcript as a user message carrying the whole
// continuation, housekeeping paragraph included, as though the user had
// typed "(This is round 1 of at most 8. Rounds are a budget, not a
// plan...)".
func (m *model) submitAs(text, note string) tea.Cmd {
	text = m.inflatePastes(text)
	if m.opts.Agent.Settings().Provider == nil {
		m.transcript.errorf("not connected to a provider yet: run /connect")
		m.refresh(false)
		return nil
	}
	if note != "" {
		m.transcript.info(note)
	} else {
		m.transcript.user(text)
		m.observePrompt(text)
	}
	// A host the user just named is a host the user is asking about, so
	// fetch_url may reach it even on a private network. Only main seeded
	// this, from the first prompt, which left "look at
	// http://localhost:8080/health" refused in the message that asked for
	// it.
	m.opts.Typed.Note(text)
	m.hist.lastUser = text
	text, atts := prompt.ExpandMentions(m.opts.Cwd, text)
	if len(atts) > 0 {
		m.noteUse("@path")
		names := make([]string, len(atts))
		for i, a := range atts {
			names[i] = a.Path
			switch {
			case a.Image():
				names[i] += " (image, " + textfmt.Bytes(a.Size) + ")"
			case !a.Dir:
				names[i] += " (" + textfmt.Bytes(a.Size) + ")"
			}
		}
		m.transcript.info(noteAttached + "attached " + strings.Join(names, " · "))
	}
	blocks := append([]llm.Block{{Type: llm.BlockText, Text: text}}, prompt.ImageBlocks(atts)...)
	m.startTurn("thinking")
	ctx, cancel := context.WithCancel(m.life)
	m.turn.cancel = cancel
	ag, send, queue := m.opts.Agent, m.send, m.queue
	m.repin()
	m.refresh(false)
	go func() {
		// Every message reached the session file as it happened; there is
		// nothing left to write when the turn ends.
		err := ag.RunBlocks(ctx, blocks, queue.push)
		send(agentDoneMsg{err: err})
	}()
	return m.spinner.Tick
}

func (m *model) startTurn(phase string) {
	m.turn.start()
	m.term.turnStarted()
	m.status = ""
	m.lastVerdict = ""
	m.turn.setPhase(phase)
	if phase == "thinking" {
		m.transcript.apply(agent.Event{Kind: agent.EvThinking})
	}
}

// onOff names a toggle's new state, so every "/x is on" line reads the
// same way.
func onOff(v bool) string {
	if v {
		return "on"
	}
	return "off"
}

// compact summarises the history in the background like a turn.
func (m *model) compact() tea.Cmd {
	m.startTurn("compacting context")
	m.turn.compacting = true
	ctx, cancel := context.WithCancel(m.life)
	m.turn.cancel = cancel
	ag, send, queue := m.opts.Agent, m.send, m.queue
	m.note(fmt.Sprintf("compacting ~%s tokens of context…", textfmt.Tokens(ag.EstimateTokens())))
	m.refresh(false)
	go func() {
		// Compaction appends the summary and the seqs it replaces to the
		// same log; the session file stays as it is.
		err := ag.ForceCompact(ctx, queue.push)
		if errors.Is(err, agent.ErrNothingToCompact) {
			err = nil
		}
		send(agentDoneMsg{err: err})
	}()
	return m.spinner.Tick
}

func (m *model) compactFast() tea.Cmd {
	m.startTurn("pruning context")
	m.turn.compacting = true
	ctx, cancel := context.WithCancel(m.life)
	m.turn.cancel = cancel
	ag, send, queue := m.opts.Agent, m.send, m.queue
	m.note(fmt.Sprintf("pruning ~%s tokens of context…", textfmt.Tokens(ag.EstimateTokens())))
	m.refresh(false)
	go func() {
		err := ag.CompactFast(ctx, queue.push)
		if errors.Is(err, agent.ErrNothingToCompact) {
			err = nil
		}
		send(agentDoneMsg{err: err})
	}()
	return m.spinner.Tick
}

func (m *model) cancelTurn() {
	m.turn.stop()
	m.turn.setPhase("cancelling")
	m.status = "cancelling… (ctrl+c again to force quit)"
}

func (m *model) finishTurn(msg agentDoneMsg) tea.Cmd {
	compacting := m.turn.finish()
	// Whatever the turn was asking goes with the turn. A prompt left up
	// over a turn that has ended answers into a channel nobody reads: the
	// question stays on screen, and the denial reason being typed keeps
	// the composer in a mode ("Why? the model reads this") that no longer
	// means anything.
	m.closePending()
	if m.asking != nil {
		m.finishAsk()
	}
	m.turn.setPhase("")
	elapsed := textfmt.Duration(time.Since(m.turn.startedAt))
	m.transcript.finishTurn()
	switch {
	case errors.Is(msg.err, context.Canceled):
		m.transcript.dropEmptyTrailing()
		m.transcript.abortUnfinished("cancelled")
		m.transcript.errorf("cancelled after %s", elapsed)
		m.status = "cancelled"
	case errors.Is(msg.err, agent.ErrMaxIterations):
		m.transcript.errorf("stopped after %d tool rounds in one turn · send a message to continue", m.opts.Agent.Settings().MaxIterations)
		m.status = "stopped"
	case errors.Is(msg.err, agent.ErrStuck):
		m.transcript.errorf("stopped: the model kept repeating the same tool call · its changes so far are kept; tell it what to do differently")
		m.status = "stuck"
	case msg.err != nil:
		m.transcript.dropEmptyTrailing()
		m.transcript.abortUnfinished("the turn ended before this call returned")
		m.transcript.errorf("%s", llm.Explain(msg.err, m.providerLabel(), m.remedies()).String())
		m.status = "error"
	case compacting:
		m.status = "compacted in " + elapsed
	default:
		m.status = "done in " + elapsed
		if b := verdictBrief(m.lastVerdict); b != "" {
			m.status = b + " · " + m.status
		}
		if m.lastTurn != "" {
			m.status += " · " + m.lastTurn
		}
		if n := m.filesChangedLastTurn(); n > 0 {
			m.status += fmt.Sprintf(" · %s changed · /undo reverts", textfmt.Plural(n, "file"))
		}
		if m.lastTurnTokens != "" {
			m.status += " · " + m.lastTurnTokens
		}
	}
	// The end of a turn is where the hint rules get their chance: the
	// counters are settled, nothing is being asked of the user, and a
	// line here is read rather than scrolled past.
	m.noteHint(func(s *hints.State) {
		if m.filesChangedLastTurn() > 0 {
			s.MutatingTurns++
		}
	})
	m.reportCacheWaste()
	m.term.turnEnded(msg.err == nil, m.status)
	if msg.err == nil && len(m.turn.queued) == 0 {
		if cmd := m.goalRound(); cmd != nil {
			return cmd
		}
		if cmd := m.suggestFollowup(); cmd != nil {
			return cmd
		}
	}
	if len(m.turn.queued) == 0 {
		return nil
	}
	if msg.err != nil {
		// Do not fire queued prompts into a failed or cancelled turn; hand
		// them back so nothing typed is lost.
		m.restoreQueued()
		return nil
	}
	next, _ := m.turn.dequeue()
	return m.submit(next)
}

// restoreQueued puts queued messages back in the input, under whatever is
// already there, separated by blank lines. Nothing typed is ever thrown
// away by a cancel: it goes back where it came from.
func (m *model) restoreQueued() {
	if len(m.turn.queued) == 0 {
		return
	}
	parts := m.turn.takeQueued()
	if v := strings.TrimSpace(m.input.Value()); v != "" {
		parts = append(parts, v)
	}
	m.input.SetValue(strings.Join(parts, "\n\n"))
	m.input.CursorEnd()
	m.layout()
	m.refresh(false)
}

// reportCacheWaste says when a turn paid a second time for a prefix the
// turn before had cached. It is invisible otherwise: the turn works, the
// answer is right, and the bill is several times larger.
func (m *model) reportCacheWaste() {
	n, cause, idle := m.opts.Agent.TurnCacheWaste()
	if n == 0 {
		return
	}
	head := "cache miss"
	if idle > time.Minute {
		head += " after " + textfmt.Duration(idle.Round(time.Minute)) + " idle"
	}
	line := fmt.Sprintf("%s: %s tokens re-billed", head, textfmt.Tokens(n))
	if usd, ok := (llm.Usage{InputTokens: n}).Cost(m.opts.Agent.Settings().Price); ok {
		line += " (~" + llm.FormatUSD(usd) + ")"
	}
	m.note(line + " · " + cause)
}

// tuiRemedies are the terminal's words for the fixes a provider failure
// calls for.
var tuiRemedies = llm.Remedies{Key: "with /connect", Model: "with /model"}
