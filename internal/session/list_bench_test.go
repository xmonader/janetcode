package session

import (
	"fmt"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/llm"
)

// fillDir writes n sessions for other and one for mine, each with events
// heavy enough that parsing them is the cost of a listing.
func fillDir(tb testing.TB, dir, mine, other string, n, events int) {
	tb.Helper()
	for i := 0; i < n+1; i++ {
		cwd := other
		if i == n {
			cwd = mine
		}
		s, err := New(dir, cwd, "fake", "m")
		if err != nil {
			tb.Fatal(err)
		}
		if _, err := s.Append(Event{Type: TypeUserMessage, Surface: true, Message: userMsg("please do the thing number " + fmt.Sprint(i))}); err != nil {
			tb.Fatal(err)
		}
		for j := 0; j < events; j++ {
			if _, err := s.Append(Event{Type: TypeToolResult, Output: strings.Repeat("internal/server/handler.go\n", 40)}); err != nil {
				tb.Fatal(err)
			}
		}
	}
}

func userMsg(text string) *llm.Message {
	m := llm.TextMessage(llm.RoleUser, text)
	return &m
}

// A listing walks the sessions directory, which holds every project's
// sessions, not just this one's. The ones that belong elsewhere must cost
// their metadata line and nothing more.
func TestListSkipsOtherProjects(t *testing.T) {
	dir := t.TempDir()
	mine, other := t.TempDir(), t.TempDir()
	fillDir(t, dir, mine, other, 8, 4)

	infos, err := List(dir, mine)
	if err != nil {
		t.Fatal(err)
	}
	if len(infos) != 1 {
		t.Fatalf("listed %d sessions, want only this project's 1", len(infos))
	}
	if infos[0].Meta.Cwd != mine {
		t.Fatalf("listed a session for %s", infos[0].Meta.Cwd)
	}
	if infos[0].Preview == "" {
		t.Fatal("the listing lost the preview")
	}
	if infos[0].Messages == 0 {
		t.Fatal("the listing lost the message count")
	}
	// Every session when no directory is named.
	all, err := List(dir, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 9 {
		t.Fatalf("listed %d of 9 sessions with no cwd filter", len(all))
	}
}

// --continue lands in the newest session of this project that has a
// conversation in it, and falls back to an empty one of this project
// rather than to another project's.
func TestLatestPrefersAConversation(t *testing.T) {
	dir := t.TempDir()
	mine, other := t.TempDir(), t.TempDir()
	withTalk, err := New(dir, mine, "fake", "m")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := withTalk.Append(Event{Type: TypeUserMessage, Surface: true, Message: userMsg("hello")}); err != nil {
		t.Fatal(err)
	}
	// A newer session of this project that never got past its start, and
	// a newer one belonging elsewhere.
	blank, err := New(dir, mine, "fake", "m")
	if err != nil {
		t.Fatal(err)
	}
	if err := blank.EnsureWritten(); err != nil {
		t.Fatal(err)
	}
	elsewhere, err := New(dir, other, "fake", "m")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := elsewhere.Append(Event{Type: TypeUserMessage, Surface: true, Message: userMsg("not yours")}); err != nil {
		t.Fatal(err)
	}
	got, err := Latest(dir, mine)
	if err != nil {
		t.Fatal(err)
	}
	if got != withTalk.Meta.ID {
		t.Fatalf("Latest = %q, want the session with a conversation (%q)", got, withTalk.Meta.ID)
	}

	// With nothing but an empty session, that is still better than
	// nothing at all.
	dir2 := t.TempDir()
	only, err := New(dir2, mine, "fake", "m")
	if err != nil {
		t.Fatal(err)
	}
	if err := only.EnsureWritten(); err != nil {
		t.Fatal(err)
	}
	if got, err := Latest(dir2, mine); err != nil || got != only.Meta.ID {
		t.Fatalf("Latest = %q, %v; want the empty session %q", got, err, only.Meta.ID)
	}
	if got, err := Latest(t.TempDir(), mine); err != nil || got != "" {
		t.Fatalf("Latest on an empty directory = %q, %v", got, err)
	}
}

// BenchmarkListOneOfMany is the shape that matters: a sessions directory
// shared by every project, listing one project's sessions.
func BenchmarkListOneOfMany(b *testing.B) {
	dir := b.TempDir()
	mine, other := b.TempDir(), b.TempDir()
	fillDir(b, dir, mine, other, 60, 20)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := List(dir, mine); err != nil {
			b.Fatal(err)
		}
	}
}
