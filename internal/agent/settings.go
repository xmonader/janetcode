package agent

import (
	"slices"
	"strings"

	"github.com/xmonader/janetcode/internal/hook"
	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/perm"
	"github.com/xmonader/janetcode/internal/verify"
)

// Settings is the part of an Agent's configuration a front-end may change
// while a turn is running: the approval and session modes (shift+tab,
// /yolo, /auto, /edits, /readonly, /plan), the keys /config sets, and the
// model, provider, price and context window /model and /connect switch
// (see SwitchModel). Each field mirrors the Agent field of the same name.
//
// The Agent fields are what a caller fills in when it builds an agent,
// before any turn runs. From then on they are read and written only under
// settingsMu: front-ends change them with UpdateSettings (or SetPlanMode),
// and the turn goroutine — and a subagent starting on a goroutine of its
// own — reads a copy with Settings. Reading one of these fields directly
// while a turn may be running is a data race.
type Settings struct {
	Model         string
	Provider      llm.Provider
	Price         *llm.Price
	ContextWindow int
	MaxTokens     int
	Effort        string
	MaxIterations int
	CompactAt     int
	AutoApprove   bool
	AutoEdits     bool
	AutoMode      bool
	ReadOnly      bool
	PlanMode      bool
	ProofNotes    bool
}

// Settings is a consistent copy of the live settings, safe to take from
// any goroutine.
func (a *Agent) Settings() Settings {
	a.settingsMu.Lock()
	defer a.settingsMu.Unlock()
	return a.settingsLocked()
}

func (a *Agent) settingsLocked() Settings {
	return Settings{
		Model:         a.Model,
		Provider:      a.Provider,
		Price:         a.Price,
		ContextWindow: a.ContextWindow,
		MaxTokens:     a.MaxTokens,
		Effort:        a.Effort,
		MaxIterations: a.MaxIterations,
		CompactAt:     a.CompactAt,
		AutoApprove:   a.AutoApprove,
		AutoEdits:     a.AutoEdits,
		AutoMode:      a.AutoMode,
		ReadOnly:      a.ReadOnly,
		PlanMode:      a.PlanMode,
		ProofNotes:    a.ProofNotes,
	}
}

// RoutingOff reports whether step routing is vetoed for this session
// (/routing off). It takes settingsMu, the same lock ToggleOff writes
// under, so a toggle cannot land in the middle of the turn goroutine's
// read in routeStep.
func (a *Agent) RoutingOff() bool {
	a.settingsMu.Lock()
	defer a.settingsMu.Unlock()
	return a.offLocked("routing")
}

// offLocked is a switch's live state: as Off set it at build time,
// flipped once for every ToggleOff since. Called with settingsMu held.
func (a *Agent) offLocked(name string) bool {
	return a.Off[name] != a.offToggled[name]
}

// ToggleOff flips one ablation switch and reports the new state, so a
// front-end can veto a named behaviour for the rest of the session
// without a rebuild. Only the switch this pair guards — "routing" —
// is read under the lock at runtime (routeStep via RoutingOff). The flip
// is recorded beside Off rather than in it: every other Off entry is read
// on the turn goroutine without a lock, and writing the map those reads
// look in — even under a key they never ask for — raced them.
func (a *Agent) ToggleOff(name string) bool {
	a.settingsMu.Lock()
	defer a.settingsMu.Unlock()
	if a.offToggled == nil {
		a.offToggled = map[string]bool{}
	}
	a.offToggled[name] = !a.offToggled[name]
	return a.offLocked(name)
}

// UpdateSettings changes the live settings atomically: change sees the
// current values and every field it sets takes effect together, so a mode
// switch that turns one flag on and two off is never observed half done.
// It is safe to call while a turn runs; the turn sees the change from its
// next read. Plan mode should change through SetPlanMode, which also logs
// it for resume.
func (a *Agent) UpdateSettings(change func(*Settings)) {
	a.settingsMu.Lock()
	defer a.settingsMu.Unlock()
	s := a.settingsLocked()
	change(&s)
	a.Model = s.Model
	a.Provider = s.Provider
	a.Price = s.Price
	a.ContextWindow = s.ContextWindow
	a.MaxTokens = s.MaxTokens
	a.Effort = s.Effort
	a.MaxIterations = s.MaxIterations
	a.CompactAt = s.CompactAt
	a.AutoApprove = s.AutoApprove
	a.AutoEdits = s.AutoEdits
	a.AutoMode = s.AutoMode
	a.ReadOnly = s.ReadOnly
	a.PlanMode = s.PlanMode
	a.ProofNotes = s.ProofNotes
}

// SwitchModel moves the live session to model, safe while a turn runs:
// onto prov when it is not nil (the model routes to another provider),
// with price and window as the model's price and context window. /model
// and /connect in every front-end switch through it (via settingsapply,
// which looks the price and window up in the config), so the four change
// together.
func (a *Agent) SwitchModel(model string, prov llm.Provider, price *llm.Price, window int) {
	a.UpdateSettings(func(s *Settings) {
		s.Model = model
		if prov != nil {
			s.Provider = prov
		}
		s.Price = price
		s.ContextWindow = window
	})
}

// SetVerify replaces the project checks the agent runs after a turn that
// changed something (nil runs none) and records whether checks exist but
// are untrusted, safe while a turn runs: /verify set, add, clear and
// trust go through it. The runner must not be changed after the call.
func (a *Agent) SetVerify(gate *verify.Runner, untrusted bool) {
	a.settingsMu.Lock()
	defer a.settingsMu.Unlock()
	a.Verify, a.VerifyUntrusted = gate, untrusted
}

// VerifyGate is the live Verify runner and VerifyUntrusted, safe to read
// while a turn runs.
func (a *Agent) VerifyGate() (gate *verify.Runner, untrusted bool) {
	a.settingsMu.Lock()
	defer a.settingsMu.Unlock()
	return a.Verify, a.VerifyUntrusted
}

// SetHooks replaces the hook runner (nil runs none), safe while a turn
// runs: /hooks trust goes through it. The runner must not be changed
// after the call; build a new one instead.
func (a *Agent) SetHooks(h *hook.Runner) {
	a.settingsMu.Lock()
	defer a.settingsMu.Unlock()
	a.Hooks = h
}

// HookRunner is the live Hooks, safe to read while a turn runs.
func (a *Agent) HookRunner() *hook.Runner {
	a.settingsMu.Lock()
	defer a.settingsMu.Unlock()
	return a.Hooks
}

// SetMaxCost sets the spend ceiling in USD (zero removes it), safe while
// a turn runs: /budget goes through it.
func (a *Agent) SetMaxCost(usd float64) {
	a.settingsMu.Lock()
	defer a.settingsMu.Unlock()
	a.MaxCostUSD = usd
}

// MaxCost is the live MaxCostUSD, safe to read while a turn runs.
func (a *Agent) MaxCost() float64 {
	a.settingsMu.Lock()
	defer a.settingsMu.Unlock()
	return a.MaxCostUSD
}

// readOnly reports whether mutating tools are hidden and refused: in
// read-only mode and in plan mode.
func (s Settings) readOnly() bool { return s.ReadOnly || s.PlanMode }

// modeName names the mode that makes the session read-only, for messages.
func (s Settings) modeName() string {
	if s.PlanMode {
		return "plan mode"
	}
	return "read-only mode"
}

// RulesSnapshot is a copy of the permission rules, safe to take from any
// goroutine.
func (a *Agent) RulesSnapshot() perm.Rules {
	a.settingsMu.Lock()
	defer a.settingsMu.Unlock()
	return perm.Rules{Allow: slices.Clone(a.Rules.Allow), Deny: slices.Clone(a.Rules.Deny)}
}

// AddRule adds a permission rule to the live set (see perm.Rules.Add),
// safe while a turn runs. It reports whether the rule was new.
func (a *Agent) AddRule(v perm.Verdict, r perm.Rule) bool {
	a.settingsMu.Lock()
	defer a.settingsMu.Unlock()
	return a.Rules.Add(v, r)
}

// MergeRules adds every rule of extra to the live set (see perm.Merge),
// safe while a turn runs: /permissions trust uses it for the project's
// rules.
func (a *Agent) MergeRules(extra perm.Rules) {
	a.settingsMu.Lock()
	defer a.settingsMu.Unlock()
	a.Rules = perm.Merge(a.Rules, extra)
}

// decideCall is Rules.DecideCall under the settings lock. The call
// carries its input, so a parameter rule (see perm.Params) can read the
// fields it names. The third result is a hard deny's reason, set only
// when the call is refused whatever the rules say.
func (a *Agent) decideCall(c perm.Call) (perm.Verdict, perm.Rule, string) {
	a.settingsMu.Lock()
	defer a.settingsMu.Unlock()
	return a.Rules.DecideCall(c)
}

// PinsSnapshot is a copy of the pinned notes, safe to take from any
// goroutine.
func (a *Agent) PinsSnapshot() []string {
	a.settingsMu.Lock()
	defer a.settingsMu.Unlock()
	return slices.Clone(a.Pins)
}

// SetPins replaces the pinned notes, as when a different session opens.
func (a *Agent) SetPins(pins []string) {
	a.settingsMu.Lock()
	defer a.settingsMu.Unlock()
	a.Pins = slices.Clone(pins)
}

// Pin keeps a note in the system prompt for the rest of the session. It
// reports how many pins there are afterwards.
func (a *Agent) Pin(text string) int {
	a.settingsMu.Lock()
	defer a.settingsMu.Unlock()
	if text = strings.TrimSpace(text); text != "" {
		a.Pins = append(a.Pins, text)
	}
	return len(a.Pins)
}

// Unpin removes the n-th pin (1-based); it reports whether one existed.
func (a *Agent) Unpin(n int) bool {
	a.settingsMu.Lock()
	defer a.settingsMu.Unlock()
	if n < 1 || n > len(a.Pins) {
		return false
	}
	a.Pins = slices.Delete(a.Pins, n-1, n)
	return true
}

// SetRules replaces the permission rules wholesale, safe while a turn
// runs: /cd rebuilds them from the configuration plus whatever the
// directory it moved to allows, so the tree left behind stops granting
// anything.
func (a *Agent) SetRules(r perm.Rules) {
	a.settingsMu.Lock()
	defer a.settingsMu.Unlock()
	a.Rules = r
}

// SetLedger replaces the per-project record of how permission prompts
// were answered. It is keyed by project, so /cd opens the new one.
func (a *Agent) SetLedger(l *perm.Ledger) {
	a.settingsMu.Lock()
	defer a.settingsMu.Unlock()
	a.Ledger = l
}

// PermLedger is the live Ledger, safe to read while a turn runs: /cd
// swaps it from a front-end goroutine while the turn goroutine is
// recording approvals into it. Nil is a working value — perm.Ledger's
// methods are nil-safe — so callers need not check.
func (a *Agent) PermLedger() *perm.Ledger {
	a.settingsMu.Lock()
	defer a.settingsMu.Unlock()
	return a.Ledger
}
