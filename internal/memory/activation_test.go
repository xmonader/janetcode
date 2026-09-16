package memory

import (
	"testing"
	"time"
)

// Activation: an entry nobody has used is sampled at its last update, a
// use today is warm, uses pile up, and ancient use cools off.
func TestActivationDecayAndFrequency(t *testing.T) {
	now := time.Now()
	fresh := Entry{Updated: now.Add(-time.Hour)}
	used := Entry{Used: []time.Time{now.Add(-time.Hour)}}
	usedTwice := Entry{Used: []time.Time{now.Add(-2 * time.Hour), now.Add(-time.Hour)}}
	stale := Entry{Used: []time.Time{now.Add(-60 * 24 * time.Hour)}}
	none := Entry{}
	if activation(fresh, now) <= 0 {
		t.Fatalf("a freshly updated entry should start warm: %f", activation(fresh, now))
	}
	if activation(usedTwice, now) <= activation(used, now) {
		t.Fatalf("more uses must rank higher: %f vs %f", activation(usedTwice, now), activation(used, now))
	}
	if activation(stale, now) >= activation(used, now) {
		t.Fatalf("a sixty-day-old use must not beat an hour-old one: %f vs %f", activation(stale, now), activation(used, now))
	}
	if activation(none, now) != 0 {
		t.Fatalf("no dates at all = %f, want 0", activation(none, now))
	}
}

// Two entries tie on word overlap; the one the work keeps needing ranks
// first. With PlainRanking (the memoryrank switch off) the order is the
// flat lexical one again.
func TestRecallRanksByActivation(t *testing.T) {
	s := Open(t.TempDir(), t.TempDir())
	// Identical query-word scores, different filler words so Put's
	// similarity merge does not fold one into the other.
	s.Put(Entry{Name: "alpha-deploy", Description: "Deploy the api with ansible", Body: "run it from ci", Source: "auto"})
	s.Put(Entry{Name: "beta-deploy", Description: "Deploy the web tier with ansible", Body: "run it from ci", Source: "auto"})
	// beta is hot, alpha has not been touched since it was written.
	s.Touch(mustGet(t, s, "beta-deploy"))
	s.Touch(mustGet(t, s, "beta-deploy"))
	// beta is hot but was written long ago; alpha is fresh and unused.
	// Activation ranking puts the hot one first, plain ranking the fresh
	// one.
	backdateUpdated(t, s, "beta-deploy", 30*24*time.Hour)
	got := s.Recall("deploy the service with ansible", 4)
	if len(got) != 2 || got[0].Name != "beta-deploy" {
		t.Fatalf("recall order = %+v", got)
	}
	s.PlainRanking = true
	got = s.Recall("deploy the service with ansible", 4)
	if len(got) != 2 || got[0].Name != "alpha-deploy" {
		t.Fatalf("plain recall order = %+v, want the flat order (updated first)", got)
	}
}

// Activation orders the entries relevance admitted; it never admits one
// on its own -- a hot entry that shares no words with the turn is not
// recalled.
func TestActivationNeverAdmitsAnIrrelevantEntry(t *testing.T) {
	s := Open(t.TempDir(), t.TempDir())
	s.Put(Entry{Name: "hot", Description: "Unrelated topic entirely", Body: "nothing to do with the query", Source: "auto"})
	for i := 0; i < 10; i++ {
		s.Touch(mustGet(t, s, "hot"))
	}
	if got := s.Recall("fix the typo in the parser", 4); len(got) != 0 {
		t.Fatalf("irrelevant hot entry recalled: %+v", got)
	}
}

// A correction from the user outranks an automatic project note at equal
// relevance and use.
func TestTypePriorFavoursTheUsersWords(t *testing.T) {
	s := Open(t.TempDir(), t.TempDir())
	// Same relevance to the query, same age; only the type differs. (The
	// descriptions differ in filler so the similarity merge keeps both.)
	s.Put(Entry{Name: "zz-note", Description: "Database migrations run through goose on ci", Body: "never edit applied migrations", Source: "auto", Type: TypeProject})
	s.Put(Entry{Name: "aa-note", Description: "Database migrations via goose when local", Body: "never edit applied migrations", Source: "user", Type: TypeFeedback})
	backdate(t, s, "zz-note", time.Hour)
	backdate(t, s, "aa-note", time.Hour)
	got := s.Recall("add a database migration", 4)
	if len(got) != 2 || got[0].Type != TypeFeedback {
		t.Fatalf("recall order = %+v, want the feedback entry first", got)
	}
}

// A /remember entry survives an automatic flood past the cap.
func TestTrimNeverEvictsTheUsersEntries(t *testing.T) {
	s := Open(t.TempDir(), t.TempDir())
	if _, _, err := s.Put(Entry{Name: "mine", Description: "My own note", Body: "keep me", Source: "user"}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < MaxEntries+5; i++ {
		if _, _, err := s.Put(Entry{Name: "auto-" + itoa(i), Description: "Auto " + itoa(i), Body: "b", Source: "auto"}); err != nil {
			t.Fatal(err)
		}
	}
	if _, ok := s.Get("mine"); !ok {
		t.Fatal("a user-written entry was evicted by automatic ones")
	}
	n := 0
	for _, e := range s.Entries() {
		if e.Scope == "project" && e.Source != "user" {
			n++
		}
	}
	if n > MaxEntries {
		t.Fatalf("automatic entries after overflow = %d, cap is %d", n, MaxEntries)
	}
}

// Touch records the use both as a count and as a timestamp, and the file
// round-trips the history.
func TestTouchRecordsUseHistory(t *testing.T) {
	s := Open(t.TempDir(), t.TempDir())
	s.Put(Entry{Name: "n", Description: "A note", Body: "b", Source: "auto"})
	s.Touch(mustGet(t, s, "n"))
	s.Touch(mustGet(t, s, "n"))
	e := mustGet(t, s, "n")
	if e.Uses != 2 || len(e.Used) != 2 {
		t.Fatalf("uses=%d history=%d, want 2 and 2", e.Uses, len(e.Used))
	}
	// The history survives a fresh read of the file.
	fresh, ok := readEntry(e.Path, e.Scope)
	if !ok || len(fresh.Used) != 2 {
		t.Fatalf("reloaded history = %+v", fresh.Used)
	}
}

func mustGet(t *testing.T, s *Store, name string) Entry {
	t.Helper()
	e, ok := s.Get(name)
	if !ok {
		t.Fatalf("no entry %q", name)
	}
	return e
}

// backdate rewrites an entry's Updated and Used as if everything happened
// d ago, so activation has something measurable to rank.
func backdate(t *testing.T, s *Store, name string, d time.Duration) {
	t.Helper()
	e := mustGet(t, s, name)
	e.Updated = e.Updated.Add(-d)
	for i := range e.Used {
		e.Used[i] = e.Used[i].Add(-d)
	}
	if err := s.write(e); err != nil {
		t.Fatal(err)
	}
}

// backdateUpdated ages only the entry's Updated, leaving its use history
// recent, so plain (updated-first) and activation (use-first) orderings
// diverge.
func backdateUpdated(t *testing.T, s *Store, name string, d time.Duration) {
	t.Helper()
	e := mustGet(t, s, name)
	e.Updated = e.Updated.Add(-d)
	if err := s.write(e); err != nil {
		t.Fatal(err)
	}
}
