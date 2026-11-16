package agent

import (
	"context"
	"time"

	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/session"
)

// OpenSession makes sess the agent's session with history as the
// conversation. The log, the pins and the undo stack all come from sess,
// so nothing of the session being left behind — its checkpoints above
// all — is carried into the new one. /clear, /resume and their web
// equivalents all switch through here.
//
// The /accept state and the armed /loop jobs follow the session as well,
// the way startup restores them: the incoming session's own are loaded
// and saved to it from here on. Left alone, the old session's accept lock
// went on refusing edits in the new one, its loops fired their prompts
// into a conversation that never asked for them, and every later change
// to either was written to the session left behind. That session keeps
// what it had saved, and gets it back when it is resumed.
//
// The agent takes the incoming session's lease before anything changes
// and gives the outgoing one's back: a session another process is
// running turns in is refused with a *session.LeasedError, and the
// conversation stays where it was.
func (a *Agent) OpenSession(sess *session.Session, history []llm.Message) error {
	if err := a.moveLease(sess); err != nil {
		return err
	}
	a.retireSubagents()
	a.switchSessionHooks(history)
	a.setMessages(history)
	a.retaint(history)
	a.SetPins(sess.Meta.Pins)
	a.SetLog(sess)
	a.acceptMode().Replace(sess.Meta.Accept, func(am *session.AcceptMeta) { _ = sess.SetAccept(am) })
	if a.Cron != nil {
		// Restore skips what it cannot use and fails only on a stopped
		// scheduler, which has nothing to stop either.
		_ = a.Cron.Replace(sess.Meta.Loops, func(specs []session.LoopSpec) { _ = sess.SetLoops(specs) })
	}
	a.UndoPath = sess.UndoPath()
	a.checkpoints.reset()
	a.scopeReset()
	return a.LoadUndo()
}

// ForkSession moves the live conversation to ns, a fork of the current
// session: the conversation and its undo stack go with it, and the fork
// owns the log from here on — and the session record too: the /accept
// state and the armed /loop jobs were being saved through callbacks
// bound to the session left behind, so without re-pointing them every
// later /accept or /loop change went to the original and the fork's own
// record kept the state it was copied with.
func (a *Agent) ForkSession(ns *session.Session) error {
	if err := a.moveLease(ns); err != nil {
		return err
	}
	a.retireSubagents()
	a.SetLog(ns)
	a.acceptMode().SetOnChange(func(am *session.AcceptMeta) { _ = ns.SetAccept(am) })
	a.Cron.SetOnChange(func(specs []session.LoopSpec) { _ = ns.SetLoops(specs) })
	a.UndoPath = ns.UndoPath()
	return a.SaveUndo()
}

// retireSubagentsWait bounds how long a session switch waits for the
// subagents of the session it leaves to stop.
const retireSubagentsWait = 10 * time.Second

// retireSubagents ends the background subagents of the session being left,
// before its log is swapped for the next one's. A subagent belongs to the
// conversation that started it: left running, it would finish into the new
// session — its end, its attempt record, its merge notice and its token
// bill written into a history it was never part of. Cancelled and waited
// for here, while the log still points at the old session, its end is
// recorded there (attempt outcome "cancelled"), and its continuable
// children go with the conversation they were for.
func (a *Agent) retireSubagents() {
	t := a.TaskTool()
	if t == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), retireSubagentsWait)
	defer cancel()
	t.Close(ctx)
}

// moveLease takes to's lease and releases the current session's, when
// the agent's log is a session that holds one.
func (a *Agent) moveLease(to *session.Session) error {
	from, _ := a.Log.(*session.Session)
	if from == to {
		return nil
	}
	if err := to.Acquire(); err != nil {
		return err
	}
	if from != nil {
		from.Release()
	}
	return nil
}
