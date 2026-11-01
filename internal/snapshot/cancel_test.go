package snapshot

import (
	"context"
	"errors"
	"testing"
)

func TestOpenReportsCancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := Open(ctx, t.TempDir())
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Open with a cancelled context returned %v, want context.Canceled", err)
	}
}
