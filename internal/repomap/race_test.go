package repomap

import (
	"context"
	"encoding/json"
	"sync"
	"testing"

	"github.com/xmonader/janetcode/internal/llm"
)

// /cd calls SetRoot from the UI while a turn's map or a repo_map call may
// be running. Both read the cache's root for the changed-files list, and
// did so without the lock SetRoot takes — a data race under -race.
func TestSetRootDoesNotRaceWithTheToolOrTurnPrep(t *testing.T) {
	a, b := fixture(t), fixture(t)
	c := &Cache{Root: a}
	tl := Tool{Cache: c}
	prep := TurnPrep(c, 0)
	stop := make(chan struct{})
	flipped := make(chan struct{})
	go func() {
		defer close(flipped)
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			if i%2 == 0 {
				c.SetRoot(b)
			} else {
				c.SetRoot(a)
			}
		}
	}()
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < 5; i++ {
			_, _ = tl.Run(context.Background(), json.RawMessage(`{"query":"handler"}`))
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 5; i++ {
			prep(context.Background(), []llm.Block{{Type: llm.BlockText, Text: "change the Store"}})
		}
	}()
	wg.Wait()
	close(stop)
	<-flipped
}
