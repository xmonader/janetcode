package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// A reply that arrives as an event stream is read by a goroutine; Close
// has to end that read, not leave it blocked on a stream the server never
// finishes.
func TestStreamableHTTPCloseEndsTheEventStreamReader(t *testing.T) {
	streaming := make(chan struct{})
	gone := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var msg rpcMessage
		json.Unmarshal(body, &msg)
		switch msg.Method {
		case "initialize":
			w.Header().Set("Mcp-Session-Id", "s1")
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"result":{"protocolVersion":"2025-06-18","capabilities":{},"serverInfo":{"name":"r","version":"1"}}}`, *msg.ID)
		case "tools/list":
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"result":{"tools":[]}}`, *msg.ID)
		case "tools/call":
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			w.(http.Flusher).Flush()
			close(streaming)
			<-r.Context().Done() // the client dropped the stream
			close(gone)
		default:
			w.WriteHeader(http.StatusAccepted)
		}
	}))
	defer srv.Close()
	c, err := Start(context.Background(), "remote", ServerConfig{URL: srv.URL}, Options{HandshakeTimeout: 10 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	go c.CallTool(context.Background(), "echo", nil)
	select {
	case <-streaming:
	case <-time.After(5 * time.Second):
		t.Fatal("the call never reached the server")
	}
	c.Close()
	select {
	case <-gone:
	case <-time.After(5 * time.Second):
		t.Fatal("Close left the event stream open")
	}
}
