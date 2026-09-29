package repomap

import (
	"context"
	"testing"
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
