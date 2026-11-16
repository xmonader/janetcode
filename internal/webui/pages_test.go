package webui

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/agent"
	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/session"
)

const pageToken = "owner-token-s3cret"

// pageServer is a token-protected server whose live session holds one
// stored page.
func pageServer(t *testing.T, addr string) (http.Handler, *session.Session) {
	t.Helper()
	sess, err := session.New(t.TempDir(), t.TempDir(), "fake", "m")
	if err != nil {
		t.Fatal(err)
	}
	if err := sess.EnsureWritten(); err != nil {
		t.Fatal(err)
	}
	body := `<!doctype html><html><head><title>t</title></head><body><script>window.x=1</script><h1>Chart</h1></body></html>`
	if _, err := session.SavePage(sess.Path(), "Revenue", []byte(body)); err != nil {
		t.Fatal(err)
	}
	return newServer(context.Background(), Options{Addr: addr, Token: pageToken, Session: sess}).handler(), sess
}

func getPage(h http.Handler, host, path string, authed bool) *httptest.ResponseRecorder {
	req := httptest.NewRequest("GET", path, nil)
	req.Host = host
	if authed {
		req.Header.Set("Authorization", "Bearer "+pageToken)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestPageIsServedSandboxedWithCSP(t *testing.T) {
	h, sess := pageServer(t, "127.0.0.1:8787")
	rec := getPage(h, "127.0.0.1:8787", "/page/"+sess.Meta.ID+"/1?theme=dark", true)
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	csp := rec.Header().Get("Content-Security-Policy")
	for _, want := range []string{
		"sandbox allow-scripts allow-popups allow-popups-to-escape-sandbox",
		"connect-src 'none'", "form-action 'none'", "base-uri 'none'", "default-src 'none'",
	} {
		if !strings.Contains(csp, want) {
			t.Errorf("CSP lacks %q: %s", want, csp)
		}
	}
	if strings.Contains(csp, "allow-same-origin") {
		t.Errorf("CSP sandbox grants allow-same-origin: %s", csp)
	}
	if got := rec.Header().Get("Content-Type"); !strings.HasPrefix(got, "text/html") {
		t.Errorf("Content-Type = %q", got)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"data-theme","dark"`) || !strings.Contains(body, "color-scheme:dark") {
		t.Errorf("dark theme not passed into the page: %.300s", body)
	}
	if !strings.Contains(body, "<h1>Chart</h1>") {
		t.Error("page body missing")
	}
	if strings.HasPrefix(body, "<script") || !strings.HasPrefix(body, "<!doctype html>") {
		t.Errorf("injection displaced the doctype: %.80s", body)
	}
	light := getPage(h, "127.0.0.1:8787", "/page/"+sess.Meta.ID+"/1?theme=bogus", true).Body.String()
	if !strings.Contains(light, `"data-theme","light"`) {
		t.Error("an unknown theme should fall back to light")
	}
}

func TestPageNeverEchoesTheToken(t *testing.T) {
	h, sess := pageServer(t, "127.0.0.1:8787")
	// The URL carries no token, and a token placed there is not what
	// authorises the request, nor is it ever reflected.
	path := "/page/" + sess.Meta.ID + "/1?theme=light&token=" + pageToken
	if rec := getPage(h, "127.0.0.1:8787", path, false); rec.Code != http.StatusUnauthorized {
		t.Errorf("unauthenticated page request = %d, want 401", rec.Code)
	}
	rec := getPage(h, "127.0.0.1:8787", path, true)
	if rec.Code != 200 {
		t.Fatalf("authenticated = %d", rec.Code)
	}
	var all strings.Builder
	for k, v := range rec.Header() {
		all.WriteString(k + ": " + strings.Join(v, ",") + "\n")
	}
	all.WriteString(rec.Body.String())
	if strings.Contains(all.String(), pageToken) {
		t.Error("the owner token was echoed in the page response")
	}
	// Opening the URL must not set the session cookie either.
	if rec.Header().Get("Set-Cookie") != "" {
		t.Errorf("page response set a cookie: %q", rec.Header().Get("Set-Cookie"))
	}
}

func TestPageCanBeFramedByTheAppOnARemoteHost(t *testing.T) {
	h, sess := pageServer(t, "0.0.0.0:8787")
	rec := getPage(h, "devbox.lan:8787", "/page/"+sess.Meta.ID+"/1", true)
	if rec.Code != 200 {
		t.Fatalf("status %d", rec.Code)
	}
	if got := rec.Header().Get("X-Frame-Options"); got != "" {
		t.Errorf("X-Frame-Options = %q would stop the app framing its own page", got)
	}
	if csp := rec.Header().Get("Content-Security-Policy"); !strings.Contains(csp, "frame-ancestors 'self'") {
		t.Errorf("CSP = %q, want frame-ancestors 'self'", csp)
	}
}

func TestPageRefusesBadAddresses(t *testing.T) {
	h, sess := pageServer(t, "127.0.0.1:8787")
	for _, p := range []string{
		"/page/" + sess.Meta.ID + "/2",
		"/page/" + sess.Meta.ID + "/0",
		"/page/" + sess.Meta.ID + "/abc",
		"/page/" + sess.Meta.ID,
		"/page/nosuchsession/1",
		"/page/..%2F..%2Fetc/1",
	} {
		if rec := getPage(h, "127.0.0.1:8787", p, true); rec.Code != http.StatusNotFound {
			t.Errorf("GET %s = %d, want 404", p, rec.Code)
		}
	}
	req := httptest.NewRequest("POST", "/page/"+sess.Meta.ID+"/1", nil)
	req.Host = "127.0.0.1:8787"
	req.Header.Set("Authorization", "Bearer "+pageToken)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST = %d", rec.Code)
	}
}

func TestInjectPageChromeKeepsDoctypeFirst(t *testing.T) {
	for name, doc := range map[string]string{
		"full":         `<!DOCTYPE html><html lang="en"><head><meta charset="utf-8"></head><body>x</body></html>`,
		"no head":      `<!doctype html><html><body>x</body></html>`,
		"fragment":     `<div>x</div>`,
		"doctype only": `<!doctype html><p>x</p>`,
		"upper head":   `<HTML><HEAD></HEAD><BODY>x</BODY></HTML>`,
	} {
		out := injectPageChrome(doc, "dark")
		if !strings.Contains(out, `"data-theme","dark"`) {
			t.Errorf("%s: no theme in %.200s", name, out)
		}
		if strings.Contains(strings.ToLower(doc), "<!doctype") && !strings.HasPrefix(strings.ToLower(out), "<!doctype") {
			t.Errorf("%s: doctype is no longer first: %.80s", name, out)
		}
		if strings.Index(out, "<script>") > strings.Index(out, "x</") && strings.Contains(out, "x</") {
			t.Errorf("%s: chrome placed after the body: %.200s", name, out)
		}
	}
}

func TestRenderHTMLResultBecomesAPageEntry(t *testing.T) {
	sess, err := session.New(t.TempDir(), t.TempDir(), "fake", "m")
	if err != nil {
		t.Fatal(err)
	}
	s := newServer(context.Background(), Options{Session: sess})
	result := `Page saved at /x/` + sess.Meta.ID + `.page-3.html (page 3), titled "Q3 \"net\" revenue". It is shown above your reply in the web UI.`
	call := agent.ToolCall{ID: "c1", Name: "render_html", Input: json.RawMessage(`{"title":"t","html":"<p>"}`)}
	s.apply(agent.Event{Kind: agent.EvToolStart, Call: &call})
	s.apply(agent.Event{Kind: agent.EvToolResult, Call: &call, Output: result})
	_, ents, _, _, _ := s.snapshot()
	var page *entry
	for i := range ents {
		if ents[i].Kind == "page" {
			page = &ents[i]
		}
	}
	if page == nil {
		t.Fatalf("no page entry in %+v", ents)
	}
	d, ok := page.Data.(pageData)
	if !ok || d.N != 3 || d.SID != sess.Meta.ID || d.Title != `Q3 "net" revenue` {
		t.Errorf("page data = %+v", page.Data)
	}

	// A failed call shows no page.
	s2 := newServer(context.Background(), Options{Session: sess})
	s2.apply(agent.Event{Kind: agent.EvToolStart, Call: &call})
	s2.apply(agent.Event{Kind: agent.EvToolResult, Call: &call, Output: "Error: html is required", IsError: true})
	_, ents2, _, _, _ := s2.snapshot()
	for _, e := range ents2 {
		if e.Kind == "page" {
			t.Errorf("an error result produced a page entry: %+v", e)
		}
	}
}

func TestReplayRestoresPages(t *testing.T) {
	sess, err := session.New(t.TempDir(), t.TempDir(), "fake", "m")
	if err != nil {
		t.Fatal(err)
	}
	s := newServer(context.Background(), Options{Session: sess})
	result := `Page saved at /p (page 2), titled "Chart". It is shown above your reply in the web UI.`
	s.replayHistory([]llm.Message{
		{Role: llm.RoleUser, Blocks: []llm.Block{{Type: llm.BlockText, Text: "show a chart"}}},
		{Role: llm.RoleAssistant, Blocks: []llm.Block{{Type: llm.BlockToolUse, ID: "c1", Name: "render_html", Input: json.RawMessage(`{"title":"Chart","html":"<p>"}`)}}},
		{Role: llm.RoleUser, Blocks: []llm.Block{{Type: llm.BlockToolResult, ToolUseID: "c1", Text: result}}},
		{Role: llm.RoleAssistant, Blocks: []llm.Block{{Type: llm.BlockText, Text: "Revenue doubled."}}},
	})
	_, ents, _, _, _ := s.snapshot()
	var kinds []string
	for _, e := range ents {
		kinds = append(kinds, e.Kind)
	}
	want := []string{"user", "tool", "page", "assistant"}
	if strings.Join(kinds, ",") != strings.Join(want, ",") {
		t.Fatalf("replayed kinds = %v, want %v (the page sits above the reply)", kinds, want)
	}
}

func TestDeletingTheSessionDeletesItsPages(t *testing.T) {
	dir := t.TempDir()
	sess, err := session.New(dir, t.TempDir(), "fake", "m")
	if err != nil {
		t.Fatal(err)
	}
	if err := sess.EnsureWritten(); err != nil {
		t.Fatal(err)
	}
	if _, err := session.SavePage(sess.Path(), "p", []byte("<p>x</p>")); err != nil {
		t.Fatal(err)
	}
	if err := session.Delete(dir, sess.Meta.ID); err != nil {
		t.Fatal(err)
	}
	if left, _ := os.ReadDir(dir); len(left) != 0 {
		t.Errorf("session dir still has %d files after Delete", len(left))
	}
}
