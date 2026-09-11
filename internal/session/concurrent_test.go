package session

import (
	"sync"
	"testing"
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
