package opref

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xmonader/janetcode/internal/secret"
)

func fakeOp(t *testing.T, script string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell script stand-in for op")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "op"), []byte("#!/bin/sh\n"+script+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	Forget()
	t.Cleanup(func() { Forget(); secret.Forget() })
}

func TestIsRef(t *testing.T) {
	for in, want := range map[string]bool{
		"op://vault/item/field": true, " op://v/i/f ": true,
		"op://": false, "sk-abc": false, "": false, "https://op://x": false,
	} {
		if got := IsRef(in); got != want {
			t.Errorf("IsRef(%q) = %v", in, got)
		}
	}
}

func TestResolveRunsOpReadAndTrimsTheNewline(t *testing.T) {
	fakeOp(t, `[ "$1" = read ] && [ "$2" = "op://v/i/f" ] && printf 'the-value\n'`)
	got, err := Resolve("op://v/i/f")
	if err != nil || got != "the-value" {
		t.Fatalf("got %q, %v", got, err)
	}
	// Resolved values are registered so output carrying one is redacted.
	if out, n := secret.Redact("x the-value y"); n != 1 || strings.Contains(out, "the-value") {
		t.Fatalf("not registered for redaction: %q", out)
	}
}

func TestResolveLeavesLiteralsAlone(t *testing.T) {
	fakeOp(t, `exit 9`)
	got, err := Resolve("sk-literal")
	if err != nil || got != "sk-literal" {
		t.Fatalf("got %q, %v", got, err)
	}
}

func TestResolveMissingOpNamesTheReference(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	Forget()
	_, err := Resolve("op://v/i/f")
	if err == nil || !strings.Contains(err.Error(), "op://v/i/f") || !strings.Contains(err.Error(), "not installed") {
		t.Fatalf("err = %v", err)
	}
}

func TestResolveFailureCarriesOpsMessageAndTheReference(t *testing.T) {
	fakeOp(t, `echo '[ERROR] item not found' >&2; exit 1`)
	_, err := Resolve("op://v/i/f")
	if err == nil || !strings.Contains(err.Error(), "op://v/i/f") || !strings.Contains(err.Error(), "item not found") {
		t.Fatalf("err = %v", err)
	}
}

func TestResolveEmptyValueIsAnError(t *testing.T) {
	fakeOp(t, `printf '\n'`)
	if _, err := Resolve("op://v/i/f"); err == nil || !strings.Contains(err.Error(), "empty") {
		t.Fatalf("err = %v", err)
	}
}

func TestResolveIsCachedUntilTheTTL(t *testing.T) {
	var calls atomic.Int32
	old, oldNow := read, now
	t.Cleanup(func() { read, now = old, oldNow; Forget(); secret.Forget() })
	Forget()
	clock := time.Unix(1000, 0)
	now = func() time.Time { return clock }
	read = func(string) (string, error) {
		calls.Add(1)
		return "value-" + string(rune('0'+calls.Load())) + "\n", nil
	}
	a, _ := Resolve("op://v/i/f")
	b, _ := Resolve("op://v/i/f")
	if a != b || calls.Load() != 1 {
		t.Fatalf("a=%q b=%q calls=%d", a, b, calls.Load())
	}
	clock = clock.Add(ttl + time.Second)
	c, _ := Resolve("op://v/i/f")
	if c == a || calls.Load() != 2 {
		t.Fatalf("after the ttl: c=%q calls=%d", c, calls.Load())
	}
}

func TestResolveAllStopsAtTheFirstFailure(t *testing.T) {
	fakeOp(t, `[ "$2" = "op://v/i/ok" ] && printf 'good\n' || { echo nope >&2; exit 1; }`)
	got, err := ResolveAll([]string{"lit", "op://v/i/ok"})
	if err != nil || got[0] != "lit" || got[1] != "good" {
		t.Fatalf("got %v, %v", got, err)
	}
	if _, err := ResolveAll([]string{"op://v/i/ok", "op://v/i/bad"}); err == nil || !strings.Contains(err.Error(), "op://v/i/bad") {
		t.Fatalf("err = %v", err)
	}
}
