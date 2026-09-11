package llm

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestWatchdogIdleFires(t *testing.T) {
	ctx, wd := Watch(context.Background(), StreamLimits{Idle: 40 * time.Millisecond, Max: -1})
	defer wd.Stop()
	select {
	case <-ctx.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("idle timeout never fired")
	}
	err := wd.Wrap(ctx.Err())
	if !errors.Is(err, ErrStreamWatchdog) {
		t.Fatalf("err = %v, want a watchdog error", err)
	}
	var st *StreamStalled
	if !errors.As(err, &st) || st.Lifetime {
		t.Fatalf("want an idle stall, got %#v", err)
	}
	if !strings.Contains(err.Error(), "stream_idle_timeout") {
		t.Fatalf("error should name the setting: %v", err)
	}
}

func TestWatchdogTickHoldsIdleOff(t *testing.T) {
	ctx, wd := Watch(context.Background(), StreamLimits{Idle: 80 * time.Millisecond, Max: -1})
	defer wd.Stop()
	deadline := time.Now().Add(250 * time.Millisecond)
	for time.Now().Before(deadline) {
		wd.Tick()
		time.Sleep(10 * time.Millisecond)
	}
	if ctx.Err() != nil {
		t.Fatalf("a stream that keeps producing was cut off: %v", wd.Err())
	}
}

func TestWatchdogLifetimeCapIsNotReset(t *testing.T) {
	ctx, wd := Watch(context.Background(), StreamLimits{Idle: -1, Max: 60 * time.Millisecond})
	defer wd.Stop()
	go func() {
		for i := 0; i < 100; i++ {
			wd.Tick()
			time.Sleep(5 * time.Millisecond)
		}
	}()
	select {
	case <-ctx.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("lifetime cap never fired")
	}
	var st *StreamStalled
	if err := wd.Wrap(ctx.Err()); !errors.As(err, &st) || !st.Lifetime {
		t.Fatalf("want a lifetime stall, got %v", err)
	}
}

func TestWatchdogWrapKeepsRealErrors(t *testing.T) {
	_, wd := Watch(context.Background(), StreamLimits{Idle: time.Hour, Max: time.Hour})
	defer wd.Stop()
	real := errors.New("http 500")
	if got := wd.Wrap(real); got != real {
		t.Fatalf("Wrap(%v) = %v, want it unchanged", real, got)
	}
	if wd.Wrap(nil) != nil {
		t.Fatal("Wrap(nil) must stay nil")
	}
}

func TestStreamLimitsResolve(t *testing.T) {
	got := StreamLimits{}.Resolve()
	if got.Idle != DefaultStreamIdle || got.Max != DefaultStreamMax {
		t.Fatalf("zero limits = %+v, want the defaults", got)
	}
	if off := (StreamLimits{Idle: -1, Max: -1}).Resolve(); off.Idle != 0 || off.Max != 0 {
		t.Fatalf("negative limits = %+v, want disabled", off)
	}
}

func TestNilWatchdogIsUsable(t *testing.T) {
	var wd *Watchdog
	wd.Tick()
	wd.Stop()
	if wd.Err() != nil {
		t.Fatal("a nil watchdog has no error")
	}
}
