package session

import (
	"strings"
	"sync"
	"testing"
	"unicode/utf8"

	"github.com/xmonader/janetcode/internal/llm"
)

// The agent appends from parallel tool goroutines while the UI goroutine
// names the session, saves pins and rates a message. Everything that
// touches Meta or the event slice has to do it under the mutex.
func TestConcurrentMetadataAndAppend(t *testing.T) {
	dir := t.TempDir()
	s, err := New(dir, "/work", "p", "m")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Append(Event{Type: TypeAssistant}); err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for n := 0; n < 25; n++ {
				if _, err := s.Append(Event{Type: TypeAssistant}); err != nil {
					t.Error(err)
					return
				}
			}
		}()
	}
	wg.Add(3)
	go func() {
		defer wg.Done()
		for n := 0; n < 25; n++ {
			if err := s.SetTitle("a title"); err != nil {
				t.Error(err)
				return
			}
		}
	}()
	go func() {
		defer wg.Done()
		for n := 0; n < 25; n++ {
			if err := s.SavePins([]string{"one", "two"}); err != nil {
				t.Error(err)
				return
			}
		}
	}()
	go func() {
		defer wg.Done()
		for n := 0; n < 25; n++ {
			if err := s.Rate(0, RateUp, "good"); err != nil {
				t.Error(err)
				return
			}
		}
	}()
	wg.Wait()

	if s.Meta.Title != "a title" || len(s.Meta.Pins) != 2 {
		t.Fatalf("metadata lost: %+v", s.Meta)
	}
}

// The match offset comes from a lowercased copy of the message, and
// lowercasing is not length-preserving: Ⱥ and Ⱦ each gain a byte. A
// conversation containing one therefore put the offset past the end of
// the original and /history panicked on it.
func TestSearchSnippetSurvivesNonASCII(t *testing.T) {
	dir := t.TempDir()
	s, err := New(dir, "/work", "p", "m")
	if err != nil {
		t.Fatal(err)
	}
	msg := llm.TextMessage(llm.RoleUser, strings.Repeat("Ⱥ", 40)+" needle here")
	if _, err := s.Append(Event{Type: TypeUserMessage, Surface: true, Message: &msg}); err != nil {
		t.Fatal(err)
	}
	hits, err := Search(dir, "/work", "needle", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 {
		t.Fatalf("hits = %+v", hits)
	}
	if !utf8.ValidString(hits[0].Snippet) {
		t.Fatalf("snippet is not valid UTF-8: %q", hits[0].Snippet)
	}
}
