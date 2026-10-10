package proof

import (
	"fmt"
	"strings"
)

// BlindTest is what the blind tester made of a turn (internal/agent's
// blindtest.go): a model that saw only the task wrote Checks checks, each
// for one rule; Held passed, Violated name the rules whose checks failed,
// and Inconclusive could not say (they crashed, timed out, or were not
// allowed to run). SentBack says the turn went back once on Violated;
// Still names the ones that failed again when rerun after that round.
type BlindTest struct {
	Model        string   `json:"model"`
	Checks       int      `json:"checks"`
	Held         int      `json:"held"`
	Inconclusive int      `json:"inconclusive"`
	Violated     []string `json:"violated,omitempty"`
	SentBack     bool     `json:"sent_back,omitempty"`
	Rechecked    bool     `json:"rechecked,omitempty"`
	Still        []string `json:"still,omitempty"`
	// Skipped says why no check ran: no reply, no checks in it, no
	// throwaway copy.
	Skipped string `json:"skipped,omitempty"`
}

// Line is the record in one line, for a proof summary and a notice.
func (b BlindTest) Line() string {
	if b.Skipped != "" {
		return "blind tester: skipped (" + b.Skipped + ")"
	}
	s := fmt.Sprintf("blind tester: %d/%d rules held", b.Held, b.Checks)
	if n := len(b.Violated); n > 0 {
		s += fmt.Sprintf(", %d failed", n)
	}
	if b.Inconclusive > 0 {
		s += fmt.Sprintf(", %d inconclusive", b.Inconclusive)
	}
	if b.Rechecked {
		if len(b.Still) == 0 {
			s += "; all fixed after one round"
		} else {
			s += fmt.Sprintf("; %d still failing after one round", len(b.Still))
		}
	}
	return s
}

// String lists the failed rules under the line, for /proof.
func (b BlindTest) String() string {
	var sb strings.Builder
	sb.WriteString(b.Line())
	for _, r := range b.Violated {
		sb.WriteString("\n  - " + r)
	}
	return sb.String()
}
