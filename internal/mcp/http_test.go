package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// streamableServer implements Streamable HTTP: JSON replies for most
// methods, an SSE reply (with an interleaved server ping) for tools/call.
func streamableServer(t *testing.T) (*httptest.Server, *sync.Map) {
	seen := &sync.Map{}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodDelete:
			seen.Store("deleted", r.Header.Get("Mcp-Session-Id"))
			w.WriteHeader(http.StatusNoContent)
			return
		case http.MethodGet:
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		body, _ := io.ReadAll(r.Body)
		var msg rpcMessage
		json.Unmarshal(body, &msg)
		if r.Header.Get("Accept") == "" || r.Header.Get("MCP-Protocol-Version") == "" {
			t.Errorf("missing headers: %v", r.Header)
		}
		seen.Store("auth", r.Header.Get("Authorization"))
		switch msg.Method {
		case "initialize":
			w.Header().Set("Mcp-Session-Id", "sess-42")
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"result":{"protocolVersion":"2025-06-18","capabilities":{},"serverInfo":{"name":"remote","version":"1"}}}`, *msg.ID)
		case "notifications/initialized":
			seen.Store("session-on-notify", r.Header.Get("Mcp-Session-Id"))
			w.WriteHeader(http.StatusAccepted)
		case "tools/list":
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"result":{"tools":[{"name":"echo","inputSchema":{"type":"object"},"annotations":{"readOnlyHint":true}}]}}`, *msg.ID)
		case "tools/call":
			w.Header().Set("Content-Type", "text/event-stream")
			fl := w.(http.Flusher)
			fmt.Fprint(w, ": keepalive\n\n")
			fmt.Fprint(w, "event: message\ndata: {\"jsonrpc\":\"2.0\",\"id\":\"srv-ping\",\"method\":\"ping\"}\n\n")
			fl.Flush()
			fmt.Fprintf(w, "data: {\"jsonrpc\":\"2.0\",\"id\":%s,\"result\":{\"content\":[{\"type\":\"text\",\"text\":\"streamed\"}]}}\n\n", *msg.ID)
			fl.Flush()
		default:
			if msg.ID != nil && msg.Method == "" {
				seen.Store("ping-answered", true)
				w.WriteHeader(http.StatusAccepted)
				return
			}
			w.WriteHeader(http.StatusBadRequest)
		}
	})), seen
}

func TestStreamableHTTP(t *testing.T) {
	srv, seen := streamableServer(t)
	defer srv.Close()
	t.Setenv("MCP_TEST_TOKEN", "tok")
	c, err := Start(context.Background(), "remote", ServerConfig{URL: srv.URL, Headers: map[string]string{"Authorization": "Bearer ${MCP_TEST_TOKEN}"}}, Options{HandshakeTimeout: 10 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if c.ServerInfo.Name != "remote" || len(c.Tools()) != 1 {
		t.Fatalf("info %+v tools %d", c.ServerInfo, len(c.Tools()))
	}
	if v, _ := seen.Load("session-on-notify"); v != "sess-42" {
		t.Fatalf("session id not echoed: %v", v)
	}
	res, err := c.CallTool(context.Background(), "echo", json.RawMessage(`{}`))
	if err != nil || res.Text != "streamed" {
		t.Fatalf("call = %+v %v", res, err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		if v, ok := seen.Load("ping-answered"); ok && v == true {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("server ping over SSE was not answered")
		}
		time.Sleep(10 * time.Millisecond)
	}
	c.Close()
	if v, _ := seen.Load("deleted"); v != "sess-42" {
		t.Fatalf("session not deleted on close: %v", v)
	}
	if v, _ := seen.Load("auth"); v != "Bearer tok" {
		t.Fatalf("auth header = %v (env vars in headers must expand)", v)
	}
}

// legacySSEServer implements the 2024-11-05 transport: GET /sse announces
// the endpoint and carries every reply; POST /messages returns 202.
func legacySSEServer(t *testing.T) *httptest.Server {
	var (
		mu     sync.Mutex
		stream http.ResponseWriter
		fl     http.Flusher
	)
	push := func(s string) {
		mu.Lock()
		defer mu.Unlock()
		if stream == nil {
			return // client went away
		}
		fmt.Fprintf(stream, "event: message\ndata: %s\n\n", s)
		fl.Flush()
	}
	mux := http.NewServeMux()
	ready := make(chan struct{})
	mux.HandleFunc("/sse", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed) // makes the client fall back
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		mu.Lock()
		stream, fl = w, w.(http.Flusher)
		fmt.Fprint(w, "event: endpoint\ndata: /messages?session=abc\n\n")
		fl.Flush()
		mu.Unlock()
		close(ready)
		<-r.Context().Done()
		mu.Lock()
		stream, fl = nil, nil
		mu.Unlock()
	})
	mux.HandleFunc("/messages", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("session") != "abc" {
			t.Errorf("endpoint query lost: %s", r.URL)
		}
		body, _ := io.ReadAll(r.Body)
		var msg rpcMessage
		json.Unmarshal(body, &msg)
		w.WriteHeader(http.StatusAccepted)
		go func() {
			<-ready
			switch msg.Method {
			case "initialize":
				push(fmt.Sprintf(`{"jsonrpc":"2.0","id":%s,"result":{"protocolVersion":"2024-11-05","capabilities":{},"serverInfo":{"name":"legacy","version":"1"}}}`, *msg.ID))
			case "tools/list":
				push(fmt.Sprintf(`{"jsonrpc":"2.0","id":%s,"result":{"tools":[{"name":"old","inputSchema":{"type":"object"}}]}}`, *msg.ID))
			case "tools/call":
				push(fmt.Sprintf(`{"jsonrpc":"2.0","id":%s,"result":{"content":[{"type":"text","text":"from stream"}]}}`, *msg.ID))
			}
		}()
	})
	return httptest.NewServer(mux)
}

func TestLegacySSEFallback(t *testing.T) {
	srv := legacySSEServer(t)
	defer srv.Close()
	c, err := Start(context.Background(), "old", ServerConfig{URL: srv.URL + "/sse"}, Options{HandshakeTimeout: 10 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if c.ServerInfo.Name != "legacy" || len(c.Tools()) != 1 || c.Tools()[0].Name != "old" {
		t.Fatalf("info %+v tools %+v", c.ServerInfo, c.Tools())
	}
	res, err := c.CallTool(context.Background(), "old", nil)
	if err != nil || res.Text != "from stream" {
		t.Fatalf("call = %+v %v", res, err)
	}
	if c.tr.(*httpTransport).mode != "sse" {
		t.Fatalf("mode = %q", c.tr.(*httpTransport).mode)
	}
}

func TestHTTPErrors(t *testing.T) {
	if _, err := Start(context.Background(), "x", ServerConfig{URL: "ftp://nope"}, Options{}); err == nil || !strings.Contains(err.Error(), "bad url") {
		t.Fatalf("err = %v", err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "go away", http.StatusForbidden)
	}))
	defer srv.Close()
	_, err := Start(context.Background(), "x", ServerConfig{URL: srv.URL}, Options{HandshakeTimeout: 5 * time.Second})
	if err == nil || !strings.Contains(err.Error(), "HTTP 403") || !strings.Contains(err.Error(), "go away") {
		t.Fatalf("err = %v", err)
	}
	// A server that closes the connection mid-handshake must not hang.
	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.(http.Flusher).Flush()
	}))
	defer dead.Close()
	if _, err := Start(context.Background(), "x", ServerConfig{URL: dead.URL}, Options{HandshakeTimeout: 2 * time.Second}); err == nil {
		t.Fatal("silent server accepted")
	}
}
