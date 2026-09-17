package llm

import (
	"net/http"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestBoundErrorMessageCapsRunesNotBytes(t *testing.T) {
	// 200 two-byte runes: 400 bytes, 200 runes — under the cap either way,
	// but it proves the counter is runes.
	short := strings.Repeat("é", 200)
	if got := BoundErrorMessage(short); got != short {
		t.Fatalf("short message changed: %d runes", utf8.RuneCountInString(got))
	}

	long := strings.Repeat("é", maxErrorBody+50) // 700 bytes, 350 runes
	got := BoundErrorMessage(long)
	if !utf8.ValidString(got) {
		t.Fatal("cap split a rune")
	}
	if utf8.RuneCountInString(got) != maxErrorBody+1 { // the ellipsis
		t.Fatalf("got %d runes", utf8.RuneCountInString(got))
	}
	if !strings.HasSuffix(got, "…") {
		t.Fatalf("missing ellipsis: %q", got[len(got)-8:])
	}
}

func TestBoundErrorMessageCollapsesWhitespace(t *testing.T) {
	got := BoundErrorMessage("a\n\n  b\t c  \r\nd")
	if got != "a b c d" {
		t.Fatalf("got %q", got)
	}
}

func TestSafeErrorMessageDropsAuthBodies(t *testing.T) {
	echo := `{"error":"invalid key sk-secret-echoed-back"}`
	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden} {
		if got := SafeErrorMessage(status, echo); got != "" {
			t.Fatalf("status %d: got %q", status, got)
		}
	}
	// A 400 keeps the message — a structured API error is what the user
	// needs to see — but not the key inside it. Dropping the body on 401
	// and 403 alone was not enough: LiteLLM- and OpenRouter-style proxies
	// answer 400 or 500 with the upstream error nested in, and that one
	// carries the key into the transcript on disk.
	got := SafeErrorMessage(http.StatusBadRequest, echo)
	if strings.Contains(got, "sk-secret-echoed-back") {
		t.Fatalf("400 carried the key through: %q", got)
	}
	if !strings.Contains(got, "invalid key") || !strings.Contains(got, "[redacted]") {
		t.Fatalf("400 should keep the message and mask the key, got %q", got)
	}
	if got := SafeErrorMessage(http.StatusInternalServerError, strings.Repeat("x", 1000)); utf8.RuneCountInString(got) > maxErrorBody+1 {
		t.Fatalf("500 body not capped: %d runes", utf8.RuneCountInString(got))
	}
}

// TestRedactCoversTheShapesProvidersActuallyReturn: the commit that added
// this file said "redact" and did not — it dropped the body on 401 and
// 403 and passed everything else through. These are the shapes a provider
// or a proxy in front of one puts a credential into an error body in.
func TestRedactCoversTheShapesProvidersActuallyReturn(t *testing.T) {
	for _, tc := range []struct{ in, leaked string }{
		{`Incorrect API key provided: sk-proj-AbCdEf123456789`, "sk-proj-AbCdEf123456789"},
		{`bad header "Authorization: Bearer abcdef1234567890"`, "abcdef1234567890"},
		{`{"token": "ghp_AbCdEf1234567890xyz"}`, "ghp_AbCdEf1234567890xyz"},
		{`upstream said api-key=sk_live_9f8e7d6c5b4a3210`, "sk_live_9f8e7d6c5b4a3210"},
		{`jwt eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eee.fff rejected`, "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9"},
	} {
		got := Redact(tc.in)
		if strings.Contains(got, tc.leaked) {
			t.Errorf("Redact(%q) left the credential in: %q", tc.in, got)
		}
		if !strings.Contains(got, "[redacted]") {
			t.Errorf("Redact(%q) = %q, nothing was masked", tc.in, got)
		}
	}
	// Ordinary error text is left alone: a redaction that eats the
	// message helps nobody.
	for _, plain := range []string{
		"model not found: gpt-5-turbo",
		"request 4f1a2b3c timed out after 60s",
		"context length exceeded: 200000 > 128000",
	} {
		if got := Redact(plain); got != plain {
			t.Errorf("Redact(%q) = %q, want it untouched", plain, got)
		}
	}
}

// TestClassifyTextReadsPastTheDisplayCap: Error() is cut at 300 runes for
// the UI and the transcript, and classification read that copy — so a
// gateway that puts its trace ids first turned an exhausted account into
// a plain rate limit, retried against a dead key.
func TestClassifyTextReadsPastTheDisplayCap(t *testing.T) {
	long := strings.Repeat("trace-id 0123456789 ", 40) + "insufficient_quota"
	err := &detailErr{msg: BoundErrorMessage(long), detail: long}
	if PermanentLimit(err.Error()) {
		t.Skip("the display copy already carries the word; the test proves nothing")
	}
	if !PermanentLimit(ClassifyText(err)) {
		t.Fatal("classification could not see past the display cap")
	}
	if Retryable(err) {
		t.Fatal("an exhausted account was graded retryable")
	}
}

type detailErr struct{ msg, detail string }

func (e *detailErr) Error() string            { return e.msg }
func (e *detailErr) ErrorDetail() string      { return e.detail }
func (e *detailErr) HTTPStatus() int          { return 429 }
func (e *detailErr) RetryHint() time.Duration { return 0 }
