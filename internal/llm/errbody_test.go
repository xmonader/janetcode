package llm

import (
	"net/http"
	"strings"
	"testing"
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
	if got := SafeErrorMessage(http.StatusBadRequest, echo); !strings.Contains(got, "sk-secret-echoed-back") {
		t.Fatalf("400 should keep the structured message, got %q", got)
	}
	if got := SafeErrorMessage(http.StatusInternalServerError, strings.Repeat("x", 1000)); utf8.RuneCountInString(got) > maxErrorBody+1 {
		t.Fatalf("500 body not capped: %d runes", utf8.RuneCountInString(got))
	}
}
