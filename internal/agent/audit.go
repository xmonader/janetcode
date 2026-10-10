package agent

import (
	"crypto/sha256"
	"encoding/hex"

	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/perm"
	"github.com/xmonader/janetcode/internal/session"
	"github.com/xmonader/janetcode/internal/tool"
)

// String is the decision as the audit record and the log write it.
func (d Decision) String() string {
	switch d {
	case Allow:
		return "allow"
	case AllowSession:
		return "allow for the session"
	case AllowAlways:
		return "allow and save a rule"
	}
	return "deny"
}

// logPermission records how one call was authorized: what settled it
// (a rule, a mode, a hook, the user) and whether it may run. A read-only
// call nobody refused is authorized by being read-only and is not
// recorded, which keeps the log to the calls that could change anything.
func (a *Agent) logPermission(t tool.Tool, u *llm.Block, basis, refusal string, err error) {
	if err != nil && refusal == "" {
		return // cancelled: the tool result says so
	}
	if refusal == "" && !tool.MutatingCall(t, u.Input) {
		return
	}
	if basis == "" {
		basis = "unknown"
	}
	e := session.Event{Type: session.TypePermission, CallID: u.ID, Name: u.Name, Reason: basis, Denied: refusal != ""}
	if refusal != "" {
		e.Output = refusal
	}
	a.logEvent(e)
}

// Unattended is an Approver that can say it has no one to ask: a
// headless run whose stdin is not a terminal answers every question with
// a deny it made itself. The audit then records that, not a person.
type Unattended interface {
	Unattended() bool
}

// answerBasis is what the audit records for an approver's answer: the
// person's, or the refusal of a run with no one to ask.
func (a *Agent) answerBasis(d Decision) string {
	if u, ok := a.Approver.(Unattended); ok && u.Unattended() {
		return "headless: no one to ask (" + d.String() + ")"
	}
	return "the user: " + d.String()
}

// shortcutBasis names the shortcut that let a mutating call run without
// asking, in the order mustPrompt checks them.
func (a *Agent) shortcutBasis(t tool.Tool, u *llm.Block, verdict perm.Verdict, rule perm.Rule, hookAllowed bool) string {
	if !tool.MutatingCall(t, u.Input) {
		return "read-only"
	}
	modes := a.Settings()
	switch {
	case modes.AutoApprove:
		return "mode: yolo"
	case (modes.AutoEdits || modes.AutoMode) && editTool(u.Name):
		if modes.AutoMode {
			return "mode: auto"
		}
		return "mode: auto-edits"
	case modes.AutoMode && a.autoAllows(*u):
		return "mode: auto (known-safe)"
	case a.sessionAllows(u.Name):
		return "allowed for the session"
	case verdict == perm.Allowed:
		return "rule " + rule.String()
	case hookAllowed:
		return "pre-tool hook"
	case a.purityAllows(*u):
		return "observed read-only"
	}
	return "unknown"
}

// contentDigest is a file's content as the audit record names it: its
// sha256, or "absent" when there was no file.
func contentDigest(data []byte, exists bool) string {
	if !exists {
		return "absent"
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
