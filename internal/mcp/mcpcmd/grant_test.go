package mcpcmd

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xmonader/janetcode/internal/extension"
)

// stagedHarness is newHarness with the staged-trust store and the policy
// the session would wire into the manager.
func stagedHarness(t *testing.T) *harness {
	t.Helper()
	h := newHarness(t)
	h.env.Extensions = extension.Store{Path: filepath.Join(t.TempDir(), extension.FileName)}
	h.mgr.Policy = extension.Policy{Store: h.env.Extensions, Cwd: h.cwd}
	return h
}

func call(t *testing.T, h *harness, name, in string) (string, error) {
	t.Helper()
	tl, ok := h.reg.Get(name)
	if !ok {
		t.Fatalf("%s is not registered", name)
	}
	return tl.Run(context.Background(), json.RawMessage(in))
}

// Installing a server leaves it restricted: its read-only tool answers,
// the rest are blocked, and /mcp enable <name> mutate opens them on the
// running session.
func TestAddInstallsRestrictedUntilEnabled(t *testing.T) {
	h := stagedHarness(t)
	res, err := Add(context.Background(), h.env, "fake", fake())
	if err != nil || res.Error != "" {
		t.Fatalf("add: %v %q", err, res.Error)
	}
	if !strings.Contains(strings.Join(res.Tips, "\n"), "/mcp enable fake mutate") {
		t.Fatalf("no restricted tip: %v", res.Tips)
	}
	g, ok, _ := h.env.Extensions.Get(extension.ProjectKey(h.cwd, "fake"))
	if !ok || g.Has(extension.Mutate) || !g.Has(extension.Read) {
		t.Fatalf("grant = %+v", g)
	}
	if out, err := call(t, h, "mcp__fake__echo", `{"text":"x"}`); err != nil || !strings.Contains(out, "echo: x") {
		t.Fatalf("read-only tool: %q %v", out, err)
	}
	if _, err := call(t, h, "mcp__fake__fail", `{}`); err == nil || !strings.Contains(err.Error(), "restricted") {
		t.Fatalf("mutating tool before enable: %v", err)
	}

	if _, err := GrantCommand(context.Background(), h.env, "enable", "fake", []string{"mutate"}); err != nil {
		t.Fatal(err)
	}
	if _, err := call(t, h, "mcp__fake__fail", `{}`); err == nil || !strings.Contains(err.Error(), "it broke") {
		t.Fatalf("after enable the call did not reach the server: %v", err)
	}

	// Per-tool switch.
	if _, err := GrantCommand(context.Background(), h.env, "tool", "fake", []string{"echo", "off"}); err != nil {
		t.Fatal(err)
	}
	if _, err := call(t, h, "mcp__fake__echo", `{"text":"x"}`); err == nil || !strings.Contains(err.Error(), "switched off") {
		t.Fatalf("switched-off tool: %v", err)
	}
	if _, err := GrantCommand(context.Background(), h.env, "tool", "fake", []string{"nope", "off"}); err == nil {
		t.Fatal("switching a tool the server does not have was accepted")
	}
	if _, err := GrantCommand(context.Background(), h.env, "tool", "fake", []string{"echo", "on"}); err != nil {
		t.Fatal(err)
	}
	if _, err := call(t, h, "mcp__fake__echo", `{"text":"x"}`); err != nil {
		t.Fatalf("tool back on: %v", err)
	}
}

// Disabling stops the process, unregisters its tools and keeps it from
// starting; enabling brings it back; removing forgets the grant.
func TestDisableStopsAndEnableRestarts(t *testing.T) {
	h := stagedHarness(t)
	if _, err := Add(context.Background(), h.env, "fake", fake()); err != nil {
		t.Fatal(err)
	}
	c, ok := h.mgr.Client("fake")
	if !ok {
		t.Fatal("server not up")
	}
	res, err := GrantCommand(context.Background(), h.env, "disable", "fake", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(res.Notes, "\n"), "stopped") {
		t.Fatalf("notes: %v", res.Notes)
	}
	select {
	case <-c.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("process still running after disable")
	}
	if _, ok := h.reg.Get("mcp__fake__echo"); ok {
		t.Fatal("tools survived disable")
	}
	if _, ok := h.mgr.Client("fake"); ok {
		t.Fatal("manager still holds the client")
	}

	// Enable starts it again (the file is trusted: Add trusted it).
	if _, err := GrantCommand(context.Background(), h.env, "enable", "fake", nil); err != nil {
		t.Fatal(err)
	}
	if _, ok := h.reg.Get("mcp__fake__echo"); !ok {
		t.Fatal("tools did not come back after enable")
	}

	if _, err := Remove(context.Background(), h.env, "fake"); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := h.env.Extensions.Get(extension.ProjectKey(h.cwd, "fake")); ok {
		t.Fatal("grant outlived the server")
	}
}

func TestGrantCommandRejectsBadInput(t *testing.T) {
	h := stagedHarness(t)
	ctx := context.Background()
	if _, err := GrantCommand(ctx, h.env, "enable", "ghost", nil); err == nil || !strings.Contains(err.Error(), "no MCP server called ghost") {
		t.Fatalf("unknown server: %v", err)
	}
	if _, err := Add(ctx, h.env, "fake", fake()); err != nil {
		t.Fatal(err)
	}
	if _, err := GrantCommand(ctx, h.env, "enable", "fake", []string{"shell"}); err == nil {
		t.Fatal("unknown capability accepted")
	}
	if _, err := GrantCommand(ctx, h.env, "tool", "fake", []string{"echo"}); err == nil {
		t.Fatal("tool without on|off accepted")
	}
	h.env.Extensions = extension.Store{}
	if _, err := GrantCommand(ctx, h.env, "disable", "fake", nil); err == nil {
		t.Fatal("grants accepted with nowhere to keep them")
	}
}

// Re-adding an unchanged server keeps what the user enabled; changing
// what it runs puts it back to restricted.
func TestReAddKeepsGrantOnlyWhenUnchanged(t *testing.T) {
	h := stagedHarness(t)
	ctx := context.Background()
	if _, err := Add(ctx, h.env, "fake", fake()); err != nil {
		t.Fatal(err)
	}
	if _, err := GrantCommand(ctx, h.env, "enable", "fake", []string{"mutate"}); err != nil {
		t.Fatal(err)
	}
	if _, err := Add(ctx, h.env, "fake", fake()); err != nil {
		t.Fatal(err)
	}
	g, _, _ := h.env.Extensions.Get(extension.ProjectKey(h.cwd, "fake"))
	if !g.Has(extension.Mutate) {
		t.Fatalf("unchanged re-add dropped the grant: %+v", g)
	}
	changed := fake()
	changed.Args = []string{"-test.run=NONE"}
	if _, err := Add(ctx, h.env, "fake", changed); err != nil {
		t.Fatal(err)
	}
	g, _, _ = h.env.Extensions.Get(extension.ProjectKey(h.cwd, "fake"))
	if g.Has(extension.Mutate) {
		t.Fatalf("a changed entry kept mutate: %+v", g)
	}
}
