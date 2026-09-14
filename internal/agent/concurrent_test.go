package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/xmonader/janetcode/internal/config"
	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/perm"
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

// TestSettingsChangesDuringATurn is a race-detector test for the settings
// a front-end changes while a turn runs: the mode toggles (shift+tab,
// /yolo, /auto, /edits, /readonly, /plan), the keys /config writes through
// ApplyConfig, /pin and /unpin, and /permissions adding a rule — while a
// subagent is built from the parent's settings on a goroutine of its own.
func TestSettingsChangesDuringATurn(t *testing.T) {
	var responses []*llm.Response
	for i := 0; i < 20; i++ {
		responses = append(responses, toolCall(fmt.Sprintf("t%d", i), "write_file", fmt.Sprintf(`{"path":"impl.go","content":"package foo // %d\n"}`, i)))
	}
	responses = append(responses, text("done"))
	a, root := newAgent(t, &fake{responses: responses}, ApproverFunc(func(context.Context, ToolCall) (Decision, error) { return Allow, nil }))
	a.AutoApprove = true
	a.Tools = tool.NewRegistry(tool.ReadFile{Root: root}, tool.WriteFile{Root: root})
	cfg := &config.Config{}
	task := &Task{Parent: a}

	done := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; ; i++ {
			select {
			case <-done:
				return
			default:
			}
			on := i%2 == 0
			a.UpdateSettings(func(s *Settings) {
				s.AutoApprove, s.AutoMode, s.AutoEdits, s.ReadOnly = !on, on, on, on
			})
			a.SetPlanMode(on)
			_ = a.Settings()
			for key, val := range map[string]string{
				"model": fmt.Sprintf("m%d", i%3), "max_tokens": fmt.Sprint(1000 + i%2),
				"compact_at": fmt.Sprint(-1 + i%2), "max_iterations": fmt.Sprint(100 + i%2),
				"intent": fmt.Sprint(on), "proof_notes": fmt.Sprint(on),
			} {
				if _, err := ApplyConfig(cfg, a, "", key, val); err != nil {
					t.Error(err)
					return
				}
			}
			a.Pin(fmt.Sprintf("note %d", i))
			a.Unpin(1)
			_ = a.PinsSnapshot()
			a.AddRule(perm.Allowed, perm.Rule{Tool: "bash", Pattern: fmt.Sprintf("echo %d", i%5)})
			_ = a.RulesSnapshot()
		}
	}()
	go func() {
		defer wg.Done()
		for {
			select {
			case <-done:
				return
			default:
			}
			if _, err := task.child(nil, nil); err != nil {
				t.Error(err)
				return
			}
			if _, err := task.forkChild(); err != nil {
				t.Error(err)
				return
			}
		}
	}()
	err := a.Run(context.Background(), "implement foo", func(Event) {})
	close(done)
	wg.Wait()
	if err != nil {
		t.Fatal(err)
	}
}
