package tui

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/xmonader/janetcode/internal/mcp"
	"github.com/xmonader/janetcode/internal/mcp/mcptool"
	"github.com/xmonader/janetcode/internal/trust"
)

// withMCP gives the harness a manager, an empty project and a trust store.
func withMCP(t *testing.T, h *harness) *mcptool.Manager {
	t.Helper()
	mgr := &mcptool.Manager{Options: mcp.Options{Root: h.m.opts.Cwd, HandshakeTimeout: 20 * time.Second}, Registry: h.m.opts.Agent.Tools}
	t.Cleanup(mgr.Close)
	h.m.opts.MCP = &MCPOptions{Manager: mgr, Trust: trust.Store{Path: filepath.Join(t.TempDir(), "trust.json")}}
	h.m.opts.ConfigDir = t.TempDir()
	return mgr
}

func projectServers(t *testing.T, h *harness) map[string]mcp.ServerConfig {
	t.Helper()
	servers, _, err := mcp.LoadProject(h.m.opts.Cwd)
	if err != nil {
		t.Fatal(err)
	}
	return servers
}

// TestMCPAddDialogSteps walks the dialog with key events only, and checks
// the entry it wrote. No server is started: "never-run" is a command that
// does not exist, which the result reports rather than hanging.
func TestMCPAddDialogSteps(t *testing.T) {
	h := newHarness(t)
	withMCP(t, h)
	h.keys("/mcp add")
	h.enter()
	if h.m.wiz == nil {
		t.Fatalf("no dialog opened:\n%s", plain(h.m.View()))
	}
	steps := []struct {
		name  string
		typed string
		// want is a fragment the dialog must be showing afterwards.
		want string
	}{
		{name: "name", typed: "docs", want: "transport"},
		{name: "transport", typed: "", want: "command"},
		{name: "command", typed: "never-run --serve", want: "settings"},
		{name: "first setting", typed: "TOKEN=abc", want: "TOKEN"},
		{name: "second setting", typed: "OTHER=2", want: "OTHER"},
		{name: "done with settings", typed: "", want: "never-run --serve"},
	}
	for _, s := range steps {
		h.typed(s.typed)
		h.enter()
		if v := plain(h.m.View()); !strings.Contains(v, s.want) {
			t.Fatalf("after %s, %q is missing:\n%s", s.name, s.want, v)
		}
	}
	// The confirmation is a list: the first row writes it.
	mm, cmd := h.m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	h.m = mm.(*model)
	if h.m.wiz != nil {
		t.Fatal("the dialog should be closed after the confirmation")
	}
	h.runCmd(cmd)
	h.pump(func() bool { return h.m.opts.MCP.ProjectTrusted })
	got := projectServers(t, h)["docs"]
	if got.Command != "never-run" || strings.Join(got.Args, " ") != "--serve" {
		t.Fatalf("written entry: %+v", got)
	}
	if got.Env["TOKEN"] != "abc" || got.Env["OTHER"] != "2" {
		t.Fatalf("environment: %+v", got.Env)
	}
	// What the user wrote themselves counts as trusted.
	o := h.m.opts.MCP
	if !o.ProjectTrusted || !o.Trust.Trusted(h.m.opts.Cwd, o.ProjectHash) {
		t.Fatalf("not trusted: trusted=%v hash=%q", o.ProjectTrusted, o.ProjectHash)
	}
	h.pump(func() bool { return strings.Contains(plain(h.m.View()), "did not start") })
}

// TestMCPAddDialogKeepsQuotedArguments: the command step says "as you
// would type them in a shell", but split on spaces, so a quoted argument
// with a space in it reached the server as two arguments, quotes and all.
func TestMCPAddDialogKeepsQuotedArguments(t *testing.T) {
	h := newHarness(t)
	withMCP(t, h)
	h.runCmd(h.m.mcpCommand([]string{"add"}))
	for _, typed := range []string{"docs", "", `never-run --dir "/my projects"`, ""} {
		h.typed(typed)
		h.enter()
	}
	mm, cmd := h.m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	h.m = mm.(*model)
	h.runCmd(cmd)
	h.pump(func() bool { return h.m.opts.MCP.ProjectTrusted })
	got := projectServers(t, h)["docs"]
	if got.Command != "never-run" || len(got.Args) != 2 || got.Args[1] != "/my projects" {
		t.Fatalf("written entry: %+v", got)
	}
	h.pump(func() bool { return strings.Contains(plain(h.m.View()), "did not start") })
}

func TestMCPAddDialogValidatesAndBacksOut(t *testing.T) {
	h := newHarness(t)
	withMCP(t, h)
	h.runCmd(h.m.mcpCommand([]string{"add"}))
	for _, tc := range []struct{ name, typed, want string }{
		{name: "empty name", typed: "", want: "needed"},
		{name: "spaces in the name", typed: "two words", want: "spaces"},
	} {
		h.typed(tc.typed)
		h.enter()
		if v := plain(h.m.View()); !strings.Contains(v, tc.want) {
			t.Fatalf("%s: %q missing:\n%s", tc.name, tc.want, v)
		}
		h.inputReset()
	}
	// A good name moves on; a URL where a command belongs is refused.
	h.m.wiz.field.SetValue("docs")
	h.enter()
	h.enter() // stdio
	h.typed("https://example.test/mcp")
	h.enter()
	if v := plain(h.m.View()); !strings.Contains(v, "needs a command") {
		t.Fatalf("view:\n%s", v)
	}
	// esc walks back out step by step and finally closes the dialog.
	for i := 0; i < 3; i++ {
		if h.m.wiz == nil {
			t.Fatalf("the dialog closed after %d escapes", i)
		}
		h.update(tea.KeyMsg{Type: tea.KeyEsc})
	}
	if h.m.wiz != nil {
		t.Fatal("esc on the first step should close the dialog")
	}
	if _, err := os.Stat(filepath.Join(h.m.opts.Cwd, mcp.ProjectFile)); !os.IsNotExist(err) {
		t.Fatal("an abandoned dialog wrote the project file")
	}
}

func TestMCPAddDialogHTTPServer(t *testing.T) {
	h := newHarness(t)
	withMCP(t, h)
	h.runCmd(h.m.mcpCommand([]string{"add"}))
	h.typed("remote")
	h.enter()
	h.update(tea.KeyMsg{Type: tea.KeyDown}) // stdio → http
	h.enter()
	h.typed("https://example.test/mcp")
	h.enter()
	h.typed("Authorization: Bearer t")
	h.enter()
	h.enter() // no more headers
	h.runCmd(h.m.wizardEnter())
	h.pump(func() bool { return h.m.opts.MCP.ProjectTrusted })
	got := projectServers(t, h)["remote"]
	if got.URL != "https://example.test/mcp" || got.Type != "http" || got.Headers["Authorization"] != "Bearer t" {
		t.Fatalf("written entry: %+v", got)
	}
	if got.Command != "" || len(got.Env) != 0 {
		t.Fatalf("a remote server got a command: %+v", got)
	}
}

func TestMCPAddTypedFormStartsTheServer(t *testing.T) {
	h := newHarness(t)
	mgr := withMCP(t, h)
	// The fake server is this test binary, re-executed as an MCP server.
	h.keys("/mcp add fake -e MCP_FAKE_SERVER=1 " + os.Args[0] + " -test.run=TestFakeMCPServerProcess")
	h.enter()
	h.pump(func() bool { return strings.Contains(plain(h.m.View()), "is up") })
	if got := projectServers(t, h)["fake"]; got.Command != os.Args[0] {
		t.Fatalf("written entry: %+v", got)
	}
	if st := mgr.Status(); len(st) != 1 || st[0].Name != "fake" {
		t.Fatalf("status: %+v", st)
	}
	if v := plain(h.m.View()); !strings.Contains(v, "echo") {
		t.Fatalf("the tools it offers are not reported:\n%s", v)
	}
}

func TestMCPAddTypedFormErrors(t *testing.T) {
	h := newHarness(t)
	withMCP(t, h)
	for _, tc := range []struct{ line, want string }{
		{line: "/mcp add docs", want: "needs the command"},
		{line: "/mcp add r -x server", want: "unknown option"},
		{line: "/mcp add r https://x.test/mcp -e A=b", want: "--header"},
		{line: "/mcp add --from", want: "needs"},
	} {
		h.keys(tc.line)
		h.enter()
		if v := plain(h.m.View()); !strings.Contains(v, tc.want) {
			t.Fatalf("%s: %q missing:\n%s", tc.line, tc.want, v)
		}
	}
	if _, err := os.Stat(filepath.Join(h.m.opts.Cwd, mcp.ProjectFile)); !os.IsNotExist(err) {
		t.Fatal("a refused command wrote the project file")
	}
}

func TestMCPRemoveConfirms(t *testing.T) {
	h := newHarness(t)
	withMCP(t, h)
	if _, _, err := mcp.AddProjectServer(h.m.opts.Cwd, "docs", mcp.ServerConfig{Command: "never-run"}); err != nil {
		t.Fatal(err)
	}
	servers, hash, err := mcp.LoadProject(h.m.opts.Cwd)
	if err != nil {
		t.Fatal(err)
	}
	h.m.opts.MCP.Project, h.m.opts.MCP.ProjectHash, h.m.opts.MCP.ProjectTrusted = servers, hash, true

	h.keys("/mcp remove nope")
	h.enter()
	if v := plain(h.m.View()); !strings.Contains(v, `no MCP server named "nope"`) {
		t.Fatalf("view:\n%s", v)
	}
	// The confirmation is a list; cancelling leaves the file alone.
	h.keys("/mcp remove docs")
	h.enter()
	if h.m.picker == nil {
		t.Fatalf("no confirmation:\n%s", plain(h.m.View()))
	}
	h.update(tea.KeyMsg{Type: tea.KeyDown})
	h.enter()
	if len(projectServers(t, h)) != 1 {
		t.Fatal("cancelling removed it anyway")
	}
	h.keys("/mcp remove docs")
	h.enter()
	h.runCmd(h.m.handlePickerKeyForTest())
	h.pump(func() bool { return strings.Contains(plain(h.m.View()), "removed docs") })
	if len(projectServers(t, h)) != 0 || len(h.m.opts.MCP.Project) != 0 {
		t.Fatal("the entry is still there")
	}
	if !h.m.opts.MCP.ProjectTrusted || h.m.opts.MCP.ProjectHash == hash {
		t.Fatalf("the front end still holds the old file: %q", h.m.opts.MCP.ProjectHash)
	}
}

// handlePickerKeyForTest presses enter on the open picker.
func (m *model) handlePickerKeyForTest() tea.Cmd {
	cmd, _ := m.handlePickerKey(tea.KeyMsg{Type: tea.KeyEnter})
	return cmd
}

// /mcp search shows each server with the /mcp add line that adds it, and
// writes nothing.
func TestMCPSearchShowsAddLines(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"servers":[{"server":{"name":"io.github.acme/pg-server","title":"Postgres","description":"Query a database.","packages":[{"registryType":"npm","identifier":"@acme/pg-mcp","transport":{"type":"stdio"}}]}}]}`))
	}))
	defer srv.Close()
	t.Setenv("JANETCODE_MCP_REGISTRY", srv.URL)
	h := newHarness(t)
	withMCP(t, h)
	h.runCmd(h.m.mcpCommand([]string{"search", "postgres"}))
	h.pump(func() bool { return strings.Contains(plain(h.m.View()), "/mcp add pg-server npx -y @acme/pg-mcp") })
	if _, err := os.Stat(filepath.Join(h.m.opts.Cwd, mcp.ProjectFile)); !os.IsNotExist(err) {
		t.Fatal("a search wrote the project file")
	}
}

// A quoted header value keeps its space: "Authorization: Bearer x" is one
// header, not three words /mcp add refuses.
func TestMCPAddTypedFormQuotedHeader(t *testing.T) {
	h := newHarness(t)
	withMCP(t, h)
	h.keys(`/mcp add remote --header 'Authorization: Bearer tok en' http://127.0.0.1:1/mcp`)
	h.enter()
	h.pump(func() bool { _, ok := projectServers(t, h)["remote"]; return ok })
	if got := projectServers(t, h)["remote"].Headers["Authorization"]; got != "Bearer tok en" {
		t.Fatalf("header = %q", got)
	}
	h.keys(`/mcp add bad --header 'open`)
	h.enter()
	if v := plain(h.m.View()); !strings.Contains(v, "unclosed ' quote") {
		t.Fatalf("an unclosed quote is not reported:\n%s", v)
	}
}
