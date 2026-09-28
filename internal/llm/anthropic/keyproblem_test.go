package anthropic

import (
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/llm"
)

// An exhausted account behind a verbose gateway says so past the display
// cap. The key ring must read the whole text, or the key gets the
// 15-second rate-limit breaker and is tried again forever instead of the
// half-hour quota one — the mistake the OpenAI provider already avoids.
func TestKeyProblemReadsPastTheDisplayCap(t *testing.T) {
	body := strings.Repeat("trace ", 80) + "your credit balance is too low"
	err := &APIError{
		Status:  429,
		Message: llm.SafeErrorMessage(429, body),
		Detail:  llm.SafeErrorDetail(429, body),
	}
	if strings.Contains(err.Message, "credit") {
		t.Fatal("the test needs the quota wording past the display cap")
	}
	f, ok := keyProblem(err)
	if !ok || f != llm.FailQuota {
		t.Fatalf("failure = %v, %v; want FailQuota", f, ok)
	}
}
