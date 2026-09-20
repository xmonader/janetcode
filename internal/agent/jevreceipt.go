package agent

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/xmonader/janetcode/internal/session"
	"github.com/xmonader/janetcode/internal/tool"
)

// Evidence-preserving receipt (SoL-Pi's EPR, Phase 0 prototype verified
// 2026-09-20, see docs/notes/jev.md): when a long tool output would be
// stubbed head+tail against the inline budget, one cheap side call may
// reduce it to a receipt — prose plus verbatim quoted lines. The
// receipt is accepted only if EVERY quoted line is a byte-exact
// substring of the original output; otherwise the head+tail stub stands
// untouched. That is the fail-open guarantee, and it is what makes a
// verified receipt trustworthy in a repair decision: it cannot smuggle
// in a number the output does not contain.
//
// The gate is all-or-nothing. A receipt with three exact quotes and one
// invented is discarded whole: a partially trusted receipt is exactly
// how a wrong number reaches a repair decision.
const receiptSystem = "You reduce a build/test log to a receipt for an agent. Keep ONLY what a repair decision needs: " +
	"test names and failure lines verbatim, plus numbers the decision needs (counts, durations, exit codes). " +
	"You may quote at most 12 lines. First 1-3 sentences of prose with no quotes in them. " +
	"Then the most load-bearing lines quoted EXACTLY as they appear in the log, each on its own line prefixed 'Q: '. " +
	"Never paraphrase a quoted line. Never invent a line."
const receiptBudget = 8 * time.Second
const receiptMaxQuotes = 20

// receiptify renders the receipt for one oversized output, or reports
// false for today's head+tail stub. ctx is the tool goroutine's, so a
// turn cancellation cancels the receipt with it.
func (a *Agent) receiptify(ctx context.Context, name, out string) (string, bool) {
	if !a.receiptsEnabled() {
		return "", false
	}
	start := time.Now()
	rctx, cancel := context.WithTimeout(ctx, receiptBudget)
	defer cancel()
	text, _, err := a.sideQuery(rctx, receiptSystem,
		"Reduce this output to a receipt:\n\n```\n"+clipForReceipt(out)+"\n```",
		a.receiptSideModel(), 700)
	if err != nil {
		a.stats.receipt(true, 0)
		return "", false
	}
	quotes, bad := checkReceiptQuotes(out, text)
	if bad > 0 || quotes == 0 || quotes > receiptMaxQuotes {
		a.stats.receipt(true, 0)
		return "", false
	}
	a.stats.receipt(false, 0)
	a.logEvent(session.Event{Type: session.TypeDecision, Name: "receipt",
		Extra: mustJSON(map[string]any{
			"consumer": "receipt", "quotes": quotes, "src_bytes": len(out),
			"rec_bytes": len(text), "latency_ms": time.Since(start).Milliseconds(),
		})})
	return text, true
}

// receiptsEnabled is the session's opt-in for the receipt consumer.
// Unlike the jev triage consumer it does not need the decision client —
// its side model is the session's own (or advisor_model).
func (a *Agent) receiptsEnabled() bool {
	return a.DecisionsReceipts && !a.Off["receipts"]
}

func (a *Agent) receiptSideModel() string {
	return strings.TrimSpace(a.AdvisorModel)
}

// clipForReceipt bounds what the reducing model sees. The quotes are
// still verified against the FULL text, so eliding the middle from the
// summarizer's view only loses candidate quotes, never accepts one.
func clipForReceipt(out string) string {
	const window = 32 * 1024
	if len(out) <= window {
		return out
	}
	return out[:18*1024] +
		"\n...[middle elided from the summarizer's view; quotes are still checked against the full output]...\n" +
		out[len(out)-8*1024:]
}

// checkReceiptQuotes counts the receipt's Q: lines and how many are
// byte-exact against the full source.
func checkReceiptQuotes(src, receipt string) (quotes, bad int) {
	for _, line := range strings.Split(receipt, "\n") {
		if !strings.HasPrefix(line, "Q: ") {
			// Only exact-prefix lines count; a sloppier receipt with
			// misaligned prefixes is not evidence we can grade.
			continue
		}
		quotes++
		if !strings.Contains(src, strings.TrimSpace(line[3:])) {
			bad++
		}
	}
	return quotes, bad
}

// receiptifyStash is what stash calls on the long-output path: it
// returns the model text for the result, plus the archive note that
// keeps the expand_output handle; not-ok reports that today's
// head+tail preview (escaped exactly as before) should be returned.
func (a *Agent) applyReceipt(ctx context.Context, id string, size, shown int, out, receipt string) string {
	return archiveNote(id, size, len(receipt), outputDigest(out)) +
		tool.EscapeDelimiters(receipt) +
		fmt.Sprintf("\n[receipt: every Q: line above is byte-exact in archived output %s; "+
			"expand_output {\"id\":%q,\"grep\":\"<regexp>\"} finds more.]\n", id, id)
}
