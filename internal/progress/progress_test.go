package progress

import (
	"context"
	"testing"
)

func TestReportReachesWatcher(t *testing.T) {
	var got []string
	ctx := With(context.Background(), func(s string) { got = append(got, s) })
	Report(ctx, "one")
	Report(ctx, "two")
	if len(got) != 2 || got[0] != "one" || got[1] != "two" {
		t.Fatalf("got %v", got)
	}
}

func TestReportWithoutWatcherIsNoop(t *testing.T) {
	Report(context.Background(), "ignored") // must not panic
}
