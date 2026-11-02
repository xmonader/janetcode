package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAttemptLifecycle(t *testing.T) {
	s := OpenAttempts("")
	a1 := s.Begin(Attempt{Child: "task-1", Desc: "first"})
	a2 := s.Begin(Attempt{Child: "task-2", Desc: "second", Edit: true})
	if a1 != "a1" || a2 != "a2" {
		t.Fatalf("ids %s %s", a1, a2)
	}
	if s.OpenFor("task-2") != "a2" {
		t.Fatal("OpenFor")
	}
	s.Finish(a1, OutcomeOK, "")
	s.Finish(a1, OutcomeFailed, "late") // second completion ignored
	s.Finish("nope", OutcomeOK, "")
	if got, _ := s.Find("a1"); got.Outcome != OutcomeOK {
		t.Fatalf("a1 = %+v", got)
	}
	if got, ok := s.Find("task-2"); !ok || got.ID != "a2" {
		t.Fatalf("find by child: %+v", got)
	}
	if _, ok := s.Find("zzz"); ok {
		t.Fatal("unknown ref found")
	}
}

func TestAttemptReconcileTable(t *testing.T) {
	cases := []struct {
		name    string
		prep    func(s *AttemptStore)
		orphans int
	}{
		{"empty", func(*AttemptStore) {}, 0},
		{"all settled", func(s *AttemptStore) { s.Finish(s.Begin(Attempt{Child: "task-1"}), OutcomeOK, "") }, 0},
		{"one open", func(s *AttemptStore) { s.Begin(Attempt{Child: "task-1"}) }, 1},
		{"mixed", func(s *AttemptStore) {
			s.Begin(Attempt{Child: "task-1"})
			s.Finish(s.Begin(Attempt{Child: "task-2"}), OutcomeFailed, "x")
			s.Begin(Attempt{Child: "task-3"})
		}, 2},
	}
	for _, c := range cases {
		path := filepath.Join(t.TempDir(), "s.attempts.log")
		s := OpenAttempts(path)
		c.prep(s)
		// "restart": a new store over the same file
		r := OpenAttempts(path)
		got := r.Reconcile()
		if len(got) != c.orphans {
			t.Errorf("%s: %d orphans, want %d", c.name, len(got), c.orphans)
		}
		for _, a := range r.All() {
			if a.Open() {
				t.Errorf("%s: %s still open after reconcile", c.name, a.ID)
			}
		}
		// reconciling is durable: a second restart finds nothing
		if again := OpenAttempts(path).Reconcile(); len(again) != 0 {
			t.Errorf("%s: orphans came back: %v", c.name, again)
		}
		// ids stay unique after a restart
		if id := OpenAttempts(path).Begin(Attempt{Child: "x"}); id == "" || r.byID[id] != nil {
			t.Errorf("%s: id %q reused", c.name, id)
		}
	}
}

func TestAttemptStoreSurvivesGarbageAndCompacts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.attempts.log")
	os.WriteFile(path, []byte("garbage\n{\"rec\":\"completion\",\"id\":\"a9\",\"outcome\":\"ok\"}\n"), 0o600)
	s := OpenAttempts(path)
	open := s.Begin(Attempt{Child: "keep-me"})
	for i := 0; i < maxAttemptLines; i++ {
		s.Finish(s.Begin(Attempt{Child: "c"}), OutcomeOK, "")
	}
	r := OpenAttempts(path)
	if len(r.All()) > maxAttemptLines/2+10 {
		t.Errorf("not compacted: %d attempts", len(r.All()))
	}
	if got, ok := r.Find(open); !ok || !got.Open() {
		t.Error("open attempt lost in compaction")
	}
}

func TestOrphanNotice(t *testing.T) {
	if OrphanNotice(nil, nil) != "" {
		t.Fatal("empty must be empty")
	}
	n := OrphanNotice([]Attempt{{ID: "a1", Child: "task-1", Desc: "edit x"}}, func(string) int { return 2 })
	for _, w := range []string{"NOT rerun", "a1", "2 recorded", "/undo agent a1"} {
		if !strings.Contains(n, w) {
			t.Errorf("notice lacks %q: %s", w, n)
		}
	}
}

func TestRewindAttemptUnwindsOnlyThatChild(t *testing.T) {
	dir := t.TempDir()
	f1, f2 := filepath.Join(dir, "a.txt"), filepath.Join(dir, "b.txt")
	os.WriteFile(f1, []byte("child\n"), 0o644)
	os.WriteFile(f2, []byte("mine\n"), 0o644)
	a := &Agent{Attempts: OpenAttempts("")}
	id := a.Attempts.Begin(Attempt{Child: "task-1"})
	a.record(Change{Tool: "write", Path: f1, Existed: false, After: []byte("child\n"), Attempt: id})
	a.record(Change{Tool: "write", Path: f2, Existed: false, After: []byte("mine\n")})
	out, err := a.RewindAttempt("task-1")
	if err != nil || !strings.Contains(out, "1 of 1") {
		t.Fatalf("%q %v", out, err)
	}
	if _, err := os.Stat(f1); !os.IsNotExist(err) {
		t.Error("child's file should be gone")
	}
	if _, err := os.Stat(f2); err != nil {
		t.Error("parent's own file must stay")
	}
	if out, _ := a.RewindAttempt(id); !strings.Contains(out, "no changes left") {
		t.Errorf("second rewind = %q", out)
	}
	if _, err := a.RewindAttempt("nope"); err == nil {
		t.Error("unknown ref should error")
	}
}
