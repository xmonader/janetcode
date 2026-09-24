package agent

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/xmonader/janetcode/internal/llm"
)

func TestFallbackChainIsCappedAndDeduped(t *testing.T) {
	got := FallbackChain([]string{" b ", "b", "a", "", "c", "d", "m"}, "m")
	if want := []string{"b", "a", "c"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("chain = %v, want %v", got, want)
	}
	if got := FallbackChain([]string{"m"}, "m"); len(got) != 0 {
		t.Fatalf("the model in use is not a fallback: %v", got)
	}
}

func TestWorthFallback(t *testing.T) {
	yes := []string{
		"insufficient_quota: you have run out",
		"rate limit reached for requests",
		"server error 503",
		"Overloaded (HTTP 529)",
		"http 429: too many requests",
	}
	for _, s := range yes {
		if !worthFallback(errors.New(s)) {
			t.Errorf("%q should be worth a fallback", s)
		}
	}
	no := []string{
		"http 400: unknown parameter foo",
		"prompt is too long: 210000 tokens > 200000 maximum",
		"context canceled",
		"tool call has invalid JSON arguments",
	}
	for _, s := range no {
		if worthFallback(errors.New(s)) {
			t.Errorf("%q should not be worth a fallback", s)
		}
	}
	// A cancellation or deadline is never worth a fallback, however a
	// provider wrapped it and whatever else its message says.
	for _, err := range []error{
		context.Canceled,
		fmt.Errorf("server error 503: %w", context.Canceled),
		fmt.Errorf("rate limit reached: %w", fmt.Errorf("stream: %w", context.DeadlineExceeded)),
	} {
		if worthFallback(err) {
			t.Errorf("%v should not be worth a fallback", err)
		}
	}
}

func TestFallbackRunsOnTheNextModel(t *testing.T) {
	f := &fake{
		errs:      []error{errors.New("insufficient_quota: this plan is exhausted")},
		responses: []*llm.Response{nil, text("done on the other model")},
	}
	a, _ := newAgent(t, f, nil)
	a.Fallbacks = []string{"backup-model"}
	evs, emit := collect()
	if err := a.Run(context.Background(), "hi", emit); err != nil {
		t.Fatal(err)
	}
	if f.requests[1].Model != "backup-model" {
		t.Fatalf("second call went to %q", f.requests[1].Model)
	}
	if a.Model != "backup-model" {
		t.Fatalf("the session stayed on %q; the quota will still be out", a.Model)
	}
	var sawDiscard, sawNotice bool
	for _, e := range *evs {
		switch {
		case e.Kind == EvDiscard:
			sawDiscard = true
		case e.Kind == EvNotice && strings.Contains(e.Text, "falling back to backup-model"):
			sawNotice = true
		}
	}
	if !sawDiscard {
		t.Fatal("the failed attempt's partial stream was not discarded")
	}
	if !sawNotice {
		t.Fatal("nothing said the turn was falling back")
	}
	// A run that only finished because of the fallback is a run the
	// harness rescued, and the counters say so.
	if st := a.Stats(); st.Fallbacks != 1 || st.Saves() != 1 {
		t.Fatalf("stats = %+v", st)
	}
}

func TestFallbackTriesEachInTurnAndReportsTheLast(t *testing.T) {
	f := &fake{errs: []error{
		errors.New("http 503 service unavailable"),
		errors.New("http 503 service unavailable"),
		errors.New("http 503 service unavailable"),
	}}
	a, _ := newAgent(t, f, nil)
	a.Fallbacks = []string{"b", "c"}
	err := a.Run(context.Background(), "hi", func(Event) {})
	if err == nil || !strings.Contains(err.Error(), "503") {
		t.Fatalf("err = %v", err)
	}
	if len(f.requests) != 3 {
		t.Fatalf("%d calls, want the model and both fallbacks", len(f.requests))
	}
}

func TestNoFallbackForAnErrorEveryModelShares(t *testing.T) {
	f := &fake{errs: []error{errors.New("http 400: bad request")}}
	a, _ := newAgent(t, f, nil)
	a.Fallbacks = []string{"b"}
	if err := a.Run(context.Background(), "hi", func(Event) {}); err == nil {
		t.Fatal("want the original error")
	}
	if len(f.requests) != 1 {
		t.Fatalf("%d calls: a bad request must not be retried elsewhere", len(f.requests))
	}
}

func TestFallbackRoutesToAnotherProvider(t *testing.T) {
	other := &fake{responses: []*llm.Response{text("hello from elsewhere")}}
	f := &fake{errs: []error{errors.New("quota exceeded")}}
	a, _ := newAgent(t, f, nil)
	a.Fallbacks = []string{"elsewhere/model"}
	a.Route = func(name string) (llm.Provider, string, error) { return other, "resolved-" + name, nil }
	if err := a.Run(context.Background(), "hi", func(Event) {}); err != nil {
		t.Fatal(err)
	}
	if len(other.requests) != 1 || other.requests[0].Model != "resolved-elsewhere/model" {
		t.Fatalf("other provider saw %+v", other.requests)
	}
	if a.Provider != llm.Provider(other) {
		t.Fatal("the session did not move to the provider that worked")
	}
}

// statusErr is a provider error that carries its status and the server's
// whole text, the way both providers' errors do.
type statusErr struct {
	status int
	msg    string
	detail string
}

func (e statusErr) Error() string            { return e.msg }
func (e statusErr) HTTPStatus() int          { return e.status }
func (e statusErr) RetryHint() time.Duration { return 0 }
func (e statusErr) ErrorDetail() string      { return e.detail }

// An error with a status is judged by the status and the whole server
// text, not by the display copy, which is cut short.
func TestWorthFallbackReadsTheStatusAndWholeText(t *testing.T) {
	long := strings.Repeat("x", 400)
	cases := []struct {
		err  statusErr
		want bool
	}{
		{statusErr{400, "http 400: " + long[:290], "http 400: " + long + " insufficient balance"}, true},
		{statusErr{402, "Payment Required", ""}, true},
		{statusErr{503, "unavailable", ""}, true},
		{statusErr{400, "http 400: max_tokens exceeds your credits limit? no: bad param", ""}, false},
		{statusErr{401, "rate limit key invalid", ""}, false},
		{statusErr{403, "The free allocated quota is exhausted", ""}, true},
	}
	for _, c := range cases {
		if got := worthFallback(fmt.Errorf("openai: %w", c.err)); got != c.want {
			t.Errorf("%d %.40q: worthFallback = %v, want %v", c.err.status, c.err.msg, got, c.want)
		}
	}
}
