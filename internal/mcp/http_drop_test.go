package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// A Streamable HTTP server has no stream whose end announces it is gone,
// so a server that went away used to leave the client "up" forever, each
// call failing on its own. A refused connection now ends the client, so
// the manager's watcher sees it and reconnects.
func TestStreamableHTTPServerGoneEndsTheConnection(t *testing.T) {
	srv, _ := streamableServer(t)
	c, err := Start(context.Background(), "remote", ServerConfig{URL: srv.URL}, Options{HandshakeTimeout: 10 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	srv.Close()
	if _, err := c.CallTool(context.Background(), "echo", nil); err == nil {
		t.Fatal("call to a stopped server succeeded")
	}
	select {
	case <-c.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("the client still reads as connected after the server went away")
	}
	if !errors.Is(c.Err(), ErrDisconnected) {
		t.Fatalf("Err = %v, want ErrDisconnected", c.Err())
	}
}

// The spec's answer for a session the server no longer knows is a 404;
// the client has to start over, which is what a reconnect does.
func TestStreamableHTTPSessionGoneEndsTheConnection(t *testing.T) {
	var expired atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var msg rpcMessage
		json.Unmarshal(body, &msg)
		if expired.Load() {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		switch msg.Method {
		case "initialize":
			w.Header().Set("Mcp-Session-Id", "s1")
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"result":{"protocolVersion":"2025-06-18","capabilities":{},"serverInfo":{"name":"r","version":"1"}}}`, *msg.ID)
		case "tools/list":
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"result":{"tools":[]}}`, *msg.ID)
		default:
			w.WriteHeader(http.StatusAccepted)
		}
	}))
	defer srv.Close()
	c, err := Start(context.Background(), "remote", ServerConfig{URL: srv.URL}, Options{HandshakeTimeout: 10 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	expired.Store(true)
	_, _ = c.CallTool(context.Background(), "echo", nil)
	select {
	case <-c.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("a 404 for our session did not end the connection")
	}
}

// One request the server answers with an error is not the server gone.
func TestStreamableHTTPServerErrorKeepsTheConnection(t *testing.T) {
	var fail atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var msg rpcMessage
		json.Unmarshal(body, &msg)
		if fail.Load() {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		switch msg.Method {
		case "initialize":
			w.Header().Set("Mcp-Session-Id", "s1")
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"result":{"protocolVersion":"2025-06-18","capabilities":{},"serverInfo":{"name":"r","version":"1"}}}`, *msg.ID)
		case "tools/list":
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"result":{"tools":[]}}`, *msg.ID)
		default:
			w.WriteHeader(http.StatusAccepted)
		}
	}))
	defer srv.Close()
	c, err := Start(context.Background(), "remote", ServerConfig{URL: srv.URL}, Options{HandshakeTimeout: 10 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	fail.Store(true)
	_, _ = c.CallTool(context.Background(), "echo", nil)
	if c.Err() != nil {
		t.Fatalf("a 500 ended the connection: %v", c.Err())
	}
}
