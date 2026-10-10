package proof

import "github.com/xmonader/janetcode/internal/slash/args"

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
	// ActionVerify checks the signed evidence on a commit and re-runs the
	// commands it records (see VerifyEvidence).
	ActionVerify Action = "verify"
	// ActionKey prints this machine's evidence public key as a line
	// another machine's trusted file takes.
	ActionKey Action = "key"
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
	// Static, for ActionVerify, checks the signature and which claims
	// have a record without re-running anything. A live session's /proof
	// verify sets it: re-running takes as long as the commands do, and a
	// slash command would hold the session for all of it.
	Static bool
}

// ParseArgs parses the text after "/proof" or "--proof": empty or a
// commit ("abc123", "HEAD~2") for ActionShow; "push [remote]" or "fetch
// [remote]" to move notes; "verify [commit]" to check a commit's signed
// evidence; "key" for this machine's evidence public key. Shared by every
// front end (the terminal, the web UI, and the CLI flag) so the shape is
// defined once.
func ParseArgs(s string) Args {
	t, _ := args.Tokenize(s, args.Spec{})
	fields := t.Positional
	if len(fields) == 0 {
		return Args{Action: ActionShow}
	}
	switch Action(fields[0]) {
	case ActionVerify:
		a := Args{Action: ActionVerify}
		if len(fields) > 1 {
			a.Commit = fields[1]
		}
		return a
	case ActionKey:
		return Args{Action: ActionKey}
	case ActionPush, ActionFetch:
		a := Args{Action: Action(fields[0]), Remote: DefaultRemote}
		if len(fields) > 1 {
			a.Remote = fields[1]
		}
		return a
	default:
		return Args{Action: ActionShow, Commit: fields[0]}
	}
}
