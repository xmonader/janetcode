package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/session"
	"github.com/xmonader/janetcode/internal/tool"
)

// TestFrontEndCallsDuringATurn is a race-detector test: what a front-end
// does from its own goroutine while a turn runs tools — /accept
// lock/unlock/status, /btw's history copy, /agents' transcript view, the
// status bar's usage, /config route_cheap, a memory refresh — must not
// share unguarded state with the turn goroutine.
func TestFrontEndCallsDuringATurn(t *testing.T) {
	var responses []*llm.Response
	for i := 0; i < 20; i++ {
		responses = append(responses, toolCall(fmt.Sprintf("t%d", i), "write_file", fmt.Sprintf(`{"path":"impl.go","content":"package foo // %d\n"}`, i)))
	}
	responses = append(responses, text("done"))
	a, root := newAgent(t, &fake{responses: responses}, nil)
	a.AutoApprove = true
	a.Tools = tool.NewRegistry(tool.WriteFile{Root: root})
	a.SystemExtra = func() string { return "extra" }
	testPath := filepath.Join(root, "foo_test.go")
	if err := os.WriteFile(testPath, []byte("package foo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	sum, _ := hashFile(testPath)
	a.Accept = &session.AcceptMeta{
		Phase: session.AcceptLocked, Request: "add foo", Root: root,
		Files: []string{testPath}, Hashes: map[string]string{testPath: sum}, Command: "true",
	}
	// The session keeps what it is handed and serialises it later, from
	// whichever goroutine saves next.
	var kept *session.AcceptMeta
	var keptMu sync.Mutex
	a.OnAcceptChange = func(am *session.AcceptMeta) {
		keptMu.Lock()
		kept = am
		keptMu.Unlock()
	}

	done := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; ; i++ {
			select {
			case <-done:
				return
			default:
			}
			if i%2 == 0 {
				_ = a.AcceptUnlock()
			} else {
				_ = a.AcceptLock()
			}
			_ = a.AcceptStatus()
			_ = a.MessagesSnapshot()
			_ = a.UsageSnapshot()
			a.RefreshExtra()
			a.SetRouteCheap("")
			keptMu.Lock()
			_, _ = json.Marshal(kept)
			keptMu.Unlock()
		}
	}()
	err := a.Run(context.Background(), "implement foo", func(Event) {})
	close(done)
	wg.Wait()
	if err != nil {
		t.Fatal(err)
	}
}
