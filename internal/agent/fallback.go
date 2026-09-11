package agent

import (
	"context"
	"regexp"
	"strings"

	"github.com/xmonader/janetcode/internal/llm"
)

// A run can be lost to one model rather than to the work: a coding-plan
// quota that runs out mid-benchmark, a provider refusing every request
// for ten minutes. The fallback chain is the answer that does not need a
// person at the terminal — the same conversation continues on the next
// model in the list, and the session stays on it rather than walking
// into the same wall on every later call.

// MaxFallbacks is how many alternatives are kept. A longer list is a
// list nobody has checked the prices of.
const MaxFallbacks = 3

// FallbackChain cleans a configured list: trimmed, deduped, without the
// model already in use, capped at MaxFallbacks.
func FallbackChain(models []string, current string) []string {
	seen := map[string]bool{strings.TrimSpace(current): true}
	var out []string
	for _, m := range models {
		m = strings.TrimSpace(m)
		if m == "" || seen[m] {
			continue
		}
		seen[m] = true
		out = append(out, m)
		if len(out) == MaxFallbacks {
			break
		}
	}
	return out
}

var statusRe = regexp.MustCompile(`(?i)(?:http |status |status code |code |error )(\d{3})`)

// quotaWords are the ways a provider says the account is done for, as
// opposed to that this request arrived too fast.
var quotaWords = []string{
	"insufficient_quota", "quota exceeded", "exceeded your current quota",
	"insufficient balance", "available balance", "out of budget", "billing",
	"usage limit reached", "credit balance is too low", "credits",
	"allocated quota", "free allocated quota",
}

// worthFallback reports whether an error is one another model could
// survive: an exhausted account, or a rate limit or server error that
// outlived the provider's own retries. Everything else — a bad request, a
// context overflow, an unknown tool, the user's cancellation — would fail
// the same way on any model and is reported as it is.
func worthFallback(err error) bool {
	if err == nil || isContextOverflow(err) {
		return false
	}
	if errIsCancel(err) {
		return false
	}
	l := strings.ToLower(err.Error())
	for _, w := range quotaWords {
		if strings.Contains(l, w) {
			return true
		}
	}
	if strings.Contains(l, "rate limit") || strings.Contains(l, "overloaded") {
		return true
	}
	if m := statusRe.FindStringSubmatch(l); m != nil {
		switch code := m[1]; {
		case code == "429", code == "529", code == "402":
			return true
		case strings.HasPrefix(code, "5"):
			return true
		}
	}
	return false
}

func errIsCancel(err error) bool {
	return strings.Contains(err.Error(), context.Canceled.Error()) ||
		strings.Contains(err.Error(), context.DeadlineExceeded.Error())
}

// fallback retries the call on each configured model in turn after a
// failure the current one is not going to recover from. The failed
// attempt's partial stream is discarded — the history never saw it, and
// EvDiscard takes the half-written answer off the screen — so what the
// next model produces is the whole reply. A model that works becomes the
// session's model: the quota that just ran out will still be out on the
// next call.
func (a *Agent) fallback(ctx context.Context, cause error, emit func(Event)) (*llm.Response, error) {
	chain := FallbackChain(a.Fallbacks, a.Model)
	if len(chain) == 0 || !worthFallback(cause) || ctx.Err() != nil {
		return nil, cause
	}
	for _, name := range chain {
		prov, model := a.Provider, name
		if a.Route != nil {
			p, resolved, err := a.Route(name)
			if err != nil {
				emit(Event{Kind: EvNotice, Text: "cannot fall back to " + name + ": " + err.Error()})
				continue
			}
			prov, model = p, resolved
		}
		emit(Event{Kind: EvDiscard})
		emit(Event{Kind: EvNotice, Text: firstLineOf(cause.Error()) + " · falling back to " + model})
		emit(Event{Kind: EvPhase, Text: "falling back to " + model})
		resp, err := a.callOn(ctx, prov, model, emit)
		if err == nil {
			a.stats.fellBack()
			a.Provider, a.Model = prov, model
			emit(Event{Kind: EvNotice, Text: "this session continues on " + model + "; /model switches back"})
			return resp, nil
		}
		if ctx.Err() != nil || !worthFallback(err) {
			return nil, err
		}
		cause = err
	}
	return nil, cause
}
