package proof

import "strings"

// Action is what a /proof invocation (or `janetcode --proof`) asks for.
type Action string

const (
	// ActionShow shows (attaching first if needed) the proof note on a
	// commit. It is the default when no subcommand is given.
	ActionShow Action = "show"
	// ActionPush pushes this repository's proof notes to a remote.
	ActionPush Action = "push"
	// ActionFetch fetches a remote's proof notes into this repository,
	// and configures the remote to bring them on every future fetch too.
	ActionFetch Action = "fetch"
)

// DefaultRemote is used by Push and Fetch (and filled in by ParseArgs)
// when no remote is named.
const DefaultRemote = "origin"

// Args is one parsed /proof (or `janetcode --proof`) invocation.
type Args struct {
	Action Action
	// Commit names the commit to show, for ActionShow. Empty means the
	// caller's own default (HEAD).
	Commit string
	// Remote names the remote to push to or fetch from, for ActionPush
	// and ActionFetch. Always filled in (DefaultRemote when none named).
	Remote string
}

// ParseArgs parses the text after "/proof" or "--proof": empty or a
// commit ("abc123", "HEAD~2") for ActionShow; "push [remote]" or "fetch
// [remote]" for the other two. Shared by every front end (the terminal,
// the web UI, and the CLI flag) so the shape is defined once.
func ParseArgs(s string) Args {
	fields := strings.Fields(s)
	if len(fields) == 0 {
		return Args{Action: ActionShow}
	}
	switch fields[0] {
	case "push":
		a := Args{Action: ActionPush, Remote: DefaultRemote}
		if len(fields) > 1 {
			a.Remote = fields[1]
		}
		return a
	case "fetch":
		a := Args{Action: ActionFetch, Remote: DefaultRemote}
		if len(fields) > 1 {
			a.Remote = fields[1]
		}
		return a
	default:
		return Args{Action: ActionShow, Commit: fields[0]}
	}
}
