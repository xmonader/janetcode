package mcptool

import (
	"context"
	"errors"
	"testing"

	"github.com/xmonader/janetcode/internal/mcp"
)

// A server the Allowed check refuses never starts, and /mcp lists it with
// the reason.
func TestManagerRefusesAServerNotAllowed(t *testing.T) {
	refused := errors.New("MCP server \"fs\" is not allowed by the managed policy")
	m := &Manager{Allowed: func(name string) error {
		if name == "fs" {
			return refused
		}
		return nil
	}}
	done := make(chan error, 1)
	if !m.Start(context.Background(), "fs", mcp.ServerConfig{Command: "/nonexistent"}, func(_ string, c *mcp.Client, err error) { done <- err }) {
		t.Fatal("Start reported nothing begun, so onDone would never be called")
	}
	if err := <-done; !errors.Is(err, refused) {
		t.Fatalf("onDone err = %v", err)
	}
	for _, s := range m.Status() {
		if s.Name == "fs" && s.Err == nil {
			t.Error("status of a refused server has no error")
		}
	}
	if _, ok := m.Client("fs"); ok {
		t.Error("a refused server has a client")
	}
}
