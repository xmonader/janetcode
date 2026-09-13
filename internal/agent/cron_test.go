package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/xmonader/janetcode/internal/session"
)

func TestParseEvery(t *testing.T) {
	d, err := ParseEvery("5m")
	if err != nil || d != 5*time.Minute {
		t.Fatalf("5m = %v %v", d, err)
	}
	if _, err := ParseEvery("0.5s"); err == nil {
		t.Fatal("sub-second should fail")
	}
	if _, err := ParseEvery("nope"); err == nil {
		t.Fatal("garbage should fail")
	}
}

func TestCronFiresThenCloseStops(t *testing.T) {
	c := NewCron()
	got := make(chan string, 4)
	c.OnFire = func(id, prompt string) { got <- id + ":" + prompt }
	id, err := c.Add(time.Second, "check tests")
	if err != nil {
		t.Fatal(err)
	}
	if id != "loop-1" {
		t.Fatalf("id = %s", id)
	}
	select {
	case s := <-got:
		if s != "loop-1:check tests" {
			t.Fatalf("fire = %q", s)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("did not fire")
	}
	c.Close()
	select {
	case s := <-got:
		t.Fatalf("fired after close: %s", s)
	case <-time.After(1500 * time.Millisecond):
	}
	if _, err := c.Add(time.Second, "x"); err == nil {
		t.Fatal("add after close should fail")
	}
}

func TestCronCancelAndList(t *testing.T) {
	c := NewCron()
	id, err := c.Add(time.Hour, "later")
	if err != nil {
		t.Fatal(err)
	}
	if n := len(c.List()); n != 1 {
		t.Fatalf("list = %d", n)
	}
	if !c.Cancel(id) {
		t.Fatal("cancel missed")
	}
	if c.Cancel(id) {
		t.Fatal("second cancel")
	}
	if n := len(c.List()); n != 0 {
		t.Fatalf("list after cancel = %d", n)
	}
}

func TestCronTool(t *testing.T) {
	c := NewCron()
	tl := &CronTool{Cron: c}
	out, err := tl.Run(context.Background(), json.RawMessage(`{"action":"add","every":"5m","prompt":"ping"}`))
	if err != nil || !strings.Contains(out, "loop-1") {
		t.Fatalf("add = %q %v", out, err)
	}
	list, err := tl.Run(context.Background(), json.RawMessage(`{"action":"list"}`))
	if err != nil || !strings.Contains(list, "ping") {
		t.Fatalf("list = %q %v", list, err)
	}
	if _, err := tl.Run(context.Background(), json.RawMessage(`{"action":"cancel","id":"loop-1"}`)); err != nil {
		t.Fatal(err)
	}
	c.Close()
}

func TestCronRestoreAndPersist(t *testing.T) {
	dir := t.TempDir()
	s, err := session.New(dir, "/w", "p", "m")
	if err != nil {
		t.Fatal(err)
	}
	c := NewCron()
	c.OnChange = func(specs []session.LoopSpec) { _ = s.SetLoops(specs) }
	id, err := c.Add(time.Hour, "ping")
	if err != nil {
		t.Fatal(err)
	}
	re, _, err := session.Open(dir, s.Meta.ID)
	if err != nil || len(re.Meta.Loops) != 1 || re.Meta.Loops[0].ID != id {
		t.Fatalf("saved = %+v err=%v", re.Meta.Loops, err)
	}
	c2 := NewCron()
	if err := c2.Restore(re.Meta.Loops); err != nil {
		t.Fatal(err)
	}
	got := c2.List()
	if len(got) != 1 || got[0].Prompt != "ping" {
		t.Fatalf("restored = %+v", got)
	}
	c.Cancel(id)
	re, _, _ = session.Open(dir, s.Meta.ID)
	if len(re.Meta.Loops) != 0 {
		t.Fatalf("cancel should persist empty, got %+v", re.Meta.Loops)
	}
	other, _ := session.New(dir, "/w", "p", "m")
	c3 := NewCron()
	if err := c3.Restore(other.Meta.Loops); err != nil {
		t.Fatal(err)
	}
	if n := len(c3.List()); n != 0 {
		t.Fatalf("other session inherited loops: %d", n)
	}
	c.Close()
	c2.Close()
	c3.Close()
}
