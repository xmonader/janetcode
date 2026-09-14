package agent

import (
	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/session"
)

// OpenSession makes sess the agent's session with history as the
// conversation. The log, the pins and the undo stack all come from sess,
// so nothing of the session being left behind — its checkpoints above
// all — is carried into the new one. /clear, /resume and their web
// equivalents all switch through here.
func (a *Agent) OpenSession(sess *session.Session, history []llm.Message) error {
	a.setMessages(history)
	a.Pins = append([]string(nil), sess.Meta.Pins...)
	a.SetLog(sess)
	a.UndoPath = sess.UndoPath()
	a.cp.mu.Lock()
	a.cp.all, a.cp.redo, a.cp.open, a.cp.dirty = nil, nil, false, false
	a.cp.changes, a.cp.seq = nil, 0
	a.cp.mu.Unlock()
	return a.LoadUndo()
}

// ForkSession moves the live conversation to ns, a fork of the current
// session: the conversation and its undo stack go with it, and the fork
// owns the log from here on.
func (a *Agent) ForkSession(ns *session.Session) error {
	a.SetLog(ns)
	a.UndoPath = ns.UndoPath()
	return a.SaveUndo()
}
