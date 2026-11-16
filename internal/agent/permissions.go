package agent

import (
	"context"
	"sort"
	"sync"

	"github.com/xmonader/janetcode/internal/egress"
	"github.com/xmonader/janetcode/internal/managed"
	"github.com/xmonader/janetcode/internal/perm"
	"github.com/xmonader/janetcode/internal/present"
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
//
// Ownership, since a turn goroutine reads all of this while a front-end
// goroutine may be changing it:
//
//   - Rules and Ledger are guarded by Agent.settingsMu, with the live
//     settings. Read them through RulesSnapshot / decideCall and
//     PermLedger, never off the field.
//   - Approver, Asker, SaveRule, Impact, Sandbox and Protected are set
//     when the agent is built (and copied onto a subagent when one is
//     built) and never assigned again, so they need no lock. Anything
//     that wants to change one of them at run time has to grow a setter
//     under settingsMu first, the way SetLedger did.
type permissions struct {
	// Approver decides mutating calls the modes and rules did not settle.
	Approver Approver
	// Asker, when set, answers labeled questions from the ask tool.
	Asker Asker
	// Unattended marks a session nobody is watching as it runs (a --bg
	// run): a question waits only the unattended time before the turn
	// goes on with the recommendation (see askwait.go).
	Unattended bool
	askWait    askWait
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
	// Protected, when set, is the set of paths a write to always asks
	// about, whatever the mode or the rules say (see perm.Protected):
	// shell startup files, git config and hooks, credentials, keys, and
	// janetcode's own permission record. Nil protects nothing, which is
	// what a test agent that never sets one up wants.
	Protected *perm.Protected
	// Policy, when set, is the administrator's managed policy: the modes
	// it disables stay off whatever a front-end or the model asks for
	// (see UpdateSettings). Nil is no policy.
	Policy *managed.Policy
	// EgressPolicy, when active, decides which hosts a tool call may send
	// data to: a blocked host is refused before the call runs, an "ask"
	// host makes the call ask whatever the mode. Nil allows every host.
	EgressPolicy *egress.Policy

	egressMu  sync.Mutex
	egressRec *egress.Recorder
	egressAux []egress.Report // after-turn memory calls (RecordMemoryCall)

	// allowed are the tools the user allowed for the rest of the session
	// (Decision AllowSession). allowedMu guards it: a turn writes it while
	// a front-end may be reading it for /permissions.
	allowedMu sync.Mutex
	allowed   map[string]bool
}

// sessionAllows reports whether name was allowed for the session.
func (p *permissions) sessionAllows(name string) bool {
	p.allowedMu.Lock()
	defer p.allowedMu.Unlock()
	return p.allowed[name]
}

// SessionAllowed lists the tools allowed for the rest of this session,
// sorted.
func (p *permissions) SessionAllowed() []string {
	p.allowedMu.Lock()
	defer p.allowedMu.Unlock()
	var out []string
	for n := range p.allowed {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
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
	p.allowedMu.Lock()
	defer p.allowedMu.Unlock()
	if p.allowed == nil {
		p.allowed = map[string]bool{}
	}
	p.allowed[name] = true
}

// Permissions gathers what decides a tool call — mode, rules, session
// allowances, protected paths, sandbox — for present.PermissionSummary.
func (a *Agent) Permissions() present.Permissions {
	m := a.Settings()
	rs := a.RulesSnapshot()
	p := present.Permissions{
		AutoApprove: m.AutoApprove, AutoMode: m.AutoMode, AutoEdits: m.AutoEdits,
		ReadOnly: m.ReadOnly, PlanMode: m.PlanMode,
		SessionAllowed: a.SessionAllowed(),
	}
	for _, r := range rs.Allow {
		p.Allow = append(p.Allow, r.String())
	}
	for _, r := range rs.Deny {
		p.Deny = append(p.Deny, r.String())
	}
	for _, r := range rs.Ask {
		p.Ask = append(p.Ask, r.String())
	}
	p.Managed = a.Policy.Summary()
	if a.Protected != nil && !a.Off["protected"] {
		p.Protected = len(a.Protected.Patterns())
	}
	if a.Sandbox.Enabled() {
		p.Sandbox = a.Sandbox.ProfileName()
	}
	return p
}
