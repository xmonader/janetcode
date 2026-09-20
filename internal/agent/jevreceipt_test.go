package agent

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/llm"
)

func receiptAgent(t *testing.T, responses []*llm.Response) (*Agent, *fake) {
	t.Helper()
	f := &fake{responses: responses}
	a, _ := newAgent(t, f, nil)
	a.DecisionsReceipts = true
	return a, f
}

var errBoom = errors.New("side call down")

// The coherent fixture: the receipt's two Q: lines exist byte-exactly
// in the source. Every receipt test stages the SAME two-line source.
var receiptSrc = "line one: seg0/offset 0\nline two: FAIL restore checkpoint 2: want 3/offset 13, got offset 39\n"

var receiptFix = "The run restored 3 segments and failed on restore 2.\n" +
	"Q: line two: FAIL restore checkpoint 2: want 3/offset 13, got offset 39\n" +
	"Q: line one: seg0/offset 0\n"

func TestReceiptifyAcceptsExactQuotes(t *testing.T) {
	a, _ := receiptAgent(t, []*llm.Response{text(receiptFix)})
	// The receipt quotes lines that exist byte-exactly in the source.
	rec, ok := a.receiptify(context.Background(), "bash", receiptSrc)
	if !ok {
		t.Fatal("a receipt with only exact quotes was rejected")
	}
	if !strings.Contains(rec, "Q: line two") {
		t.Fatalf("receipt text lost: %q", rec)
	}
}

func TestReceiptifyRejectsAFabricatedQuote(t *testing.T) {
	a, _ := receiptAgent(t, []*llm.Response{text(
		"prose\nQ: this exact line is INVENTED and appears nowhere in the source\n")})
	src := "real line one\nreal line two\n"
	if rec, ok := a.receiptify(context.Background(), "bash", src); ok {
		t.Fatalf("a fabricated quote was accepted: %q", rec)
	}
}

func TestReceiptifyZeroQuotesIsRejected(t *testing.T) {
	a, _ := receiptAgent(t, []*llm.Response{text(
		"prose only: 3 tests failed in 4.2s.\n")})
	if rec, ok := a.receiptify(context.Background(), "bash", "anything...\n"); ok {
		t.Fatalf("a quote-free receipt was accepted: %q", rec)
	}
}

// A side call that fails leaves the head+tail preview standing: the
// receipt path must be invisible when the provider is not there.
func TestReceiptifyFailsOpenOnProviderError(t *testing.T) {
	f := &fake{responses: []*llm.Response{}, errs: []error{errBoom}}
	a, _ := newAgent(t, f, nil)
	a.DecisionsReceipts = true
	if rec, ok := a.receiptify(context.Background(), "bash", "out\n"); ok {
		t.Fatalf("error path produced a receipt: %q", rec)
	}
}

func TestReceiptifyDisabledIsANoOp(t *testing.T) {
	f := &fake{responses: []*llm.Response{text(receiptFix)}}
	a, _ := newAgent(t, f, nil)
	// DecisionsReceipts deliberately unset.
	if rec, ok := a.receiptify(context.Background(), "bash", "long\n"); ok {
		t.Fatalf("unconfigured consumer produced a receipt: %q", rec)
	}
	if len(f.requests) != 0 {
		t.Fatalf("unconfigured consumer still called the provider %d times", len(f.requests))
	}
}

func TestReceiptGateRejectsStaleReceipts(t *testing.T) {
	a, _ := receiptAgent(t, []*llm.Response{text(receiptFix)})
	// Perturb the numbers the prototype perturbed: a receipt correct for
	// the ORIGINAL source is fabricated against THIS one (the model
	// re-reading a re-run's log). Both its quotes must be judged against
	// the source they are handed, not remembered from anywhere.
	src := strings.Replace(receiptSrc, "seg0/offset 0", "seg9/offset 9", 1)
	src = strings.Replace(src, "checkpoint 2: want 3/offset 13, got offset 39",
		"checkpoint 2: want 5/offset 51, got offset 42", 1)
	n, bad := checkReceiptQuotes(src, receiptFix)
	if n != 2 || bad != 2 {
		t.Fatalf("gate = %d quotes, %d bad; want both mismatched after the perturbation", n, bad)
	}
	if rec, ok := a.receiptify(context.Background(), "bash", src); ok {
		t.Fatalf("gate passed a receipt quoting the wrong counts: %q", rec)
	}
}

func TestStatsReceiptTallyAndBytes(t *testing.T) {
	var s Stats
	if s.ReceiptCalls != 0 {
		t.Fatal("zero stats must be zero")
	}
	s.ReceiptCalls = 2
	s.ReceiptRejected = 1
	s.ReceiptSavedB = 9000
	if s.ReceiptCalls != 2 || s.ReceiptRejected != 1 {
		t.Fatal("receipt fields did not stick")
	}
}
