package sessionproto

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

func newClock() *clock { return &clock{t: time.Unix(1_700_000_000, 0)} }

func TestSecondControllerRefusedWithState(t *testing.T) {
	tb := NewTable(10*time.Second, newClock().now)
	if c := tb.Authorize("a", "alice", 0, false, 0); c != nil {
		t.Fatalf("first client refused: %v", c)
	}
	c := tb.Authorize("b", "bob", 0, true, 2)
	if c == nil || c.Code != CodeLeaseHeld {
		t.Fatalf("second client: %+v, want %s", c, CodeLeaseHeld)
	}
	if c.State.Holder == nil || c.State.Holder.Client != "a" || !c.State.Busy || c.State.Queued != 2 || c.State.You != RoleNone {
		t.Fatalf("conflict state does not describe the session: %+v", c.State)
	}
}

func TestLeaseLapsesWithoutHeartbeat(t *testing.T) {
	clk := newClock()
	tb := NewTable(10*time.Second, clk.now)
	tb.Authorize("a", "", 0, false, 0)
	clk.add(9 * time.Second)
	if !tb.Touch("a") {
		t.Fatal("holder's heartbeat refused before expiry")
	}
	clk.add(9 * time.Second) // 18s after grant, 9s after the touch
	if c := tb.Authorize("b", "", 0, false, 0); c == nil {
		t.Fatal("lease expired early: heartbeat did not extend it")
	}
	clk.add(2 * time.Second) // 11s since the touch
	if c := tb.Authorize("b", "", 0, false, 0); c != nil {
		t.Fatalf("lapsed lease still blocks: %v", c)
	}
	if tb.Touch("a") {
		t.Fatal("a lapsed holder's heartbeat revived its lease")
	}
}

func TestStaleEpochIsNamedAndFresh(t *testing.T) {
	clk := newClock()
	tb := NewTable(time.Second, clk.now)
	tb.Authorize("a", "", 0, false, 0)
	old := tb.View("a", false, 0).Epoch
	clk.add(2 * time.Second)
	tb.Authorize("b", "", 0, false, 0) // takes over after a lapsed
	c := tb.Authorize("a", "", old, false, 0)
	if c == nil || c.Code != CodeStaleLease {
		t.Fatalf("stale holder got %+v, want %s", c, CodeStaleLease)
	}
	if c.State.Epoch <= old || c.State.Holder.Client != "b" {
		t.Fatalf("stale conflict should carry the current state: %+v", c.State)
	}
	// A client that was holder, lapsed, and asks again with its old epoch on a free session is stale too.
	clk.add(2 * time.Second)
	if c := tb.Authorize("a", "", old, false, 0); c == nil || c.Code != CodeStaleLease {
		t.Fatalf("old epoch on a free session: %+v", c)
	}
	if c := tb.Authorize("a", "", 0, false, 0); c != nil {
		t.Fatalf("client re-asking without an epoch should be granted: %v", c)
	}
}

func TestObserverNeverMutatesUntilItTakesControl(t *testing.T) {
	tb := NewTable(time.Minute, newClock().now)
	leave := tb.Watch("o")
	defer leave()
	if c := tb.Authorize("o", "", 0, false, 0); c == nil || c.Code != CodeObserver {
		t.Fatalf("observer on a free session: %+v", c)
	}
	if v := tb.View("o", false, 0); v.You != RoleObserver || v.Observers != 1 || v.Holder != nil {
		t.Fatalf("observer view: %+v", v)
	}
	if _, c := tb.Acquire("o", "", false, 0); c != nil {
		t.Fatalf("observer cannot upgrade a free session: %v", c)
	}
	if c := tb.Authorize("o", "", 0, false, 0); c != nil {
		t.Fatalf("former observer refused after acquiring: %v", c)
	}
}

func TestTakeWaitsForTurnBoundary(t *testing.T) {
	tb := NewTable(time.Minute, newClock().now)
	tb.Authorize("a", "", 0, false, 0)
	if _, c := tb.Take("b", "", true, 0); c == nil || c.Code != CodeBusy {
		t.Fatalf("handoff mid-turn: %+v", c)
	}
	if c := tb.Authorize("a", "", 0, true, 0); c != nil {
		t.Fatalf("holder lost the lease to a refused handoff: %v", c)
	}
	snap, c := tb.Take("b", "", false, 0)
	if c != nil || snap.You != RoleController {
		t.Fatalf("idle handoff: %+v %v", snap, c)
	}
	if c := tb.Authorize("a", "", 0, false, 0); c == nil || c.Code != CodeLeaseHeld {
		t.Fatalf("old holder after handoff: %+v", c)
	}
}

func TestResetStalesEveryone(t *testing.T) {
	tb := NewTable(time.Minute, newClock().now)
	tb.Authorize("a", "", 0, false, 0)
	e := tb.View("a", false, 0).Epoch
	tb.Reset()
	if c := tb.Authorize("a", "", e, false, 0); c == nil || c.Code != CodeStaleLease {
		t.Fatalf("after reset: %+v", c)
	}
}

func TestReleaseByNonHolderChangesNothing(t *testing.T) {
	tb := NewTable(time.Minute, newClock().now)
	tb.Authorize("a", "", 0, false, 0)
	if tb.Release("b") {
		t.Fatal("non-holder released the lease")
	}
	if !tb.Release("a") {
		t.Fatal("holder could not release")
	}
	if c := tb.Authorize("b", "", 0, false, 0); c != nil {
		t.Fatalf("free session refused: %v", c)
	}
}

// Many clients race for a free session: exactly one wins.
func TestConcurrentClientsOneWinner(t *testing.T) {
	tb := NewTable(time.Minute, nil)
	var wins atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 64; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			if tb.Authorize(string(rune('a'+id%26))+string(rune('A'+id/26)), "", 0, false, 0) == nil {
				wins.Add(1)
			}
		}(i)
	}
	wg.Wait()
	if wins.Load() != 1 {
		t.Fatalf("%d clients got the lease, want 1", wins.Load())
	}
}

func TestWatchLeaveIsIdempotentAndCounted(t *testing.T) {
	tb := NewTable(time.Minute, nil)
	l1 := tb.Watch("o")
	l2 := tb.Watch("o")
	l1()
	l1()
	if v := tb.View("o", false, 0); v.You != RoleObserver {
		t.Fatalf("one of two attachments left, role %v", v.You)
	}
	l2()
	if v := tb.View("o", false, 0); v.You != RoleNone || v.Observers != 0 {
		t.Fatalf("all attachments gone: %+v", v)
	}
}

func TestSnapshotSame(t *testing.T) {
	a := Snapshot{Epoch: 1, Holder: &Holder{Client: "x", Expires: time.Unix(1, 0)}}
	b := Snapshot{Epoch: 1, Holder: &Holder{Client: "x", Expires: time.Unix(99, 0)}}
	if !a.Same(b) {
		t.Fatal("expiry-only difference should not count")
	}
	b.Holder.Client = "y"
	if a.Same(b) {
		t.Fatal("different holder reported the same")
	}
}
