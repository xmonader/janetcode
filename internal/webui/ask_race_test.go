package webui

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/xmonader/janetcode/internal/agent"
)

// The open questions are the server's, changed and copied under its lock.
// AskTimedOut marks them proceeded in place, and /api/state and the event
// log used to hand out that same slice to be serialised outside the lock:
// a page loading while a question timed out read it as it was written.
// Run with -race.
func TestTimedOutQuestionsAreNotSharedWithReaders(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := newServer(ctx, Options{})
	qs := []agent.Question{{Prompt: "Which port?", Options: []agent.AskOption{{Label: "8080"}, {Label: "9000"}}, Recommended: "9000"}}
	go func() { _, _ = s.Ask(ctx, qs) }()
	deadline := time.Now().Add(3 * time.Second)
	for {
		s.mu.Lock()
		up := s.asking != nil
		s.mu.Unlock()
		if up {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the question never came up")
		}
		time.Sleep(time.Millisecond)
	}
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for range 200 {
			s.AskTimedOut(qs, nil)
		}
	}()
	go func() {
		defer wg.Done()
		for range 200 {
			_, _, _, open, _ := s.snapshot()
			_, _ = json.Marshal(open)
			s.mu.Lock()
			log := append([]envelope(nil), s.log...)
			s.mu.Unlock()
			for _, e := range log {
				_, _ = json.Marshal(e)
			}
		}
	}()
	wg.Wait()
}
