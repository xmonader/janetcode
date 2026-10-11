package agent

import (
	"fmt"
	"path/filepath"
	"strings"
	"sync"

	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/scope"
	"github.com/xmonader/janetcode/internal/session"
)

// Scope contracts (internal/scope): the expected blast radius of the
// session's requests — the files they name, those files' package,
// importers and tests — inferred from the tree the moment the first edit
// of a request lands, before that edit can widen it. An edit outside the
// contract is never refused. It is recorded, once per path, on the
// session log as a "scope" notice carrying the edit's provenance intent
// as its justification, and a headless run reports the share of its
// edits that went outside (CLIResult.Scope) as a metric.
//
// Deterministic and model-free, so on by default; JANETCODE_OFF=scope
// turns it off. ScopeJustify (config scope_justify) additionally appends
// one line to the result of an out-of-scope edit that came with no stated
// reason, asking for one; the model's next words are then recorded as the
// justification. That line changes what the model sees, so it is opt-in.

// scopeLedger is the session's contract and what has been recorded
// against it.
type scopeLedger struct {
	mu sync.Mutex
	// texts are the requests the contract was inferred from, joined;
	// a request that adds text re-infers it.
	texts    []string
	contract *scope.Contract
	// noted are the paths already recorded as outside, so a file edited
	// ten times is one notice.
	noted map[string]bool
}

// scopeOn reports whether scope contracts are kept.
func (a *Agent) scopeOn() bool { return !a.Off["scope"] }

// scopeContract is the contract for the session's requests so far,
// inferred (again) when the current request is one it has not seen.
// created are the paths the session has made, which the walk leaves out.
func (a *Agent) scopeContract(root string) *scope.Contract {
	req := strings.TrimSpace(a.integ.userText)
	a.scopes.mu.Lock()
	defer a.scopes.mu.Unlock()
	if req != "" && (len(a.scopes.texts) == 0 || a.scopes.texts[len(a.scopes.texts)-1] != req) {
		a.scopes.texts = append(a.scopes.texts, req)
		a.scopes.contract = nil
	}
	if a.scopes.contract == nil && len(a.scopes.texts) > 0 {
		c := scope.Infer(root, strings.Join(a.scopes.texts, "\n\n"), a.scopeCreated(root)...)
		a.scopes.contract = &c
	}
	return a.scopes.contract
}

// scopeCreated is every path, relative to root, the session's tool calls
// created rather than changed.
func (a *Agent) scopeCreated(root string) []string {
	var out []string
	for _, c := range a.Changes() {
		if c.Existed {
			continue
		}
		if rel, ok := scopeRel(root, c.Path); ok {
			out = append(out, rel)
		}
	}
	return out
}

func scopeRel(root, p string) (string, bool) {
	if !filepath.IsAbs(p) {
		return filepath.ToSlash(p), true
	}
	rel, err := filepath.Rel(root, p)
	if err != nil || strings.HasPrefix(rel, "..") {
		return "", false
	}
	return filepath.ToSlash(rel), true
}

// scopeAfter records the paths a mutating call just changed that fall
// outside the contract, and returns the text to append to the call's
// result: a request for a one-line reason when ScopeJustify is on and the
// call stated none, else "". pend is what before() read, so a path is
// only counted when the call did change it — the provenance record
// (after) has already been written by then.
func (a *Agent) scopeAfter(u llm.Block, intent string, pend []pending) string {
	if !a.scopeOn() || len(pend) == 0 || a.DryRunOn() {
		return ""
	}
	root := a.RootDir()
	// The contract must not see what this call created: infer it over
	// the tree with this call's new files left out (scopeCreated reads
	// the provenance record, which after() already extended).
	c := a.scopeContract(root)
	if c == nil || c.Empty() {
		return ""
	}
	changed := map[string]bool{}
	for _, ch := range a.Changes() {
		if ch.Call == u.ID {
			changed[ch.Path] = true
		}
	}
	why := intentLine(strings.TrimSpace(intent), 120)
	if why == "" {
		why = a.intentFor(u.ID, "")
	}
	var unjustified []string
	for _, p := range pend {
		if !changed[p.path] {
			continue
		}
		rel, ok := scopeRel(root, p.path)
		if !ok || scope.Incidental(rel) || c.Covers(rel) {
			continue
		}
		a.scopes.mu.Lock()
		if a.scopes.noted == nil {
			a.scopes.noted = map[string]bool{}
		}
		first := !a.scopes.noted[rel]
		a.scopes.noted[rel] = true
		a.scopes.mu.Unlock()
		if !first {
			continue
		}
		out := "outside the task's inferred scope; justification: " + why
		if why == "" {
			out = "outside the task's inferred scope; no justification given"
			unjustified = append(unjustified, rel)
		}
		a.logEvent(session.Event{Type: session.TypeNotice, CallID: u.ID, Name: "scope", Arguments: rel, Output: out})
	}
	if !a.ScopeJustifyOn() || len(unjustified) == 0 {
		return ""
	}
	return fmt.Sprintf("\n\n[scope] %s is outside what the task names (%s). If the change is needed, say why in one line.",
		strings.Join(unjustified, ", "), scopeNamed(c.Mentioned, 4))
}

// scopeNamed lists up to n of the contract's mentions for a note.
func scopeNamed(m []string, n int) string {
	if len(m) <= n {
		return strings.Join(m, ", ")
	}
	return strings.Join(m[:n], ", ") + fmt.Sprintf(" and %d more", len(m)-n)
}

// ScopeReport measures the session's edits against its scope contract:
// nil when scope contracts are off or the requests named nothing to infer
// one from. An out-of-scope path whose edit stated no reason takes, as
// its justification, the first thing the model said after the scope
// notice for it — the answer to ScopeJustify's question when that is on.
func (a *Agent) ScopeReport() *scope.Report {
	if !a.scopeOn() {
		return nil
	}
	a.scopes.mu.Lock()
	c := a.scopes.contract
	a.scopes.mu.Unlock()
	if c == nil {
		return nil
	}
	r := scope.Measure(*c, a.RootDir(), a.Changes())
	if r == nil || a.Log == nil {
		return r
	}
	answers := scopeAnswers(a.Log.Events())
	for i, o := range r.Outside {
		if o.Unjustified && answers[o.Path] != "" {
			r.Outside[i].Justification, r.Outside[i].Unjustified = answers[o.Path], false
		}
	}
	return r
}

// scopeAnswers maps each path a scope notice recorded as unjustified to
// the first line of text the model wrote after that notice.
func scopeAnswers(events []session.Event) map[string]string {
	out := map[string]string{}
	var waiting []string
	for _, e := range events {
		switch {
		case e.Type == session.TypeNotice && e.Name == "scope" && strings.Contains(e.Output, "no justification"):
			waiting = append(waiting, e.Arguments)
		case e.Type == session.TypeAssistant && e.Message != nil && len(waiting) > 0:
			text := ""
			for _, b := range e.Message.Blocks {
				if b.Type == llm.BlockText && strings.TrimSpace(b.Text) != "" {
					text = intentLine(b.Text, 160)
					break
				}
			}
			if text == "" {
				continue
			}
			for _, p := range waiting {
				out[p] = text
			}
			waiting = nil
		}
	}
	return out
}

// SetScopeJustify turns the justification request on or off for the
// calls that follow (/config scope_justify).
func (a *Agent) SetScopeJustify(on bool) {
	a.featMu.Lock()
	a.ScopeJustify = on
	a.featMu.Unlock()
}

// ScopeJustifyOn is ScopeJustify, safe to read while a turn runs, and off
// when JANETCODE_OFF names scope.
func (a *Agent) ScopeJustifyOn() bool {
	a.featMu.RLock()
	defer a.featMu.RUnlock()
	return a.ScopeJustify && a.scopeOn()
}

// scopeReset forgets the contract, for a session switch: the next
// session's requests infer their own.
func (a *Agent) scopeReset() {
	a.scopes.mu.Lock()
	a.scopes.texts, a.scopes.contract, a.scopes.noted = nil, nil, nil
	a.scopes.mu.Unlock()
}
