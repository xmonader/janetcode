package repomap

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xmonader/janetcode/internal/llm"
)

// The fallback walk honours the context: TurnPrep bounds the build to
// twenty seconds, and a tree outside version control (a home directory,
// say) used to be walked to the end regardless.
func TestBuildStopsOnACancelledContext(t *testing.T) {
	root := fixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Build(ctx, root, nil); err == nil {
		t.Fatal("Build walked the whole tree for a cancelled caller")
	}
}

// A tree too big to index within TurnPrep's deadline is not started on
// again every turn. With the build cut short and nothing cached, each
// turn waited out the whole twenty seconds and still got no map; before
// the build honoured the deadline it paid once and the map stayed.
func TestTurnPrepDoesNotRetryABuildThatRanOutOfTime(t *testing.T) {
	root := fixture(t)
	// Build parses on several goroutines at once.
	var parsed atomic.Int64
	c := &Cache{Root: root, Parser: func(string, []byte) ([]Symbol, []string, bool) {
		parsed.Add(1)
		return nil, nil, false
	}}
	prep := TurnPrep(c, 0)
	past, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	prep(past, []llm.Block{{Type: llm.BlockText, Text: "change the Store"}})
	parsed.Store(0)
	start := time.Now()
	prep(context.Background(), []llm.Block{{Type: llm.BlockText, Text: "change the Store"}})
	if n := parsed.Load(); n != 0 {
		t.Fatalf("the next turn built the index again (%d files parsed) for a tree that had already run out of time", n)
	}
	if time.Since(start) > 5*time.Second {
		t.Fatal("the next turn waited on the build")
	}
	// The repo_map tool, which has no such deadline, still builds it.
	if _, err := c.Get(context.Background()); err != nil || parsed.Load() == 0 {
		t.Fatalf("Get: %v, parsed %d", err, parsed.Load())
	}
}
