package tui

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/xmonader/janetcode/internal/inspect"
)

// /inspect ran its report on the UI goroutine with a minute's timeout, so
// a slow subject (a network check, a big session log) froze the whole
// interface — no typing, no redraw, not even ctrl+c — until it answered.
func TestInspectDoesNotFreezeTheInterface(t *testing.T) {
	opts := fakeOptions(t)
	release := make(chan struct{})
	reg := inspect.New()
	reg.Add(inspect.Subject{Name: "slow", Summary: "takes its time", Report: func(ctx context.Context, _ string) (string, error) {
		select {
		case <-release:
		case <-ctx.Done():
		}
		return "slow report done", nil
	}})
	opts.Inspect = reg
	h := newHarnessFor(t, opts)
	defer close(release)

	h.keys("/inspect slow")
	returned := make(chan struct{})
	go func() {
		h.enter()
		close(returned)
	}()
	select {
	case <-returned:
	case <-time.After(3 * time.Second):
		t.Fatal("the interface is blocked on /inspect")
	}
	release <- struct{}{}
	h.pump(func() bool { return strings.Contains(plain(h.m.View()), "slow report done") })
}
