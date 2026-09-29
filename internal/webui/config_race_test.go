package webui

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/xmonader/janetcode/internal/mcp"
	"github.com/xmonader/janetcode/internal/mcp/mcptool"
)

// hammer runs read in a loop on another goroutine while write runs a few
// times: under -race, a shared field written without the lock its
// readers take is reported.
func hammer(read, write func()) {
	stop := make(chan struct{})
	started := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			read()
			if i == 0 {
				close(started)
			}
		}
	}()
	<-started
	for i := 0; i < 20; i++ {
		write()
	}
	close(stop)
	<-done
}

// /connect replaced the whole config, and /model, /config and /cua set
// fields of it, from one request goroutine while another request (the
// connect form, the model menu, /cua) read it.
func TestTheConfigChangesUnderALockWhilePagesReadIt(t *testing.T) {
	s, _ := firstRun(t)
	readForm := func() {
		w := httptest.NewRecorder()
		s.handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/connect", nil))
		w = httptest.NewRecorder()
		s.handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/models", nil))
		_ = s.cuaStatus()
	}
	hammer(readForm, func() {
		connectCall(t, s, `{"action":"connect","name":"dashscope-coding","url":"https://coding-intl.dashscope.aliyuncs.com/v1","keys":"good","model":"qwen3.7-plus"}`)
	})
	hammer(readForm, func() { s.model("qwen3.7-flash") })
	hammer(readForm, func() { s.configKV("effort=high") })
	hammer(readForm, func() { s.cuaOff() })
	if got := s.opts.Config.Model; got != "qwen3.7-flash" {
		t.Fatalf("model = %q", got)
	}
}

// /mcp add and remove record the project's servers from the goroutine
// that ran them, while a plain /mcp listed them from a request.
func TestTheProjectServersChangeUnderALockWhileListed(t *testing.T) {
	s, _ := firstRun(t)
	s.opts.MCP = &MCPOptions{Manager: &mcptool.Manager{}, Project: map[string]mcp.ServerConfig{"a": {Command: "a"}}}
	env := s.mcpEnv()
	hammer(func() { _ = s.mcpListing() }, func() {
		env.Updated(map[string]mcp.ServerConfig{"b": {Command: "b"}}, "h")
	})
}
