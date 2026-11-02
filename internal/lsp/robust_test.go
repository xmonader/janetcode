package lsp

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeFiles(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for n, c := range files {
		if err := os.WriteFile(filepath.Join(root, n), []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	for i := 0; i < 100; i++ {
		if ok() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func readLog(path string) string {
	b, _ := os.ReadFile(path)
	return string(b)
}

func TestGlobRegexp(t *testing.T) {
	for _, c := range []struct {
		glob, path string
		want       bool
	}{
		{"**/*.go", "a/b/c.go", true},
		{"**/*.go", "c.go", true},
		{"**/*.go", "c.rs", false},
		{"*.go", "a/c.go", false},
		{"**/*.{go,mod}", "x/go.mod", true},
		{"**/*.{go,mod}", "x/go.sum", false},
		{"src/**", "src/a/b", true},
		{"a?c", "abc", true},
		{"a?c", "a/c", false},
		{"[!a]x", "bx", true},
		{"[!a]x", "ax", false},
		{"a.b", "aXb", false},
	} {
		re, err := globRegexp(c.glob)
		if err != nil {
			t.Fatalf("%s: %v", c.glob, err)
		}
		if got := re.MatchString(c.path); got != c.want {
			t.Errorf("%q ~ %q = %v, want %v", c.glob, c.path, got, c.want)
		}
	}
}

func TestPullDiagnostics(t *testing.T) {
	root := t.TempDir()
	m := New(root, fakeConfig(t, "--pull"))
	defer m.Close()
	writeFiles(t, root, map[string]string{"a.foo": "ok\nERR here\n"})
	lines, ok, err := m.Diagnose(context.Background(), "a.foo")
	if err != nil || !ok || len(lines) != 1 || !strings.Contains(lines[0], "a.foo:2:1: error: found ERR") {
		t.Fatalf("pull: %v %v %v", lines, ok, err)
	}
	writeFiles(t, root, map[string]string{"a.foo": "clean\n"})
	lines, ok, err = m.Diagnose(context.Background(), "a.foo")
	if err != nil || !ok || len(lines) != 0 {
		t.Fatalf("after fix: %v %v %v", lines, ok, err)
	}
}

func TestWatchedFilesAreForwardedToMatchingWatchers(t *testing.T) {
	root := t.TempDir()
	logf := filepath.Join(root, "log.txt")
	m := New(root, fakeConfig(t, "--watch", "--log="+logf))
	defer m.Close()
	writeFiles(t, root, map[string]string{"a.foo": "ok\n"})
	if _, _, err := m.Diagnose(context.Background(), "a.foo"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "registration", func() bool {
		m.mu.Lock()
		c := m.clients["fake"]
		m.mu.Unlock()
		c.watchM.Lock()
		defer c.watchM.Unlock()
		return c.hasWatchers()
	})
	m.NotifyChanged(FileEvent{"b.foo", Created}, FileEvent{"notes.txt", Changed}, FileEvent{filepath.Join(root, "sub", "c.foo"), Deleted})
	waitFor(t, "watched event", func() bool { return strings.Count(readLog(logf), "watched ") >= 2 })
	got := readLog(logf)
	if !strings.Contains(got, "watched 1 file://"+filepath.ToSlash(root)+"/b.foo") || !strings.Contains(got, "watched 3 ") {
		t.Errorf("log:\n%s", got)
	}
	if strings.Contains(got, "notes.txt") {
		t.Errorf("an unwatched file was sent:\n%s", got)
	}
}

func TestEventsWaitForTheFirstRegistration(t *testing.T) {
	c := &client{}
	c.watched([]FileEvent{{"/p/a.foo", Changed}})
	if len(c.queued) != 1 {
		t.Fatalf("queued = %d", len(c.queued))
	}
	for i := 0; i < maxQueuedEvents+10; i++ {
		c.watched([]FileEvent{{"/p/a.foo", Changed}})
	}
	if len(c.queued) != maxQueuedEvents {
		t.Fatalf("queue not bounded: %d", len(c.queued))
	}
}

func TestCrashedServerRestartsWithBackoffAndReplay(t *testing.T) {
	root := t.TempDir()
	logf := filepath.Join(root, "log.txt")
	m := New(root, fakeConfig(t, "--log="+logf))
	m.RestartBackoff = 300 * time.Millisecond
	defer m.Close()
	writeFiles(t, root, map[string]string{"a.foo": "ok\n", "b.foo": "ERR\n"})
	ctx := context.Background()
	for _, f := range []string{"a.foo", "b.foo"} {
		if _, ok, err := m.Diagnose(ctx, f); err != nil || !ok {
			t.Fatalf("%s: %v %v", f, ok, err)
		}
	}
	m.mu.Lock()
	first := m.clients["fake"]
	m.mu.Unlock()
	_ = first.cmd.Process.Kill()
	<-first.done
	// Inside the backoff the query goes without a server instead of into
	// a crash loop.
	lines, ok, err := m.Diagnose(ctx, "a.foo")
	if err != nil || ok || lines != nil {
		t.Fatalf("during backoff: %v %v %v", lines, ok, err)
	}
	time.Sleep(350 * time.Millisecond)
	if _, ok, err := m.Diagnose(ctx, "a.foo"); err != nil || !ok {
		t.Fatalf("after backoff: %v %v", ok, err)
	}
	// The other open file is re-sent to the new server without anyone
	// asking about it.
	waitFor(t, "replay", func() bool { return strings.Count(readLog(logf), "open ") >= 4 })
	if m.restarts["fake"] != 1 {
		t.Errorf("restarts = %d", m.restarts["fake"])
	}
}

func TestBackoffDoubles(t *testing.T) {
	m := New(t.TempDir(), Config{})
	m.RestartBackoff = time.Second
	for n, want := range map[int]time.Duration{1: time.Second, 2: 2 * time.Second, 3: 4 * time.Second} {
		if got := m.backoff(n); got != want {
			t.Errorf("backoff(%d) = %v", n, got)
		}
	}
}

// A server that stops answering must cost a bounded wait per edit and
// then be restarted, not hold every turn until it is killed by hand.
func TestHungServerIsRestarted(t *testing.T) {
	root := t.TempDir()
	cfg := fakeConfig(t, "--hang")
	cfg.Timeout = 1
	m := New(root, cfg)
	m.RestartBackoff = 10 * time.Millisecond
	defer m.Close()
	writeFiles(t, root, map[string]string{"a.foo": "ok\n"})
	ctx := context.Background()
	for i := 0; i < maxStalls; i++ {
		start := time.Now()
		_, ok, err := m.Diagnose(ctx, "a.foo")
		if err != nil || ok {
			t.Fatalf("round %d: %v %v", i, ok, err)
		}
		if time.Since(start) > 4*time.Second {
			t.Fatalf("round %d took %v", i, time.Since(start))
		}
	}
	m.mu.Lock()
	first := m.clients["fake"]
	m.mu.Unlock()
	if !first.hung() {
		t.Fatal("server not marked hung")
	}
	if st := strings.Join(m.Status(), "\n"); !strings.Contains(st, "not answering") {
		t.Errorf("status: %s", st)
	}
	time.Sleep(50 * time.Millisecond)
	if _, _, err := m.Diagnose(ctx, "a.foo"); err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	second := m.clients["fake"]
	m.mu.Unlock()
	if second == first || m.restarts["fake"] != 1 {
		t.Errorf("not restarted: same=%v restarts=%d", second == first, m.restarts["fake"])
	}
	select {
	case <-first.reaped:
	case <-time.After(5 * time.Second):
		t.Error("the hung server was left running")
	}
}

func TestCallHasABackstopTimeoutAndCancels(t *testing.T) {
	root := t.TempDir()
	logf := filepath.Join(root, "log.txt")
	m := New(root, fakeConfig(t, "--hang", "--log="+logf))
	defer m.Close()
	writeFiles(t, root, map[string]string{"a.foo": "ok\n"})
	c, err := m.clientFor(context.Background(), "a.foo")
	if err != nil || c == nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	if _, err := c.call(ctx, "textDocument/hover", map[string]any{}); err == nil {
		t.Fatal("hung call returned")
	}
	if c.pending() != 0 {
		t.Error("a timed-out request is still tracked as pending")
	}
	waitFor(t, "cancel", func() bool { return strings.Contains(readLog(logf), "$/cancelRequest") })
	if c.hung() {
		t.Error("one timeout is not a hang")
	}
}
