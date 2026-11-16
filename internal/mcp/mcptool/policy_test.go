package mcptool

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xmonader/janetcode/internal/extension"
	"github.com/xmonader/janetcode/internal/mcp"
	"github.com/xmonader/janetcode/internal/tool"
)

func startPolicyManager(t *testing.T, name string, st extension.Store) (*Manager, *tool.Registry) {
	t.Helper()
	reg := tool.NewRegistry()
	m := &Manager{
		Options:  mcp.Options{Root: t.TempDir(), Version: "test", HandshakeTimeout: 20 * time.Second},
		Registry: reg,
		Policy:   extension.Policy{Store: st, Cwd: "/proj"},
	}
	t.Cleanup(m.Close)
	done := make(chan error, 1)
	cfg := mcp.ServerConfig{Command: os.Args[0], Env: map[string]string{"MCP_FAKE_SERVER": "1"}}
	if !m.Start(context.Background(), name, cfg, func(_ string, _ *mcp.Client, err error) { done <- err }) {
		t.Fatal("Start began nothing")
	}
	if err := <-done; err != nil {
		t.Logf("start: %v", err)
	}
	return m, reg
}

func run(t *testing.T, reg *tool.Registry, name string, in string) (string, error) {
	t.Helper()
	tl, ok := reg.Get(name)
	if !ok {
		t.Fatalf("tool %s not registered", name)
	}
	return tl.Run(context.Background(), json.RawMessage(in))
}

// A newly installed (restricted) server answers its read-only tool and
// its resources, and refuses every tool it did not mark read-only, with
// the way to enable it in the message. Enabling takes effect on the tool
// objects already registered.
func TestRestrictedServerBlocksMutatingTools(t *testing.T) {
	st := extension.Store{Path: filepath.Join(t.TempDir(), extension.FileName)}
	if err := st.Set(extension.ProjectKey("/proj", "fake"), extension.Restricted("installed")); err != nil {
		t.Fatal(err)
	}
	_, reg := startPolicyManager(t, "fake", st)

	if out, err := run(t, reg, "mcp__fake__echo", `{"text":"hi"}`); err != nil || !strings.Contains(out, "echo: hi") {
		t.Fatalf("read-only tool: %q %v", out, err)
	}
	if out, err := run(t, reg, "mcp__fake__list_resources", `{}`); err != nil || !strings.Contains(out, "notes.txt") {
		t.Fatalf("resources: %q %v", out, err)
	}
	_, err := run(t, reg, "mcp__fake__fail", `{}`)
	if err == nil || !strings.Contains(err.Error(), "/mcp enable fake mutate") {
		t.Fatalf("mutating tool on a restricted server: %v", err)
	}

	if err := st.Set(extension.ProjectKey("/proj", "fake"), extension.Full("approved")); err != nil {
		t.Fatal(err)
	}
	// The fake server's "fail" tool answers with an error of its own: the
	// call reached it, which is what enabling mutate has to allow.
	if _, err := run(t, reg, "mcp__fake__fail", `{}`); err == nil || !strings.Contains(err.Error(), "it broke") {
		t.Fatalf("after enabling mutate the call did not reach the server: %v", err)
	}

	g := extension.Full("approved")
	g.DisabledTools = []string{"echo"}
	if err := st.Set(extension.ProjectKey("/proj", "fake"), g); err != nil {
		t.Fatal(err)
	}
	if _, err := run(t, reg, "mcp__fake__echo", `{"text":"hi"}`); err == nil {
		t.Fatal("a switched-off tool still ran")
	}
}

// Disabling a server refuses a call made through a tool object taken
// before, and Stop ends the process and unregisters the tools.
func TestDisabledServerRefusesCallsAndStops(t *testing.T) {
	st := extension.Store{Path: filepath.Join(t.TempDir(), extension.FileName)}
	m, reg := startPolicyManager(t, "fake", st)
	held, _ := reg.Get("mcp__fake__echo")
	if _, err := held.Run(context.Background(), json.RawMessage(`{"text":"a"}`)); err != nil {
		t.Fatalf("unrecorded server: %v", err)
	}

	if err := st.Set(extension.UserKey("fake"), extension.Full("x").Without(extension.Connect)); err != nil {
		t.Fatal(err)
	}
	if _, err := held.Run(context.Background(), json.RawMessage(`{"text":"a"}`)); err == nil || !strings.Contains(err.Error(), "disabled") {
		t.Fatalf("held tool of a disabled server: %v", err)
	}
	if err := m.Permit("fake", "", false); err == nil {
		t.Fatal("Permit allowed a prompt on a disabled server")
	}
	c, _ := m.Client("fake")
	m.Stop("fake")
	if _, ok := reg.Get("mcp__fake__echo"); ok {
		t.Fatal("tools still registered after Stop")
	}
	select {
	case <-c.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("the server process was not stopped")
	}
	// And it will not start again while disabled.
	done := make(chan error, 1)
	cfg := mcp.ServerConfig{Command: os.Args[0], Env: map[string]string{"MCP_FAKE_SERVER": "1"}}
	m.Start(context.Background(), "fake", cfg, func(_ string, _ *mcp.Client, err error) { done <- err })
	if err := <-done; err == nil || !strings.Contains(err.Error(), "disabled") {
		t.Fatalf("restart of a disabled server: %v", err)
	}
	if _, ok := m.Client("fake"); ok {
		t.Fatal("a disabled server has a client")
	}
}
