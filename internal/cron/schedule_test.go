package cron

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/xmonader/janetcode/internal/session"
)

func TestRestoreSkipsUnusableSpecsInsteadOfFailingTheResume(t *testing.T) {
	c := New()
	defer c.Close()
	err := c.Restore([]session.LoopSpec{
		{ID: "loop-1", Every: "nonsense", Prompt: "bad interval"},
		{ID: "loop-2", Every: "1h", Prompt: "   "},
		{ID: "loop-3", Every: "1h", Prompt: "good"},
		{ID: "loop-3", Every: "1h", Prompt: "duplicate id"},
	})
	if err != nil {
		t.Fatalf("a corrupt entry must not fail the resume: %v", err)
	}
	got := c.List()
	if len(got) != 1 || got[0].ID != "loop-3" || got[0].Prompt != "good" {
		t.Fatalf("restored = %+v", got)
	}
}

func TestRestoreIsBoundedAndKeepsIdsUnique(t *testing.T) {
	c := New()
	defer c.Close()
	// An id-less spec first: it must not be minted onto an id a later
	// spec claims, or that later spec would be silently dropped.
	specs := []session.LoopSpec{{Every: "1h", Prompt: "no id"}}
	for i := 0; i < 40; i++ {
		specs = append(specs, session.LoopSpec{ID: fmt.Sprintf("loop-%d", i+1), Every: "1h", Prompt: "p"})
	}
	if err := c.Restore(specs); err != nil {
		t.Fatal(err)
	}
	got := c.List()
	if len(got) != maxJobs {
		t.Fatalf("restored %d jobs, want the %d cap", len(got), maxJobs)
	}
	seen := map[string]bool{}
	for _, j := range got {
		if seen[j.ID] {
			t.Fatalf("duplicate id %s in %+v", j.ID, got)
		}
		seen[j.ID] = true
	}
	if got[0].Prompt != "no id" || got[0].ID == "loop-1" {
		t.Fatalf("id-less spec collided with an explicit id: %+v", got[0])
	}
	if _, err := c.Add(time.Hour, "x"); err == nil {
		t.Fatal("add past the cap should fail")
	}
}

func TestListAndCancelSeeRestoredJobsWithForeignIds(t *testing.T) {
	c := New()
	defer c.Close()
	if err := c.Restore([]session.LoopSpec{{ID: "nightly", Every: "1h", Prompt: "run the suite"}}); err != nil {
		t.Fatal(err)
	}
	got := c.List()
	if len(got) != 1 || got[0].ID != "nightly" {
		t.Fatalf("a job whose id is not loop-N is invisible: %+v", got)
	}
	if specs := c.Specs(); len(specs) != 1 || specs[0].ID != "nightly" {
		t.Fatalf("it would be dropped from the session file: %+v", specs)
	}
	if !c.Cancel("nightly") {
		t.Fatal("cancel missed it")
	}
}

func TestAddIsCappedAndValidated(t *testing.T) {
	c := New()
	defer c.Close()
	for i := 0; i < maxJobs; i++ {
		if _, err := c.Add(time.Hour, "p"); err != nil {
			t.Fatalf("add %d: %v", i, err)
		}
	}
	if _, err := c.Add(time.Hour, "p"); err == nil {
		t.Fatal("past the cap should fail")
	}
	if _, err := c.Add(48*time.Hour, "p"); err == nil {
		t.Fatal("over-long interval should fail")
	}
	if _, err := c.Add(time.Hour, "   "); err == nil {
		t.Fatal("empty prompt should fail")
	}
}

func TestOnChangeNeverGoesBackwards(t *testing.T) {
	c := New()
	defer c.Close()
	var mu sync.Mutex
	var sizes []int
	c.SetOnChange(func(specs []session.LoopSpec) {
		mu.Lock()
		sizes = append(sizes, len(specs))
		mu.Unlock()
	})
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if id, err := c.Add(time.Hour, "p"); err == nil {
				c.Cancel(id)
			}
		}()
	}
	wg.Wait()
	mu.Lock()
	last := sizes[len(sizes)-1]
	mu.Unlock()
	if n := len(c.List()); last != n {
		t.Fatalf("last persisted snapshot had %d jobs, scheduler has %d", last, n)
	}
}

func TestSetOnFireIsSafeWhileJobsAreArmed(t *testing.T) {
	c := New()
	defer c.Close()
	fired := make(chan string, 1)
	if _, err := c.Add(time.Second, "p"); err != nil {
		t.Fatal(err)
	}
	c.SetOnFire(func(id, prompt string) {
		select {
		case fired <- id:
		default:
		}
	})
	select {
	case <-fired:
	case <-time.After(3 * time.Second):
		t.Fatal("did not fire")
	}
}

func TestToolRejectsCancelledContextAndBadAction(t *testing.T) {
	c := New()
	defer c.Close()
	tl := &Tool{Cron: c}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := tl.Run(ctx, json.RawMessage(`{"action":"list"}`)); err == nil {
		t.Fatal("a cancelled context should stop the tool")
	}
	if _, err := tl.Run(context.Background(), json.RawMessage(`{"action":"nope"}`)); err == nil {
		t.Fatal("unknown action should fail")
	}
	if _, err := tl.Run(context.Background(), json.RawMessage(`not json`)); err == nil {
		t.Fatal("malformed input should fail")
	}
	if _, err := tl.Run(context.Background(), json.RawMessage(`{"action":"cancel","id":" "}`)); err == nil {
		t.Fatal("cancel without an id should fail")
	}
}

func TestLoopIndex(t *testing.T) {
	for _, tc := range []struct {
		id string
		n  int
		ok bool
	}{{"loop-1", 1, true}, {"loop-42", 42, true}, {"loop-", 0, false}, {"loop-0", 0, false},
		{"nightly", 0, false}, {"loop-1x", 0, false}, {"loop--1", 0, false}, {"", 0, false}} {
		n, ok := loopIndex(tc.id)
		if n != tc.n || ok != tc.ok {
			t.Errorf("loopIndex(%q) = %d,%v want %d,%v", tc.id, n, ok, tc.n, tc.ok)
		}
	}
}
