package session

import (
	"context"
	"os"
	"sync"
	"testing"
	"time"
)

type collector struct {
	mu  sync.Mutex
	got []Event
}

func (c *collector) add(e Event) error {
	c.mu.Lock()
	c.got = append(c.got, e)
	c.mu.Unlock()
	return nil
}

func (c *collector) names() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []string
	for _, e := range c.got {
		out = append(out, e.Type+":"+e.Name)
	}
	return out
}

func (c *collector) waitFor(t *testing.T, n int) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		c.mu.Lock()
		have := len(c.got)
		c.mu.Unlock()
		if have >= n {
			return
		}
	}
	t.Fatalf("followed %d events, want %d: %v", len(c.got), n, c.names())
}

func startFollow(t *testing.T, path string, after int) (*collector, func() error) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	c := &collector{}
	done := make(chan error, 1)
	go func() { done <- Follow(ctx, path, after, 10*time.Millisecond, c.add) }()
	return c, func() error { cancel(); return <-done }
}

func note(name string) Event { return Event{Type: TypeNotice, Name: name} }

// An observer gets the log so far and then every append, once and in
// order, without ever taking the lease.
func TestFollowDeliversHistoryThenAppends(t *testing.T) {
	s, _ := leasedSession(t)
	for _, n := range []string{"a", "b"} {
		if _, err := s.Append(note(n)); err != nil {
			t.Fatal(err)
		}
	}
	c, stop := startFollow(t, s.Path(), 0)
	c.waitFor(t, 2)
	for _, n := range []string{"c", "d"} {
		if _, err := s.Append(note(n)); err != nil {
			t.Fatal(err)
		}
	}
	c.waitFor(t, 4)
	if err := stop(); err != context.Canceled {
		t.Fatalf("Follow ended with %v, want context.Canceled", err)
	}
	want := []string{"notice:a", "notice:b", "notice:c", "notice:d"}
	for i, w := range want {
		if c.names()[i] != w {
			t.Fatalf("followed %v, want %v", c.names(), want)
		}
	}
	if s.Leased() {
		t.Fatal("this test never acquired the lease")
	}
	if _, ok := Holder(s.Path()); ok {
		t.Fatal("following took the session's lease")
	}
}

func TestFollowAfterSkipsWhatTheObserverHas(t *testing.T) {
	s, _ := leasedSession(t)
	var last int
	for _, n := range []string{"a", "b", "c"} {
		seq, err := s.Append(note(n))
		if err != nil {
			t.Fatal(err)
		}
		last = seq
	}
	c, stop := startFollow(t, s.Path(), last-1)
	c.waitFor(t, 1)
	_ = stop()
	if got := c.names(); len(got) != 1 || got[0] != "notice:c" {
		t.Fatalf("followed %v after seq %d", got, last-1)
	}
}

// A line the owner is halfway through writing is not delivered torn.
func TestFollowHoldsBackAPartialLine(t *testing.T) {
	s, _ := leasedSession(t)
	if _, err := s.Append(note("a")); err != nil {
		t.Fatal(err)
	}
	c, stop := startFollow(t, s.Path(), 0)
	defer stop()
	c.waitFor(t, 1)

	f, err := os.OpenFile(s.Path(), os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(`{"type":"event","seq":99,"t":"notice","name":"half`); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	if n := len(c.names()); n != 1 {
		t.Fatalf("a torn line was delivered: %v", c.names())
	}
	if _, err := f.WriteString("\"}\n"); err != nil {
		t.Fatal(err)
	}
	c.waitFor(t, 2)
	if got := c.names()[1]; got != "notice:half" {
		t.Fatalf("completed line delivered as %q", got)
	}
}

// A session not yet written is waited for, not an error.
func TestFollowWaitsForTheFile(t *testing.T) {
	dir := t.TempDir()
	s, err := New(dir, "/work", "p", "m")
	if err != nil {
		t.Fatal(err)
	}
	c, stop := startFollow(t, s.Path(), 0)
	defer stop()
	time.Sleep(50 * time.Millisecond)
	if _, err := s.Append(note("first")); err != nil {
		t.Fatal(err)
	}
	c.waitFor(t, 1)
}

// A rewrite of the file (rewind, compaction, torn-line repair) is
// announced and the log is delivered again from its start.
func TestFollowAnnouncesARewrite(t *testing.T) {
	s, _ := leasedSession(t)
	for _, n := range []string{"a", "b", "c"} {
		if _, err := s.Append(note(n)); err != nil {
			t.Fatal(err)
		}
	}
	c, stop := startFollow(t, s.Path(), 0)
	defer stop()
	c.waitFor(t, 3)

	s.mu.Lock()
	s.ev = s.ev[:len(s.ev)-1] // the last event is rewound away
	err := s.rewriteLocked()
	s.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	c.waitFor(t, 3+1+2)
	got := c.names()[3:]
	want := []string{TypeFollowReset + ":", "notice:a", "notice:b"}
	for i, w := range want {
		if got[i] != w {
			t.Fatalf("after the rewrite followed %v, want %v", got, want)
		}
	}
	// Events appended after the rewrite reuse a sequence number the observer
	// saw before it; they must still arrive.
	if _, err := s.Append(note("c2")); err != nil {
		t.Fatal(err)
	}
	c.waitFor(t, 3+1+2+1)
	if got := c.names(); got[len(got)-1] != "notice:c2" {
		t.Fatalf("event after rewrite lost: %v", got)
	}
}

func TestFollowStopsWithTheCallback(t *testing.T) {
	s, _ := leasedSession(t)
	if _, err := s.Append(note("a")); err != nil {
		t.Fatal(err)
	}
	stopErr := os.ErrClosed
	err := Follow(context.Background(), s.Path(), 0, 10*time.Millisecond, func(Event) error { return stopErr })
	if err != stopErr {
		t.Fatalf("Follow returned %v, want the callback's error", err)
	}
}
