package llm

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// hedgeFake is a provider that waits, then streams its text, or fails.
type hedgeFake struct {
	name  string
	wait  time.Duration
	text  string
	err   error
	calls atomic.Int32
	// cancelled is set when the call's context ended it.
	cancelled atomic.Bool
}

func (f *hedgeFake) Name() string { return f.name }

func (f *hedgeFake) Stream(ctx context.Context, req Request, emit func(Event)) (*Response, error) {
	f.calls.Add(1)
	select {
	case <-time.After(f.wait):
	case <-ctx.Done():
		f.cancelled.Store(true)
		return nil, ctx.Err()
	}
	if f.err != nil {
		return nil, f.err
	}
	for _, w := range strings.SplitAfter(f.text, " ") {
		emit(Event{Type: EventTextDelta, Text: w})
	}
	return &Response{Message: TextMessage(RoleAssistant, f.text), StopReason: StopEndTurn, Model: f.name}, nil
}

// hedgeStatus is a StatusError with a status and nothing else.
type hedgeStatus int

func (s hedgeStatus) Error() string            { return fmt.Sprintf("http %d", int(s)) }
func (s hedgeStatus) HTTPStatus() int          { return int(s) }
func (s hedgeStatus) RetryHint() time.Duration { return 0 }

func hedgeReq() Request {
	return Request{Model: "m", Messages: []Message{TextMessage(RoleUser, "hi")}}
}

func hedgeCollect(p Provider) (*Response, string, []string, error) {
	var text strings.Builder
	var status []string
	resp, err := p.Stream(context.Background(), hedgeReq(), func(ev Event) {
		switch ev.Type {
		case EventTextDelta:
			text.WriteString(ev.Text)
		case EventStatus:
			status = append(status, ev.Text)
		}
	})
	return resp, text.String(), status, err
}

// A primary that answers before the delay is never duplicated.
func TestHedgeFastPrimaryIsNotDuplicated(t *testing.T) {
	a := &hedgeFake{name: "a", wait: 5 * time.Millisecond, text: "from a"}
	b := &hedgeFake{name: "b", text: "from b"}
	p := Hedge(a, b, HedgeConfig{After: 200 * time.Millisecond})
	resp, text, _, err := hedgeCollect(p)
	if err != nil || resp.Model != "a" || text != "from a" {
		t.Fatalf("resp = %+v text = %q err = %v", resp, text, err)
	}
	if b.calls.Load() != 0 {
		t.Fatal("a fast primary was hedged")
	}
	if s := p.(*hedged).HedgeStats(); s.Sent != 0 {
		t.Fatalf("stats = %+v", s)
	}
}

// A primary that has said nothing by the delay is raced, the faster
// answer wins, and the slow one is cancelled and shows nothing.
func TestHedgeSlowPrimaryLosesToTheDuplicate(t *testing.T) {
	a := &hedgeFake{name: "a", wait: 2 * time.Second, text: "from a"}
	b := &hedgeFake{name: "b", wait: 5 * time.Millisecond, text: "from b"}
	p := Hedge(a, b, HedgeConfig{After: 30 * time.Millisecond})
	began := time.Now()
	resp, text, status, err := hedgeCollect(p)
	if err != nil || resp.Model != "b" || text != "from b" {
		t.Fatalf("resp = %+v text = %q err = %v", resp, text, err)
	}
	if time.Since(began) > time.Second {
		t.Fatal("the call waited for the slow primary")
	}
	if len(status) != 1 || !strings.Contains(status[0], "racing b") {
		t.Fatalf("status = %q", status)
	}
	deadline := time.Now().Add(time.Second)
	for !a.cancelled.Load() && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if !a.cancelled.Load() {
		t.Fatal("the losing primary was not cancelled")
	}
	if s := p.(*hedged).HedgeStats(); s.Sent != 1 || s.Won != 1 {
		t.Fatalf("stats = %+v", s)
	}
}

// The duplicate is only a bet: when the primary still answers first, it
// wins and the duplicate is the one cancelled.
func TestHedgePrimaryCanStillWin(t *testing.T) {
	a := &hedgeFake{name: "a", wait: 60 * time.Millisecond, text: "from a"}
	b := &hedgeFake{name: "b", wait: 2 * time.Second, text: "from b"}
	p := Hedge(a, b, HedgeConfig{After: 20 * time.Millisecond})
	resp, text, _, err := hedgeCollect(p)
	if err != nil || resp.Model != "a" || text != "from a" {
		t.Fatalf("resp = %+v text = %q err = %v", resp, text, err)
	}
	if s := p.(*hedged).HedgeStats(); s.Sent != 1 || s.Won != 0 {
		t.Fatalf("stats = %+v", s)
	}
}

// A primary that fails on the route (a 504 from the gateway) before its
// first token fails over at once, without waiting out the delay.
func TestHedgeRouteFailureFailsOver(t *testing.T) {
	a := &hedgeFake{name: "a", err: hedgeStatus(504)}
	b := &hedgeFake{name: "b", text: "from b"}
	p := Hedge(a, b, HedgeConfig{After: time.Hour})
	resp, _, _, err := hedgeCollect(p)
	if err != nil || resp.Model != "b" {
		t.Fatalf("resp = %+v err = %v", resp, err)
	}
	if s := p.(*hedged).HedgeStats(); s.Failover != 1 || s.Won != 1 {
		t.Fatalf("stats = %+v", s)
	}
}

// A request the server rejects (a 400) is the request's fault, not the
// route's: the duplicate would fail the same way, so none is sent.
func TestHedgeBadRequestIsNotDuplicated(t *testing.T) {
	a := &hedgeFake{name: "a", err: hedgeStatus(400)}
	b := &hedgeFake{name: "b", text: "from b"}
	_, _, _, err := hedgeCollect(Hedge(a, b, HedgeConfig{After: time.Hour}))
	if err == nil || b.calls.Load() != 0 {
		t.Fatalf("err = %v, b calls = %d", err, b.calls.Load())
	}
}

// When both routes fail, the error names both, the primary's first.
func TestHedgeBothFail(t *testing.T) {
	a := &hedgeFake{name: "a", err: hedgeStatus(504)}
	b := &hedgeFake{name: "b", err: hedgeStatus(503)}
	_, _, _, err := hedgeCollect(Hedge(a, b, HedgeConfig{After: time.Hour}))
	var se StatusError
	if err == nil || !errors.As(err, &se) || se.HTTPStatus() != 504 || !strings.Contains(err.Error(), "hedge (b)") {
		t.Fatalf("err = %v", err)
	}
}

// A cancelled call ends both attempts and says so.
func TestHedgeCancel(t *testing.T) {
	a := &hedgeFake{name: "a", wait: time.Hour}
	b := &hedgeFake{name: "b", wait: time.Hour}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err := Hedge(a, b, HedgeConfig{After: 10 * time.Millisecond}).Stream(ctx, hedgeReq(), nil)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v", err)
	}
}

// Without a second route there is nothing to hedge.
func TestHedgeNilSecondIsPrimary(t *testing.T) {
	a := &hedgeFake{name: "a"}
	if Hedge(a, nil, HedgeConfig{}) != Provider(a) {
		t.Fatal("Hedge with no second route must return the primary")
	}
}

// The learned delay is the p95 of recent first tokens, bounded below by
// the floor, and the fallback until there are enough of them.
func TestHedgeDelayLearnsP95(t *testing.T) {
	h := Hedge(&hedgeFake{name: "a"}, &hedgeFake{name: "b"}, HedgeConfig{Fallback: 7 * time.Second, Floor: 100 * time.Millisecond}).(*hedged)
	if d := h.delay(); d != 7*time.Second {
		t.Fatalf("delay with no samples = %s", d)
	}
	for i := 1; i <= 100; i++ {
		h.ttft.Observe(time.Duration(i) * 10 * time.Millisecond)
	}
	if d := h.delay(); d != 960*time.Millisecond {
		t.Fatalf("delay = %s, want the p95 960ms", d)
	}
	low := Hedge(&hedgeFake{name: "a"}, &hedgeFake{name: "b"}, HedgeConfig{Floor: time.Second}).(*hedged)
	for i := 0; i < 30; i++ {
		low.ttft.Observe(50 * time.Millisecond)
	}
	if d := low.delay(); d != time.Second {
		t.Fatalf("delay = %s, want the floor", d)
	}
}

// The window forgets: a route that slowed down is judged as it is now.
func TestHedgeTTFTWindow(t *testing.T) {
	var tt TTFT
	for i := 0; i < hedgeWindow; i++ {
		tt.Observe(time.Millisecond)
	}
	for i := 0; i < hedgeWindow; i++ {
		tt.Observe(time.Second)
	}
	if p, ok := tt.P95(); !ok || p != time.Second {
		t.Fatalf("p95 = %s %v", p, ok)
	}
}

// Status lines from the primary's retries still reach the user before a
// winner; the duplicate's are not shown, since it may never be used.
func TestHedgeStatusBeforeContent(t *testing.T) {
	a := hedgeFunc(func(ctx context.Context, emit func(Event)) (*Response, error) {
		emit(Event{Type: EventStatus, Text: "retrying 1/3"})
		emit(Event{Type: EventTextDelta, Text: "ok"})
		return &Response{Message: TextMessage(RoleAssistant, "ok")}, nil
	})
	_, text, status, err := hedgeCollect(Hedge(a, &hedgeFake{name: "b", wait: time.Hour}, HedgeConfig{After: time.Hour}))
	if err != nil || text != "ok" || len(status) != 1 || status[0] != "retrying 1/3" {
		t.Fatalf("text = %q status = %q err = %v", text, status, err)
	}
}

type hedgeFunc func(ctx context.Context, emit func(Event)) (*Response, error)

func (f hedgeFunc) Name() string { return "func" }
func (f hedgeFunc) Stream(ctx context.Context, _ Request, emit func(Event)) (*Response, error) {
	return f(ctx, emit)
}

// The wrapper keeps the wrapped provider's optional interfaces.
func TestHedgePassesInterfacesThrough(t *testing.T) {
	p := Hedge(&hedgeFake{name: "a"}, &hedgeFake{name: "b"}, HedgeConfig{})
	if p.Name() != "a" {
		t.Fatalf("name = %q", p.Name())
	}
	if _, ok := p.(ModelLister); !ok {
		t.Fatal("ModelLister hidden")
	}
	if _, ok := p.(PerTask); !ok {
		t.Fatal("PerTask hidden")
	}
	if HonoursEffort(p) {
		t.Fatal("a provider without effort must not gain it")
	}
}
