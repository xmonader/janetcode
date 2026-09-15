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
	ns, err := New(dir, from.Meta.Cwd, from.Meta.Provider, from.Meta.Model)
	if err != nil {
		return nil, err
	}
	ns.Meta.Pins = append([]string(nil), from.Meta.Pins...)
	ns.Meta.ForkOf = from.Meta.ID
	if from.Meta.Start != nil {
		start := *from.Meta.Start
		ns.Meta.Start = &start
	}
	if title = strings.TrimSpace(title); title == "" {
		title = "fork of " + from.Label()
	}
	ns.Meta.Title = title
	ns.ev = from.Events()
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
