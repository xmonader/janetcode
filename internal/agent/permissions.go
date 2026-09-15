package agent

import (
	"context"

	"github.com/xmonader/janetcode/internal/perm"
	"github.com/xmonader/janetcode/internal/shell"
)

// permissions is the collaborator that decides whether a tool call may
// run: the persistent rules, the approver that asks the user, the ledger
// that remembers how the same shape of prompt has been answered before,
// the session-wide allows the user granted, and the two things a prompt
// needs before it can be shown (the impact line and, for shell, the
// sandbox the command would run in). The decision itself is authorize
// (exec.go); this is what it decides with.
//
// Embedded in Agent by value, so Approver, Asker, Rules, SaveRule,
// Ledger, Impact and Sandbox keep their names for the front-ends that
// fill them in.
type permissions struct {
	// Approver decides mutating calls the modes and rules did not settle.
	Approver Approver
	// Asker, when set, answers labeled questions from the ask tool.
	Asker Asker
	// Rules are persistent permission rules consulted before the Approver:
	// a deny refuses the call outright, an allow skips the prompt.
	// Guarded by Agent.settingsMu, with the live settings.
	Rules perm.Rules
	// SaveRule, when set, persists a rule the user created with AllowAlways.
	SaveRule func(perm.Rule) error
	// Ledger, when set, remembers how permission prompts of each rule
	// shape have been answered, across sessions, so a call approved
	// several times running with no refusal can be flagged as learned
	// (ToolCall.Learned) and offered in /permissions before the user ever
	// saves a rule for it. Nil is a working default: perm.Ledger's
	// methods are nil-safe no-ops.
	Ledger *perm.Ledger
	// Impact, when set, is asked what an edit_file or write_file call
	// reaches — the symbols it redefines, their references, their tests,
	// and the checks that will run (see package impact) — and returns one
	// line for ToolCall.Impact, or "". It is only ever asked right before
	// a permission prompt is actually shown, on a short internal budget,
	// so it never delays one noticeably.
	Impact func(ctx context.Context, call ToolCall) string
	// Sandbox, when set, is the optional wrapper around local bash.
	Sandbox *shell.Sandbox

	// allowed are the tools the user allowed for the rest of the session
	// (Decision AllowSession).
	allowed map[string]bool
}

// allowSession marks name as allowed for the rest of this session,
// initialising the map on first use.
//
// The map is created here rather than at the start of a turn, and that is
// the thing that makes this safe: RunRecipe (see recipe.go) drives calls
// through authorize without ever going through RunBlocks's turn setup, so
// initialising only there left a straight-line path — a fresh agent whose
// very first action is RunRecipe, with an approver that answers
// AllowSession — straight into an assignment on a nil map. Initialising
// here, at the point the map is actually written, is safe under every
// caller rather than only the ones remembered to set it up first.
func (p *permissions) allowSession(name string) {
	if p.allowed == nil {
		p.allowed = map[string]bool{}
	}
	p.allowed[name] = true
}
