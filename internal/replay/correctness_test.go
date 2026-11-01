package replay

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/xmonader/janetcode/internal/llm"
)

func TestReadRejectsMalformedNonFinalRecord(t *testing.T) {
	rec := recording + `{"type":"event","t":"assistant/message"` + "\n" +
		`{"type":"event","t":"assistant/message","message":{"role":"assistant","blocks":[{"type":"text","text":"after corruption"}]}}` + "\n"
	if _, err := Read(strings.NewReader(rec)); err == nil {
		t.Fatal("Read ignored a malformed record between valid records")
	}
}

func TestStreamHonoursCancellationDuringEmit(t *testing.T) {
	p, err := Read(strings.NewReader(recording))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	events := 0
	resp, err := p.Stream(ctx, llm.Request{}, func(llm.Event) {
		events++
		cancel()
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Stream returned response %+v, error %v after cancellation", resp, err)
	}
	if events != 1 {
		t.Fatalf("Stream emitted %d events after the first one cancelled the context", events)
	}
}

func TestConcurrentStreamHandsOutEachResponseOnce(t *testing.T) {
	const calls = 32
	p := &Provider{}
	for i := 0; i < calls; i++ {
		p.responses = append(p.responses, &llm.Response{
			Message:    llm.TextMessage(llm.RoleAssistant, fmt.Sprintf("response-%d", i)),
			StopReason: llm.StopEndTurn,
		})
	}

	var wg sync.WaitGroup
	texts := make(chan string, calls)
	errs := make(chan error, calls)
	for i := 0; i < calls; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, err := p.Stream(context.Background(), llm.Request{}, nil)
			if err != nil {
				errs <- err
				return
			}
			texts <- r.Message.Text()
		}()
	}
	wg.Wait()
	close(texts)
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	seen := map[string]bool{}
	for text := range texts {
		if seen[text] {
			t.Errorf("response %q was handed out twice", text)
		}
		seen[text] = true
	}
	if len(seen) != calls {
		t.Fatalf("received %d distinct responses, want %d", len(seen), calls)
	}
}
