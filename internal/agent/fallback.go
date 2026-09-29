package agent

import (
	"context"
	"errors"
	"regexp"
	"strconv"
	"strings"

	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/textfmt"
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

// worthFallback reports whether an error is one another model could
// survive: an exhausted account, or a rate limit or server error that
// outlived the provider's own retries. Everything else — a bad request, a
// context overflow, an unknown tool, the user's cancellation — would fail
// the same way on any model and is reported as it is.
//
// An error that carries its HTTP status is judged by the status and the
// server's whole text (llm.ClassifyText), not by Error(): that is cut for
// display, and an "insufficient balance" past the cut was missed. Only an
// error with no status — a failure reported inside the stream — falls
// back to reading its words.
func worthFallback(err error) bool {
	if err == nil || isContextOverflow(err) || errIsCancel(err) {
		return false
	}
	text := llm.ClassifyText(err)
	if llm.PermanentLimit(text) {
		return true
	}
	var se llm.StatusError
	if errors.As(err, &se) && se.HTTPStatus() != 0 {
		return fallbackStatus(se.HTTPStatus())
	}
	l := strings.ToLower(text)
	if strings.Contains(l, "rate limit") || strings.Contains(l, "overloaded") {
		return true
	}
	if m := statusRe.FindStringSubmatch(l); m != nil {
		code, _ := strconv.Atoi(m[1])
		return fallbackStatus(code)
	}
	return false
}

// fallbackStatus reports whether a status says this model cannot serve the
// request now: too many requests, payment required, overloaded, or a
// server error.
func fallbackStatus(code int) bool {
	return code == 429 || code == 402 || code == 529 || code >= 500
}

// errIsCancel reports whether err is a cancellation or a deadline, which
// the providers return wrapped (%w) rather than restated.
func errIsCancel(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

// fallback retries the call on each configured model in turn after a
// failure the current one is not going to recover from. The failed
// attempt's partial stream is discarded — the history never saw it, and
// EvDiscard takes the half-written answer off the screen — so what the
// next model produces is the whole reply. A model that works becomes the
// session's model: the quota that just ran out will still be out on the
// next call.
func (a *Agent) fallback(ctx context.Context, cause error, emit func(Event)) (*llm.Response, error) {
	chain := FallbackChain(a.Fallbacks, a.Settings().Model)
	if len(chain) == 0 || !worthFallback(cause) || ctx.Err() != nil {
		return nil, cause
	}
	for _, name := range chain {
		prov, model := a.Settings().Provider, name
		if a.Route != nil {
			p, resolved, err := a.Route(name)
			if err != nil {
				emit(Event{Kind: EvNotice, Text: "cannot fall back to " + name + ": " + err.Error()})
				continue
			}
			// A router that answers with no provider means "the one
			// already in use serves it" — taking it literally would send
			// the call to a nil provider. sideQuery reads it the same way.
			if p != nil {
				prov = p
			}
			if resolved != "" {
				model = resolved
			}
		}
		emit(Event{Kind: EvDiscard})
		emit(Event{Kind: EvNotice, Text: textfmt.FirstLine(cause.Error(), 70) + " · falling back to " + model})
		emit(Event{Kind: EvPhase, Text: "falling back to " + model})
		resp, err := a.callOn(ctx, prov, model, callRoute{slot: "fallback", kind: "fallback", why: textfmt.FirstLine(cause.Error(), 70)}, emit)
		if err == nil {
			a.stats.fellBack()
			// The price moves with the model: Settings.Price is what
			// PriceOf answers for the session's model, and the old one
			// left in place billed the fallback at the replaced model's
			// rate. Nothing to look it up in leaves it unpriced, which
			// says so, rather than wrong, which does not.
			// The window likewise: it sizes compaction and the
			// oversized-output guard for the model now in use.
			var price *llm.Price
			if a.Prices != nil {
				price = a.Prices(model)
			}
			window := a.WindowOf(model)
			a.UpdateSettings(func(s *Settings) {
				s.Provider, s.Model, s.Price, s.ContextWindow = prov, model, price, window
			})
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
