package headless

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/agent"
	"github.com/xmonader/janetcode/internal/config"
	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/llm/anthropic"
	"github.com/xmonader/janetcode/internal/llm/openai"
)

// Every row of exitRules has to be reachable: a rule an earlier row
// shadows is a rule that never fires, which is how a taxonomy rots
// without anything failing.
func TestExitRulesAllReachable(t *testing.T) {
	samples := map[string]error{
		"cancelled":  context.Canceled,
		"budget":     fmt.Errorf("%w: spent it", ErrBudget),
		"turn limit": fmt.Errorf("turn: %w", agent.ErrMaxIterations),
		"bad input":  fmt.Errorf("%w: two prompts", ErrBadInput),
		"auth":       fmt.Errorf("x: %w", llm.ErrNoAPIKey),
		"config":     fmt.Errorf("%w: bad json", ErrConfig),
	}
	if len(samples) != len(exitRules) {
		t.Fatalf("exitRules has %d rows, the test knows %d: add a sample for the new row", len(exitRules), len(samples))
	}
	for _, r := range exitRules {
		err, ok := samples[r.name]
		if !ok {
			t.Fatalf("no sample error for rule %q", r.name)
		}
		if got := ExitCode(err); got != r.code {
			t.Errorf("rule %q: ExitCode(%v) = %d, want %d (shadowed by an earlier row?)", r.name, err, got, r.code)
		}
	}
}

// Deadlines end a run the same way an interrupt does: the work did not
// finish, and nothing about the request was wrong.
func TestExitCodeDeadline(t *testing.T) {
	for _, err := range []error{
		context.DeadlineExceeded,
		fmt.Errorf("provider: %w", context.DeadlineExceeded),
	} {
		if got := ExitCode(err); got != ExitCancelled {
			t.Errorf("ExitCode(%v) = %d, want %d", err, got, ExitCancelled)
		}
	}
}

// A typed provider refusal exits 41 whichever provider raised it, and
// carries the status for the hint the command line prints.
func TestExitCodeTypedProviderErrors(t *testing.T) {
	cases := []struct {
		err      error
		wantCode int
		rejected bool
	}{
		{&openai.APIError{Status: 401, Message: "bad key"}, ExitAuth, true},
		{fmt.Errorf("turn: %w", &openai.APIError{Status: 403, Message: "forbidden"}), ExitAuth, true},
		{fmt.Errorf("turn: %w", &anthropic.APIError{Status: 401, Message: "invalid x-api-key"}), ExitAuth, true},
		{&anthropic.APIError{Status: 403, Message: "forbidden"}, ExitAuth, true},
		// Not the key: a rate limit and a server fault are ordinary failures.
		{&openai.APIError{Status: 429, Message: "slow down"}, ExitOther, false},
		{&anthropic.APIError{Status: 500, Message: "boom"}, ExitOther, false},
		{nil, ExitOK, false},
	}
	for _, c := range cases {
		if got := ExitCode(c.err); got != c.wantCode {
			t.Errorf("ExitCode(%v) = %d, want %d", c.err, got, c.wantCode)
		}
		if got := KeyRejected(c.err); got != c.rejected {
			t.Errorf("KeyRejected(%v) = %v, want %v", c.err, got, c.rejected)
		}
	}
}

// A message that merely reads like a refusal still exits 41 -- a caller
// reading only the status should retry with a different key -- but it
// does not earn the "check your key" hint, which would send a person
// after the wrong bug when the text came from a tool's own output.
func TestExitCodeUntypedAuthMessage(t *testing.T) {
	for _, s := range []string{"provider refused (HTTP 401)", "proxy said (HTTP 403)"} {
		err := errors.New(s)
		if got := ExitCode(err); got != ExitAuth {
			t.Errorf("ExitCode(%q) = %d, want %d", s, got, ExitAuth)
		}
		if KeyRejected(err) {
			t.Errorf("KeyRejected(%q) = true, want false: only a typed error may accuse the key", s)
		}
		if _, ok := HTTPStatus(err); ok {
			t.Errorf("HTTPStatus(%q) reported a status from prose", s)
		}
	}
}

// Missing credentials and an unusable configuration file are different
// failures with different fixes, and must not collapse into one code.
func TestExitCodeCredentialsVersusConfig(t *testing.T) {
	if got := ExitCode(fmt.Errorf("x: %w", config.ErrNoCredentials)); got != ExitAuth {
		t.Errorf("no credentials = %d, want %d", got, ExitAuth)
	}
	if got := ExitCode(fmt.Errorf("%w: unknown provider %q", config.ErrBadConfig, "nope")); got != ExitConfig {
		t.Errorf("bad config = %d, want %d", got, ExitConfig)
	}
}

// Anything the table does not recognise is a plain failure.
func TestExitCodeUnclassified(t *testing.T) {
	if got := ExitCode(errors.New("something")); got != ExitOther {
		t.Errorf("ExitCode(unknown) = %d, want %d", got, ExitOther)
	}
}

// The codes themselves are an API: a script branches on them, so they
// must stay put and stay distinct.
func TestExitCodesAreStableAndDistinct(t *testing.T) {
	want := map[string]int{
		"ok": 0, "other": 1, "auth": 41, "bad input": 42,
		"config": 52, "turn limit": 53, "budget": 55, "cancelled": 130,
	}
	got := map[string]int{
		"ok": ExitOK, "other": ExitOther, "auth": ExitAuth, "bad input": ExitBadInput,
		"config": ExitConfig, "turn limit": ExitTurnLimit, "budget": ExitBudget, "cancelled": ExitCancelled,
	}
	seen := map[int]string{}
	for name, code := range got {
		if want[name] != code {
			t.Errorf("%s = %d, want %d: exit codes are part of the CLI contract", name, code, want[name])
		}
		if other, dup := seen[code]; dup {
			t.Errorf("%s and %s both exit %d", name, other, code)
		}
		seen[code] = name
	}
}

// A headless failure used to print Go's raw network error ("Post ...:
// dial tcp 127.0.0.1:9: connect: connection refused") while the TUI
// said what failed and what to try. Explained errors keep their cause
// for the exit status; the run's own stops pass through untouched.
func TestExplainFailureWordsProviderErrors(t *testing.T) {
	refused := fmt.Errorf(`Post "http://127.0.0.1:9/v1/chat/completions": dial tcp 127.0.0.1:9: connect: connection refused`)
	got := explainFailure(refused, Options{})
	if !strings.HasPrefix(got.Error(), "Cannot reach the API") || !strings.Contains(got.Error(), "janetcode --doctor") {
		t.Fatalf("refused = %q", got)
	}
	if !errors.Is(got, refused) || !Explained(got) {
		t.Fatal("the explanation lost its cause")
	}
	auth := &openai.APIError{Status: 401, Message: "bad key"}
	if x := explainFailure(auth, Options{}); ExitCode(x) != ExitAuth || !KeyRejected(x) {
		t.Fatalf("auth explained to exit %d", ExitCode(x))
	}
	for _, own := range []error{context.Canceled, ErrBudget, errors.New("something else entirely")} {
		if x := explainFailure(own, Options{}); x != own {
			t.Errorf("%v was rewritten to %v", own, x)
		}
	}
}
