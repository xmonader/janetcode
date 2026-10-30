package session

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/xmonader/janetcode/internal/llm"
)

func TestModifyMetaKeepsFileTimeAndRefusesHeld(t *testing.T) {
	dir := t.TempDir()
	s, err := New(dir, "/p", "fake", "m")
	if err != nil {
		t.Fatal(err)
	}
	m := llm.TextMessage(llm.RoleUser, "hi")
	if _, err := s.Append(Event{Type: TypeUserMessage, Surface: true, Message: &m}); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-48 * time.Hour).Truncate(time.Second)
	if err := os.Chtimes(s.Path(), old, old); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name string
		id   string
		want error
	}{
		{"ok", s.Meta.ID, nil},
		{"unknown", "20000101-000000-nope", os.ErrNotExist},
	} {
		err := ModifyMeta(dir, c.id, func(m *Meta) { m.Title, m.Archived, m.Starred = "T", true, true })
		if !errors.Is(err, c.want) {
			t.Fatalf("%s: err = %v, want %v", c.name, err, c.want)
		}
	}
	got, _, err := Open(dir, s.Meta.ID)
	if err != nil || got.Meta.Title != "T" || !got.Meta.Archived || !got.Meta.Starred {
		t.Fatalf("meta = %+v, %v", got.Meta, err)
	}
	if st, _ := os.Stat(s.Path()); !st.ModTime().Equal(old) {
		t.Fatalf("mtime = %v, want %v", st.ModTime(), old)
	}
	if err := ModifyMeta(dir, "../x", func(*Meta) {}); err == nil {
		t.Fatal("path escape accepted")
	}
	if err := s.Acquire(); err != nil {
		t.Fatal(err)
	}
	defer s.Release()
	var held ErrHeld
	if err := ModifyMeta(dir, s.Meta.ID, func(*Meta) {}); !errors.As(err, &held) {
		t.Fatalf("held session modified: %v", err)
	}
	if err := Delete(dir, s.Meta.ID); !errors.As(err, &held) {
		t.Fatalf("held session deleted: %v", err)
	}
}

func TestDeleteRemovesSidecars(t *testing.T) {
	dir := t.TempDir()
	s, err := New(dir, "/p", "fake", "m")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.EnsureWritten(); err != nil {
		t.Fatal(err)
	}
	side := s.UndoPath()
	if err := os.WriteFile(side, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(dir, "20000101-000000-keep.jsonl")
	if err := os.WriteFile(other, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Delete(dir, s.Meta.ID); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{s.Path(), side} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("%s survived: %v", p, err)
		}
	}
	if _, err := os.Stat(other); err != nil {
		t.Errorf("another session was removed: %v", err)
	}
	if err := Delete(dir, s.Meta.ID); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("second delete = %v", err)
	}
}

func TestMessageCutAndForkAt(t *testing.T) {
	dir := t.TempDir()
	s, err := New(dir, "/p", "fake", "m")
	if err != nil {
		t.Fatal(err)
	}
	for i, x := range []struct {
		role llm.Role
		text string
	}{{llm.RoleUser, "one"}, {llm.RoleAssistant, "a1"}, {llm.RoleUser, "two"}, {llm.RoleAssistant, "a2"}} {
		m := llm.TextMessage(x.role, x.text)
		typ := TypeUserMessage
		if x.role == llm.RoleAssistant {
			typ = TypeAssistant
		}
		if _, err := s.Append(Event{Type: typ, Surface: true, Turn: i/2 + 1, Message: &m}); err != nil {
			t.Fatal(err)
		}
	}
	cases := []struct {
		n, where int
		ok       bool
		keeps    int
	}{
		{1, CutBefore, true, 0}, {1, CutAfter, true, 1}, {1, CutEndTurn, true, 2},
		{2, CutBefore, true, 2}, {2, CutAfter, true, 3}, {2, CutEndTurn, true, 4},
		{3, CutAfter, false, 0},
	}
	for _, c := range cases {
		cut, ok := MessageCut(s.Events(), c.n, c.where)
		if ok != c.ok {
			t.Fatalf("n=%d where=%d ok=%v", c.n, c.where, ok)
		}
		if !ok {
			continue
		}
		kept := 0
		for _, e := range s.Events() {
			if cut == 0 || e.Seq < cut {
				kept++
			}
		}
		if kept != c.keeps && !(cut == 0 && c.keeps == 4) {
			t.Errorf("n=%d where=%d keeps %d events, want %d (cut %d)", c.n, c.where, kept, c.keeps, cut)
		}
		ns, err := ForkAt(dir, s, cut, "")
		if err != nil {
			t.Fatal(err)
		}
		if got := len(FoldSurface(ns.Events())); got != c.keeps {
			t.Errorf("fork n=%d where=%d holds %d messages, want %d", c.n, c.where, got, c.keeps)
		}
	}
}
