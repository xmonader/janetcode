// Forking a session: /fork copies a conversation so a branch of it can be
// explored without disturbing the original.
package session

import "strings"

// StayArg is the /fork stay form: copy the conversation but keep this
// session live. Plain /fork switches into the copy. One command, two
// destinations.
func StayArg(arg string) (stay bool, title string) {
	arg = strings.TrimSpace(arg)
	if arg == "stay" {
		return true, ""
	}
	if rest, ok := strings.CutPrefix(arg, "stay "); ok {
		return true, strings.TrimSpace(rest)
	}
	return false, arg
}

// Fork starts a new session holding a copy of this one's messages, so a
// conversation can branch: try one approach here, another in the fork.
func Fork(dir string, from *Session, title string) (*Session, error) {
	// One snapshot of the parent under its lock: the UI goroutine that
	// forks is not the one that appends or rewrites metadata.
	from.mu.Lock()
	meta := from.Meta
	events := append([]Event(nil), from.ev...)
	from.mu.Unlock()
	ns, err := New(dir, meta.Cwd, meta.Provider, meta.Model)
	if err != nil {
		return nil, err
	}
	ns.Meta.Pins = append([]string(nil), meta.Pins...)
	ns.Meta.ForkOf = meta.ID
	if meta.Start != nil {
		start := *meta.Start
		ns.Meta.Start = &start
	}
	// What the session was working under goes with the conversation: the
	// extra roots its file tools could reach, its armed loops and its
	// /accept lock. Without them a resumed fork lost the directories it
	// had been editing and, worse, the lock on the acceptance tests —
	// the tests it was approved against became editable again.
	ns.Meta.ExtraDirs = append([]string(nil), meta.ExtraDirs...)
	ns.Meta.Loops = append([]LoopSpec(nil), meta.Loops...)
	ns.Meta.Accept = meta.Accept.Clone()
	if title = strings.TrimSpace(title); title == "" {
		label := meta.Title
		if label == "" {
			label = meta.ID
		}
		title = "fork of " + label
	}
	ns.Meta.Title = title
	ns.ev = events
	for _, e := range ns.ev {
		if e.Seq > ns.nextSeq {
			ns.nextSeq = e.Seq
		}
	}
	if err := ns.rewrite(); err != nil {
		return nil, err
	}
	return ns, nil
}
