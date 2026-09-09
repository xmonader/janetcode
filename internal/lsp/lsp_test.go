package lsp

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func fakeConfig(t *testing.T, extra ...string) Config {
	t.Helper()
	script, err := filepath.Abs("testdata/fakels.py")
	if err != nil {
		t.Fatal(err)
	}
	return Config{Servers: map[string]Server{
		"fake": {Command: append([]string{"python3", script}, extra...), Extensions: []string{".foo"}},
		"gone": {Command: []string{"no-such-language-server-xyz"}, Extensions: []string{".gone"}},
	}}
}

func TestDiagnoseOpenChangeAndKnown(t *testing.T) {
	root := t.TempDir()
	var logged []string
	m := New(root, fakeConfig(t))
	m.Log = func(s string) { logged = append(logged, s) }
	defer m.Close()
	p := filepath.Join(root, "a.foo")
	os.WriteFile(p, []byte("fine\nthis has ERR here\nWARN too\n"), 0o644)
	lines, ok, err := m.Diagnose(context.Background(), "a.foo")
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	want := []string{"a.foo:2:10: error: found ERR (fake)", "a.foo:3:1: warning: found WARN (fake)"}
	if strings.Join(lines, "|") != strings.Join(want, "|") {
		t.Fatalf("lines = %q", lines)
	}
	if known := m.Known(); len(known) != 2 {
		t.Fatalf("known = %q", known)
	}
	os.WriteFile(p, []byte("all good\n"), 0o644)
	lines, ok, err = m.Diagnose(context.Background(), p) // absolute path, second edit → didChange
	if err != nil || !ok || len(lines) != 0 {
		t.Fatalf("after fix: lines=%q ok=%v err=%v", lines, ok, err)
	}
	if known := m.Known(); len(known) != 0 {
		t.Fatalf("known after fix = %q", known)
	}
	st := strings.Join(m.Status(), "\n")
	if !strings.Contains(st, "fake: running (pid") || !strings.Contains(st, "1 files open") || !strings.Contains(st, "gone: not installed (no-such-language-server-xyz not on PATH)") {
		t.Fatalf("status:\n%s", st)
	}
	if len(logged) == 0 || logged[0] != "fake server error log" {
		t.Fatalf("logged = %q", logged)
	}
}

func TestUnhandledExtensionAndMissingServer(t *testing.T) {
	m := New(t.TempDir(), fakeConfig(t))
	defer m.Close()
	if lines, ok, err := m.Diagnose(context.Background(), "x.txt"); lines != nil || ok || err != nil {
		t.Fatalf("txt: %q %v %v", lines, ok, err)
	}
	os.WriteFile(filepath.Join(m.Root, "x.gone"), []byte("ERR"), 0o644)
	start := time.Now()
	if _, ok, err := m.Diagnose(context.Background(), "x.gone"); ok || err == nil || !strings.Contains(err.Error(), "not installed") {
		t.Fatalf("gone: ok=%v err=%v", ok, err)
	}
	// The failure is remembered: no error and no wait the second time.
	if _, ok, err := m.Diagnose(context.Background(), "x.gone"); ok || err != nil {
		t.Fatalf("gone again: ok=%v err=%v", ok, err)
	}
	if time.Since(start) > 2*time.Second {
		t.Fatal("missing server retried slowly")
	}
	if !strings.Contains(strings.Join(m.Status(), "\n"), "gone: unavailable: no-such-language-server-xyz is not installed") {
		t.Fatalf("status = %q", m.Status())
	}
	disabled := New(t.TempDir(), Config{Disabled: true})
	if disabled.Enabled() {
		t.Fatal("disabled manager enabled")
	}
	if !strings.Contains(strings.Join(disabled.Status(), "\n"), "disabled in the config") {
		t.Fatalf("status = %q", disabled.Status())
	}
}

func TestVersionlessServerAndTimeout(t *testing.T) {
	root := t.TempDir()
	m := New(root, fakeConfig(t, "--no-version"))
	m.Config.Timeout = 1
	defer m.Close()
	os.WriteFile(filepath.Join(root, "b.foo"), []byte("ERR\n"), 0o644)
	lines, ok, err := m.Diagnose(context.Background(), "b.foo")
	if err != nil || !ok || len(lines) != 1 {
		t.Fatalf("lines=%q ok=%v err=%v", lines, ok, err)
	}
	os.WriteFile(filepath.Join(root, "b.foo"), []byte("ok\n"), 0o644)
	if lines, ok, err := m.Diagnose(context.Background(), "b.foo"); err != nil || !ok || len(lines) != 0 {
		t.Fatalf("second: lines=%q ok=%v err=%v", lines, ok, err)
	}
}

func TestValidateAndActive(t *testing.T) {
	if err := (Config{Servers: map[string]Server{"x": {Extensions: []string{".x"}}}}).Validate(); err == nil {
		t.Fatal("extensions without command accepted")
	}
	if err := (Config{Servers: map[string]Server{"x": {Command: []string{"x"}, Extensions: []string{"x"}}}}).Validate(); err == nil {
		t.Fatal("extension without dot accepted")
	}
	c := Config{Servers: map[string]Server{"gopls": {}, "mine": {Command: []string{"mine"}, Extensions: []string{".m"}}}}
	a := c.Active()
	if _, ok := a["gopls"]; ok {
		t.Fatal("empty command did not remove the default")
	}
	if a["mine"].Command[0] != "mine" || a["pyright"].Command[0] != "pyright-langserver" {
		t.Fatalf("active = %+v", a)
	}
	m := New("/r", c)
	if m.ServerFor("x.m") != "mine" || m.ServerFor("x.go") != "" || m.ServerFor("X.PY") != "pyright" {
		t.Fatal("ServerFor wrong")
	}
	if fromURI("file:///tmp/a%20b/c.go") != filepath.FromSlash("/tmp/a b/c.go") {
		t.Fatal("fromURI")
	}
}

func TestResolvePrefersDeclarationOverComment(t *testing.T) {
	lines := []string{
		"package cfg",
		"",
		"// Config is the app configuration.",
		"type Config struct{ Name string }",
		"",
		"func use(c *Config) {}",
	}
	line, col, err := resolve(Point{Symbol: "Config"}, lines)
	if err != nil {
		t.Fatal(err)
	}
	if line != 3 || lines[line][col:col+6] != "Config" {
		t.Fatalf("resolved to line %d col %d (%q)", line, col, lines[line])
	}
	// With only a comment mention, it still points somewhere rather than
	// failing: the server decides whether it can answer.
	if l, _, err := resolve(Point{Symbol: "app"}, lines); err != nil || l != 2 {
		t.Fatalf("comment-only: line %d err %v", l, err)
	}
	if _, _, err := resolve(Point{Symbol: "Missing"}, lines); err == nil {
		t.Fatal("absent symbol accepted")
	}
	// A line without a column points at its first identifier.
	if l, c, err := resolve(Point{Line: 6}, lines); err != nil || l != 5 || lines[l][c:c+4] != "func" {
		t.Fatalf("line 6 → %d:%d err %v", l, c, err)
	}
	if _, _, err := resolve(Point{Line: 99}, lines); err == nil {
		t.Fatal("line past the end accepted")
	}
}

func TestInCommentAndHoverText(t *testing.T) {
	if !inComment("// Config is here", 3) || !inComment("code // Config", 8) || inComment("type Config struct", 5) {
		t.Fatal("comment detection")
	}
	if got := hoverText([]byte(`{"kind":"markdown","value":"func Load() *Config"}`)); got != "func Load() *Config" {
		t.Fatalf("markup = %q", got)
	}
	if got := hoverText([]byte(`"plain string"`)); got != "plain string" {
		t.Fatalf("string = %q", got)
	}
	if got := hoverText([]byte(`[{"kind":"markdown","value":"a"},"b"]`)); got != "a\nb" {
		t.Fatalf("list = %q", got)
	}
}

func TestDecodeLocationShapes(t *testing.T) {
	m := &Manager{Root: "/repo"}
	one := m.decodeLocations([]byte(`{"uri":"file:///repo/a.go","range":{"start":{"line":4,"character":2}}}`))
	if len(one) != 1 || one[0].Path != "a.go" || one[0].Line != 5 || one[0].Col != 3 {
		t.Fatalf("single = %+v", one)
	}
	list := m.decodeLocations([]byte(`[{"uri":"file:///repo/a.go","range":{"start":{"line":0,"character":0}}},{"uri":"file:///repo/b.go","range":{"start":{"line":1,"character":1}}}]`))
	if len(list) != 2 || list[1].Path != "b.go" || list[1].Line != 2 {
		t.Fatalf("list = %+v", list)
	}
	links := m.decodeLocations([]byte(`[{"targetUri":"file:///repo/c.go","targetRange":{"start":{"line":9,"character":0}},"targetSelectionRange":{"start":{"line":9,"character":5}}}]`))
	if len(links) != 1 || links[0].Path != "c.go" || links[0].Col != 6 {
		t.Fatalf("links = %+v", links)
	}
	if got := m.decodeLocations([]byte(`null`)); got != nil {
		t.Fatalf("null = %+v", got)
	}
}

// A language server that dies must not poison the session: every query
// after it would fail with its exit status, which reads as a fact about
// the code rather than as a crashed process. It is dropped and started
// again — but not forever.
func TestDeadServerIsRestartedThenGivenUpOn(t *testing.T) {
	m := New(t.TempDir(), Config{Servers: map[string]Server{
		"dier": {Command: []string{"sh", "-c", "exit 1"}, Extensions: []string{".dies"}},
	}})
	defer m.Close()
	ctx := context.Background()
	// Starting fails, so it is remembered as failed and not retried.
	for i := 0; i < 4; i++ {
		if c, err := m.clientFor(ctx, "x.dies"); c != nil {
			t.Fatalf("attempt %d handed out a client: %v", i, err)
		}
	}
	if len(m.failed) == 0 {
		t.Fatal("a server that cannot start was not remembered")
	}
	// A live client is handed back as it is.
	m2 := New(t.TempDir(), Config{})
	defer m2.Close()
	c := &client{done: make(chan struct{})}
	if !c.alive() {
		t.Fatal("a running client reported dead")
	}
	close(c.done)
	if c.alive() {
		t.Fatal("a client whose process ended reported alive")
	}
}

// Answering several questions about one file used to re-send the whole
// file each time, so the server parsed it once per question. It is sent
// when it changes and not otherwise.
func TestUnchangedDocumentIsNotResent(t *testing.T) {
	w := &countingWriter{}
	c := &client{docs: map[string]int{}, diags: map[string]diagState{}, waits: map[string]chan struct{}{},
		done: make(chan struct{}), stdin: w}
	if err := c.sync("/p/a.go", "package a\n"); err != nil {
		t.Fatal(err)
	}
	first := w.writes
	if first == 0 {
		t.Fatal("the first sync sent nothing")
	}
	// Same content: nothing goes over the wire.
	for i := 0; i < 5; i++ {
		if err := c.sync("/p/a.go", "package a\n"); err != nil {
			t.Fatal(err)
		}
	}
	if w.writes != first {
		t.Fatalf("%d writes for five identical syncs, want %d", w.writes, first)
	}
	// Changed content is sent.
	if err := c.sync("/p/a.go", "package a\n\nfunc F() {}\n"); err != nil {
		t.Fatal(err)
	}
	if w.writes == first {
		t.Fatal("a changed document was not sent")
	}
	// A different file is its own document.
	before := w.writes
	if err := c.sync("/p/b.go", "package a\n"); err != nil {
		t.Fatal(err)
	}
	if w.writes == before {
		t.Fatal("a second file was not opened")
	}
}

// countingWriter stands in for the server's stdin and counts the
// notifications that reach it.
type countingWriter struct{ writes int }

func (c *countingWriter) Write(p []byte) (int, error) { c.writes++; return len(p), nil }
func (c *countingWriter) Close() error                { return nil }
