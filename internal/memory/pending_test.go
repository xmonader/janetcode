package memory

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func confirmStore(t *testing.T) *Store {
	s := &Store{Root: t.TempDir(), Key: "proj"}
	s.Confirm.Store(true)
	return s
}

func TestConfirmHoldsModelAndExtractorWrites(t *testing.T) {
	s := confirmStore(t)
	// The user's own /remember is saved at once.
	if _, st, err := s.Put(Entry{Body: "the user prefers tabs", Source: "user"}); err != nil || st != Created {
		t.Fatalf("user write = %v %v", st, err)
	}
	e, st, err := s.Put(Entry{Name: "build", Description: "build with make", Body: "run make", Source: "model"})
	if err != nil || st != Held || !e.Pending {
		t.Fatalf("model write = %+v %v %v", e, st, err)
	}
	for _, x := range s.Entries() {
		if x.Name == "build" {
			t.Fatal("a held entry is already in memory")
		}
	}
	if strings.Contains(s.Index(), "build with make") {
		t.Fatal("a held entry reached the prompt index")
	}
	changed, err := s.Apply([]Extracted{{Name: "ci", Description: "CI runs on push", Body: "CI runs on push", Evidence: []string{"sess#~4"}}})
	if err != nil || len(changed) != 1 || !changed[0].Pending {
		t.Fatalf("extraction = %+v %v", changed, err)
	}
	listing := RenderPending(s.Pending())
	for _, want := range []string{"build", "ci", "sess#~4", "project"} {
		if !strings.Contains(listing, want) {
			t.Errorf("pending listing lacks %q:\n%s", want, listing)
		}
	}
	saved, err := s.Approve("build")
	if err != nil || len(saved) != 1 {
		t.Fatalf("approve = %+v %v", saved, err)
	}
	if _, ok := s.Get("build"); !ok {
		t.Fatal("approved entry not in memory")
	}
	if _, err := s.Reject("all"); err != nil {
		t.Fatal(err)
	}
	if len(s.Pending()) != 0 {
		t.Fatal("reject all left something pending")
	}
	if _, ok := s.Get("ci"); ok {
		t.Fatal("a rejected entry was saved")
	}
}

func TestApprovedCorrectionRetiresWhatItReplaces(t *testing.T) {
	s := confirmStore(t)
	s.Confirm.Store(false)
	if _, _, err := s.Put(Entry{Name: "port", Description: "the server listens on 8080", Body: "8080", Source: "auto"}); err != nil {
		t.Fatal(err)
	}
	s.Confirm.Store(true)
	changed, err := s.Apply([]Extracted{{Name: "port-9090", Description: "the dev server listens on port 9090 since the move", Body: "9090", Replaces: "port"}})
	if err != nil || len(changed) != 1 {
		t.Fatal(changed, err)
	}
	if p := s.Pending(); len(p) != 1 || p[0].Replaces != "port" || !strings.Contains(RenderPending(p), "replaces port") {
		t.Fatalf("pending = %+v", p)
	}
	if old, _ := s.Get("port"); old.Closed() {
		t.Fatal("the old entry was retired before the correction was approved")
	}
	if _, err := s.Approve("1"); err != nil {
		t.Fatal(err)
	}
	if old, _ := s.Get("port"); !old.Closed() {
		t.Fatal("approving the correction did not retire the old entry")
	}
}

func TestMemoryToolSaysHeld(t *testing.T) {
	s := confirmStore(t)
	in, _ := json.Marshal(map[string]string{"action": "remember", "name": "x", "description": "a fact", "body": "a fact"})
	out, err := (&Tool{Store: s}).Run(context.Background(), in)
	if err != nil || !strings.Contains(out, "Held for the user's confirmation") {
		t.Fatalf("tool = %q %v", out, err)
	}
}

// What the model saves is held with the session record it was saved at,
// so /memory pending can say which turn it came from; an entry with no
// record says that too instead of saying nothing.
func TestHeldModelEntryNamesItsSourceTurn(t *testing.T) {
	s := confirmStore(t)
	save := func(name string, origin func() string) {
		in, _ := json.Marshal(map[string]string{"action": "remember", "name": name, "description": "fact " + name, "body": "fact " + name})
		if _, err := (&Tool{Store: s, Origin: origin}).Run(context.Background(), in); err != nil {
			t.Fatal(err)
		}
	}
	save("with-origin", func() string { return "sess-1#~42" })
	save("without-origin", nil)
	listing := RenderPending(s.Pending())
	if !strings.Contains(listing, "learned at sess-1#~42") {
		t.Errorf("the source turn is not shown:\n%s", listing)
	}
	if !strings.Contains(listing, "source turn: not recorded") {
		t.Errorf("a missing source is not said:\n%s", listing)
	}
}

func TestConfirmCommand(t *testing.T) {
	s := &Store{Root: t.TempDir(), Key: "proj"}
	if out, ok, _ := s.ConfirmCommand("confirm on"); !ok || !s.Confirm.Load() || !strings.Contains(out, "on") {
		t.Fatalf("confirm on = %q %v", out, ok)
	}
	if _, ok, _ := s.ConfirmCommand("some words"); ok {
		t.Fatal("a search was taken as a confirmation command")
	}
	s.Confirm.Store(false)
	_, _, _ = s.Put(Entry{Name: "old", Description: "an old fact", Body: "old", Source: "user"})
	if out, ok, err := s.ConfirmCommand("expire old"); !ok || err != nil || !strings.Contains(out, "expired") {
		t.Fatalf("expire = %q %v", out, err)
	}
	if e, _ := s.Get("old"); !e.Closed() {
		t.Fatal("expire did not retire the entry")
	}
}
