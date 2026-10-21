package agent

import (
	"context"
	"encoding/json"
	"sync"
	"testing"

	"github.com/xmonader/janetcode/internal/perm"
	"github.com/xmonader/janetcode/internal/tool"
)

// /cd moves the parent's root, system prompt and permission ledger from a
// front-end goroutine while a turn may be starting a subagent. Building
// the child read all three off the fields with no lock, which -race
// reports; it must read them through the parent's accessors.
func TestSubagentBuildRacesNothingWithCd(t *testing.T) {
	dir := t.TempDir()
	parent := &Agent{Provider: &fake{}, Model: "m", System: "sys", AutoApprove: true}
	task := &Task{Parent: parent, Root: dir}
	parent.Tools = tool.NewRegistry(tool.ListDir{Root: dir}, task)

	var wg sync.WaitGroup
	stop := make(chan struct{})
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			parent.SetSystem("sys moved")
			parent.SetLedger(&perm.Ledger{})
			task.SetRoot(dir)
		}
	}()
	for i := 0; i < 20; i++ {
		if _, err := task.child(nil, nil); err != nil {
			t.Fatal(err)
		}
		if _, err := task.forkChild(); err != nil {
			t.Fatal(err)
		}
		// Not a git repository, so the editing job is refused — after it
		// has read the root it would have checked out.
		_, _ = task.Run(context.Background(), json.RawMessage(`{"description":"x","prompt":"edit it","edit":true}`))
	}
	close(stop)
	wg.Wait()
}
