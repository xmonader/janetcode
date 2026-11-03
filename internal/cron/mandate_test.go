package cron

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/xmonader/janetcode/internal/session"
)

func lastKind(j *Journal, id string) Occurrence {
	rec := j.Recent(1, id)
	if len(rec) == 0 {
		return Occurrence{}
	}
	return rec[0]
}

func TestObserveJobRunsNothing(t *testing.T) {
	j := OpenJournal("")
	j.Append(Occurrence{Time: time.Now().Add(-5 * time.Hour), Job: "loop-1", Kind: KindFired})
	c := New()
	defer c.Close()
	c.SetJournal(j)
	got := make(chan string, 4)
	c.SetOnFire(func(id, p string) { got <- id })
	_ = c.Restore([]session.LoopSpec{{ID: "loop-1", Every: "1h", Prompt: "deploy the thing", Mode: ModeObserve}})
	deadline := time.Now().Add(5 * time.Second)
	var o Occurrence
	for time.Now().Before(deadline) {
		if o = lastKind(j, "loop-1"); o.Kind == KindObserved {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if o.Kind != KindObserved {
		t.Fatalf("want observed, got %+v", o)
	}
	for _, want := range []string{"would have run: deploy the thing", "every 1h", "catch-up of 5 missed"} {
		if !strings.Contains(o.Detail, want) {
			t.Errorf("detail %q lacks %q", o.Detail, want)
		}
	}
	select {
	case <-got:
		t.Fatal("an observe job must not fire")
	default:
	}
	if c.List()[0].Mode != ModeObserve || !strings.Contains(c.List()[0].Line(), "[observe]") {
		t.Errorf("listing should show observe: %q", c.List()[0].Line())
	}
}

func TestObserveModeSurvivesPersistAndFlipsToRun(t *testing.T) {
	c := New()
	defer c.Close()
	var saved []session.LoopSpec
	c.SetOnChange(func(s []session.LoopSpec) { saved = s })
	id, err := c.AddMode(time.Hour, "x", ModeObserve)
	if err != nil || saved[0].Mode != ModeObserve {
		t.Fatalf("add observe: %v %+v", err, saved)
	}
	if _, ok, err := c.Control("run the tests"); ok || err != nil {
		t.Fatal("a prompt starting with run is not a subcommand")
	}
	if _, ok, err := c.Control("run " + id); !ok || err != nil {
		t.Fatalf("run: %v %v", ok, err)
	}
	if saved[0].Mode != ModeRun {
		t.Errorf("mode after run = %q", saved[0].Mode)
	}
}

func TestToolAddObserveButNoFlip(t *testing.T) {
	c := New()
	defer c.Close()
	tl := &Tool{Cron: c}
	in, _ := json.Marshal(map[string]string{"action": "add", "every": "1h", "prompt": "p", "mode": "observe"})
	if _, err := tl.Run(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	if c.List()[0].Mode != ModeObserve {
		t.Fatal("tool add mode=observe ignored")
	}
	in, _ = json.Marshal(map[string]string{"action": "run", "id": "loop-1"})
	if _, err := tl.Run(context.Background(), in); err == nil {
		t.Fatal("the model must not be able to flip a job to run")
	}
}

func TestWidened(t *testing.T) {
	base := func() Posture {
		return Posture{Trust: "instructions", Mode: "ask", Allow: []string{"bash(go test)"}, Deny: []string{"bash(rm)"}}
	}
	m := *freeze(base(), time.Now())
	with := func(f func(p *Posture)) Posture { p := base(); f(&p); return p }
	cases := []struct {
		name string
		p    Posture
		want string // substring of the widening; "" means not wider
	}{
		{"same", base(), ""},
		{"narrower", Posture{Trust: "untrusted", Mode: "read-only", Deny: []string{"bash(rm)"}}, ""},
		{"trust", with(func(p *Posture) { p.Trust = "full" }), "trust instructions -> full"},
		{"mode", with(func(p *Posture) { p.Mode = "allow-all" }), "mode ask -> allow-all"},
		{"new allow", with(func(p *Posture) { p.Allow = append(p.Allow, "bash(git push)") }), "new allow bash(git push)"},
		{"deny dropped", with(func(p *Posture) { p.Deny = nil }), "deny dropped bash(rm)"},
		{"unknown mode", with(func(p *Posture) { p.Mode = "yolo2" }), "mode ask -> yolo2"},
	}
	for _, tc := range cases {
		got := strings.Join(tc.p.Widened(m), ";")
		if (tc.want == "") != (got == "") || !strings.Contains(got, tc.want) {
			t.Errorf("%s: widened = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestMandateChangedSkipsUntilReapproved(t *testing.T) {
	j := OpenJournal("")
	c := New()
	defer c.Close()
	c.SetJournal(j)
	cur := &Posture{Trust: "full", Mode: "ask"}
	c.SetPosture(func() Posture { return *cur })
	id, err := c.Add(time.Hour, "tidy")
	if err != nil {
		t.Fatal(err)
	}
	if m := c.List()[0].Mandate; m == nil || m.Mode != "ask" || m.At.IsZero() {
		t.Fatalf("mandate not frozen at add: %+v", m)
	}
	got := make(chan string, 8)
	c.SetOnFire(func(id, p string) { got <- id })
	e := c.jobs[id]

	c.fire(id, e, KindFired, 0) // same posture: runs
	if len(got) != 1 {
		t.Fatal("same posture should run")
	}
	cur.Mode = "allow-all" // widened while down
	c.fire(id, e, KindCatchup, 3)
	if len(got) != 1 {
		t.Fatal("a wider posture must not run")
	}
	o := lastKind(j, id)
	if o.Kind != KindMandateChanged || !strings.Contains(o.Detail, "mode ask -> allow-all") || o.Missed != 3 {
		t.Fatalf("journal = %+v", o)
	}
	if c.List()[0].Mandate.Mode != "ask" {
		t.Fatal("the mandate must stay frozen")
	}
	cur.Mode = "read-only" // narrower runs under current
	c.fire(id, e, KindFired, 0)
	if len(got) != 2 {
		t.Fatal("a narrower posture should run")
	}
	cur.Mode = "allow-all"
	if _, ok, err := c.Control("approve " + id); !ok || err != nil {
		t.Fatalf("approve: %v %v", ok, err)
	}
	c.fire(id, e, KindFired, 0)
	if len(got) != 3 || c.List()[0].Mandate.Mode != "allow-all" {
		t.Fatal("re-approved job should run under the refreshed mandate")
	}
}

func TestLegacyJobFreezesAtFirstFire(t *testing.T) {
	c := New()
	defer c.Close()
	_ = c.Restore([]session.LoopSpec{{ID: "loop-1", Every: "1h", Prompt: "p"}})
	c.SetPosture(func() Posture { return Posture{Trust: "full", Mode: "ask"} })
	c.SetOnFire(func(string, string) {})
	c.fire("loop-1", c.jobs["loop-1"], KindFired, 0)
	if c.List()[0].Mandate == nil {
		t.Fatal("a job saved before mandates should be frozen at its first firing")
	}
}

func TestRulesText(t *testing.T) {
	for _, w := range []string{"stop or wait", "never resume", "git log/status", "Never report unfinished work as done"} {
		if !strings.Contains(Rules, w) {
			t.Errorf("rules lack %q", w)
		}
	}
}
