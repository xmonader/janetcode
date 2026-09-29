package repomap

import (
	"context"
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
	parsed := 0
	c := &Cache{Root: root, Parser: func(string, []byte) ([]Symbol, []string, bool) {
		parsed++
		return nil, nil, false
	}}
	prep := TurnPrep(c, 0)
	past, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	prep(past, []llm.Block{{Type: llm.BlockText, Text: "change the Store"}})
	parsed = 0
	start := time.Now()
	prep(context.Background(), []llm.Block{{Type: llm.BlockText, Text: "change the Store"}})
	if parsed != 0 {
		t.Fatalf("the next turn built the index again (%d files parsed) for a tree that had already run out of time", parsed)
	}
	if time.Since(start) > 5*time.Second {
		t.Fatal("the next turn waited on the build")
	}
	// The repo_map tool, which has no such deadline, still builds it.
	if _, err := c.Get(context.Background()); err != nil || parsed == 0 {
		t.Fatalf("Get: %v, parsed %d", err, parsed)
	}
}
