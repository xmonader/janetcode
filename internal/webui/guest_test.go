package webui

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/xmonader/janetcode/internal/agent"
	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/session"
	"github.com/xmonader/janetcode/internal/tool"
)

const ownerToken = "owner-secret-token"

type guestEnv struct {
	t    *testing.T
	s    *server
	ts   *httptest.Server
	prov *scripted
}

// guestServer is a token-gated server (off loopback, as when shared)
// with a recorded session and a model that answers every prompt "ok".
func guestServer(t *testing.T) *guestEnv {
	t.Helper()
	root := t.TempDir()
	sess, err := session.New(t.TempDir(), root, "fake", "m")
	if err != nil {
		t.Fatal(err)
	}
	prov := &scripted{}
	for i := 0; i < 20; i++ {
		prov.responses = append(prov.responses, &llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "ok"), StopReason: llm.StopEndTurn})
	}
	ag := &agent.Agent{Provider: prov, Tools: tool.NewRegistry(tool.WriteFile{Root: root}), Model: "m", Log: sess, Root: root}
	s := newServer(context.Background(), Options{Agent: ag, Session: sess, Provider: "fake", Cwd: root, Version: "test", Addr: "0.0.0.0:8787", Token: ownerToken})
	ts := httptest.NewServer(s.handler())
	t.Cleanup(ts.Close)
	return &guestEnv{t: t, s: s, ts: ts, prov: prov}
}

func (g *guestEnv) req(method, path, body string, hdr map[string]string) (int, string, http.Header) {
	g.t.Helper()
	r, err := http.NewRequest(method, g.ts.URL+path, strings.NewReader(body))
	if err != nil {
		g.t.Fatal(err)
	}
	for k, v := range hdr {
		r.Header.Set(k, v)
	}
	res, err := http.DefaultClient.Do(r)
	if err != nil {
		g.t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return res.StatusCode, string(b), res.Header
}

func (g *guestEnv) owner(method, path, body string) (int, string) {
	code, b, _ := g.req(method, path, body, map[string]string{"Authorization": "Bearer " + ownerToken})
	return code, b
}

// share makes a link through the owner's API and returns its path
// (/guest/<token>/) and token.
func (g *guestEnv) share() (path, token string) {
	g.t.Helper()
	code, b := g.owner("POST", "/api/guest", `{"action":"create"}`)
	if code != 200 {
		g.t.Fatalf("create = %d %s", code, b)
	}
	var out struct{ URL string }
	if err := json.Unmarshal([]byte(b), &out); err != nil {
		g.t.Fatal(err)
	}
	i := strings.Index(out.URL, "/guest/")
	if i < 0 {
		g.t.Fatalf("url = %q", out.URL)
	}
	path = out.URL[i:]
	token = strings.Trim(strings.TrimPrefix(path, "/guest/"), "/")
	return path, token
}

// sseReader reads a guest's event stream.
type sseReader struct {
	t   *testing.T
	ch  chan envelope
	end context.CancelFunc
}

func (g *guestEnv) stream(path string) *sseReader {
	g.t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	g.t.Cleanup(cancel)
	r, _ := http.NewRequestWithContext(ctx, "GET", g.ts.URL+path+"events", nil)
	res, err := http.DefaultClient.Do(r)
	if err != nil {
		g.t.Fatal(err)
	}
	if res.StatusCode != 200 {
		res.Body.Close()
		g.t.Fatalf("events = %d", res.StatusCode)
	}
	sr := &sseReader{t: g.t, ch: make(chan envelope, 256), end: cancel}
	go func() {
		defer res.Body.Close()
		defer close(sr.ch)
		sc := bufio.NewScanner(res.Body)
		sc.Buffer(make([]byte, 1<<20), 1<<20)
		for sc.Scan() {
			if d, ok := strings.CutPrefix(sc.Text(), "data: "); ok {
				var e envelope
				if json.Unmarshal([]byte(d), &e) == nil {
					sr.ch <- e
				}
			}
		}
	}()
	return sr
}

// until reads events until ok says one is the one, or fails after 5s.
func (sr *sseReader) until(what string, ok func(envelope) bool) envelope {
	sr.t.Helper()
	timeout := time.After(5 * time.Second)
	for {
		select {
		case e, open := <-sr.ch:
			if !open {
				sr.t.Fatalf("stream closed before %s", what)
			}
			if ok(e) {
				return e
			}
		case <-timeout:
			sr.t.Fatalf("no %s within 5s", what)
		}
	}
}

func (g *guestEnv) guestRequests() []guestRequest { return g.s.guestPayload().Requests }

// Every owner endpoint refuses a guest token, however it is presented:
// as the owner's cookie, as a bearer header, in the query, or by
// reaching for the owner's routes through the guest's own prefix.
func TestGuestTokenOpensNoOwnerEndpoint(t *testing.T) {
	g := guestServer(t)
	_, tok := g.share()
	owner := []string{
		"/", "/events", "/api/state", "/api/prompt", "/api/steer", "/api/cancel", "/api/permission", "/api/question",
		"/api/command", "/api/commands", "/api/models", "/api/connect", "/api/inspect", "/api/sessions",
		"/api/dashboard", "/api/dashboard/x", "/api/session/abc", "/api/session/abc/fork", "/api/workspace/check", "/api/workspace/apply",
		"/api/panel", "/api/workspace", "/api/agent", "/api/job", "/api/ptc", "/api/trajectory", "/api/trajectory/timeline",
		"/api/trajectory/raw", "/api/term", "/api/change", "/api/new", "/api/load", "/api/upload", "/api/voice", "/api/ui",
		"/api/complete", "/api/suggest", "/api/mermaid", "/api/guest", "/static/app.js",
	}
	creds := []struct {
		name string
		hdr  map[string]string
		qs   string
	}{
		{"bearer", map[string]string{"Authorization": "Bearer " + tok}, ""},
		{"owner cookie", map[string]string{"Cookie": cookieName(ownerToken) + "=" + tok}, ""},
		{"guest cookie name", map[string]string{"Cookie": cookieName(tok) + "=" + tok}, ""},
		{"query", nil, "?token=" + tok},
	}
	for _, p := range owner {
		for _, c := range creds {
			for _, m := range []string{"GET", "POST"} {
				code, body, _ := g.req(m, p+c.qs, `{"action":"create","text":"x"}`, c.hdr)
				if code != http.StatusUnauthorized {
					t.Errorf("%s %s with a guest token as %s = %d, want 401", m, p, c.name, code)
				}
				if strings.Contains(body, ownerToken) {
					t.Errorf("%s %s leaked the owner token", m, p)
				}
			}
		}
	}
	if n := len(g.guestRequests()); n != 0 {
		t.Fatalf("a refused request left %d guest requests", n)
	}
	// Reaching for the owner's routes through the guest prefix.
	for _, p := range []string{"api/state", "../api/state", "events/../../api/state", "api/prompt", "static/app.js", "%2e%2e/api/state"} {
		if code, _, _ := g.req("GET", "/guest/"+tok+"/"+p, "", nil); code != http.StatusNotFound && code != http.StatusMovedPermanently && code != http.StatusUnauthorized {
			t.Errorf("/guest/<token>/%s = %d", p, code)
		}
	}
}

// The guest page, its script and style, and its stream never carry the
// owner's token, and the page is locked to its own origin by CSP.
func TestGuestPageNeverHoldsOwnerToken(t *testing.T) {
	g := guestServer(t)
	path, _ := g.share()
	for _, f := range []string{"", "app.js", "app.css"} {
		code, body, hdr := g.req("GET", path+f, "", nil)
		if code != 200 {
			t.Fatalf("GET %s%s = %d", path, f, code)
		}
		if strings.Contains(body, ownerToken) {
			t.Errorf("%q carries the owner token", f)
		}
		if f == "" {
			csp := hdr.Get("Content-Security-Policy")
			if !strings.Contains(csp, "default-src 'none'") || !strings.Contains(csp, "frame-ancestors 'none'") {
				t.Errorf("CSP = %q", csp)
			}
			if hdr.Get("Referrer-Policy") != "no-referrer" {
				t.Errorf("Referrer-Policy = %q", hdr.Get("Referrer-Policy"))
			}
			if strings.Contains(body, "<script>") {
				t.Error("inline script would break the CSP")
			}
		}
	}
	// The guest page works with no cookie at all.
	if code, _, _ := g.req("GET", path, "", map[string]string{"Cookie": ""}); code != 200 {
		t.Errorf("page without cookies = %d", code)
	}
}

// Only the page, its two assets, the stream and "say" exist under a guest
// link; anything else, a wrong verb, or a wrong token is refused.
func TestGuestRouteTable(t *testing.T) {
	g := guestServer(t)
	path, tok := g.share()
	cases := []struct {
		method, path string
		want         int
	}{
		{"GET", path, 200},
		{"GET", "/guest/" + tok, 200}, // redirected to the slash form
		{"POST", path, 405},
		{"GET", path + "say", 405},
		{"POST", path + "events", 405},
		{"GET", path + "nope", 404},
		{"GET", path + "diff", 404},
		{"GET", path + "file", 404},
		{"POST", path + "fork", 404},
		{"POST", path + "model", 404},
		{"POST", path + "permission", 404},
		{"POST", path + "question", 404},
		{"GET", "/guest/" + strings.Repeat("0", 48) + "/", 404},
		{"GET", "/guest//", 404},
		{"GET", "/guest/", 404},
		{"POST", "/guest/" + strings.Repeat("0", 48) + "/say", 404},
	}
	for _, c := range cases {
		code, _, _ := g.req(c.method, c.path, `{"text":"x"}`, nil)
		if code != c.want {
			t.Errorf("%s %s = %d, want %d", c.method, c.path, code, c.want)
		}
	}
	// A cross-site page cannot make the guest's browser speak for it.
	for _, p := range []string{"events", "say"} {
		m := "GET"
		if p == "say" {
			m = "POST"
		}
		code, _, _ := g.req(m, path+p, `{"text":"x"}`, map[string]string{"Sec-Fetch-Site": "cross-site"})
		if code != http.StatusForbidden {
			t.Errorf("cross-site %s = %d", p, code)
		}
	}
	// The owner's own token is not a guest credential.
	if code, _, _ := g.req("GET", "/guest/"+ownerToken+"/", "", nil); code != 404 {
		t.Errorf("owner token as guest token = %d", code)
	}
}

func TestGuestSeesTheStreamAndNothingElse(t *testing.T) {
	g := guestServer(t)
	path, _ := g.share()
	// Rows an owner has that a guest must not.
	g.s.add(entry{Kind: "sheet", Name: "secret", Text: "owner-only sheet"})
	g.s.add(entry{Kind: "notice", Text: "owner-only notice /home/owner"})
	g.s.add(entry{Kind: "tool", Name: "edit_file", Summary: "a.go", Output: "edited", Preview: "--- a/a.go\n+++ b/a.go\n-DIFFLINE", Data: "card"})
	g.s.emit(envelope{Type: "permission", Perm: &permPayload{ID: "p1", Name: "bash"}})
	g.s.emit(envelope{Type: "question", Questions: []agent.Question{{Prompt: "pick?"}}})
	g.s.emit(envelope{Type: "compose", Compose: "owner draft"})

	sr := g.stream(path)
	if code, b := g.owner("POST", "/api/prompt", `{"text":"hello guests"}`); code != 200 {
		t.Fatalf("prompt = %d %s", code, b)
	}
	var seen []envelope
	sr.until("assistant reply", func(e envelope) bool {
		seen = append(seen, e)
		return e.Type == "entry" && e.Entry.Kind == "assistant" && e.Entry.Text == "ok"
	})
	var kinds []string
	for _, e := range seen {
		switch e.Type {
		case "hello", "entry", "status":
		default:
			t.Errorf("a guest was sent a %q event", e.Type)
		}
		b, _ := json.Marshal(e)
		for _, leak := range []string{"owner-only", "DIFFLINE", "owner draft", "pick?", "preview", "/home/owner", ownerToken} {
			if strings.Contains(string(b), leak) {
				t.Errorf("guest event leaks %q: %s", leak, b)
			}
		}
		if e.Type == "entry" {
			kinds = append(kinds, e.Entry.Kind)
		}
	}
	joined := strings.Join(kinds, ",")
	for _, want := range []string{"tool", "user", "assistant"} {
		if !strings.Contains(joined, want) {
			t.Errorf("guest never saw a %q row: %s", want, joined)
		}
	}
}

func TestGuestViewAllowList(t *testing.T) {
	if _, ok := guestView(envelope{Type: "entry", Entry: &entry{Kind: "verdict", Text: "x"}}); ok {
		t.Error("verdict passed")
	}
	if _, ok := guestView(envelope{Type: "permission", Perm: &permPayload{}}); ok {
		t.Error("permission passed")
	}
	v, ok := guestView(envelope{Type: "entry", Entry: &entry{ID: "e1", Kind: "tool", Name: "bash", Output: "out", Preview: "DIFF", Data: 1, TokIn: 5, Run: "e0"}})
	if !ok || v.Entry.Preview != "" || v.Entry.Data != nil || v.Entry.TokIn != 0 || v.Entry.Output != "out" {
		t.Errorf("tool view = %+v", v.Entry)
	}
	v, ok = guestView(envelope{Type: "status", Status: &statusPayload{Running: true, Phase: "thinking", Yolo: true, Cost: "$1", Mode: "yolo"}})
	if !ok || v.Status.Yolo || v.Status.Cost != "" || v.Status.Mode != "" || !v.Status.Running {
		t.Errorf("status view = %+v", v.Status)
	}
	if v, ok := guestView(envelope{Type: "entry", Entry: &entry{ID: "e9", Kind: "sheet", Gone: true}}); !ok || !v.Entry.Gone {
		t.Error("a removed row must still be removed for the guest")
	}
}

func TestGuestMessageBecomesRequestThenSendOrDismiss(t *testing.T) {
	g := guestServer(t)
	path, _ := g.share()

	for _, c := range []struct{ body string }{{`{"text":""}`}, {`{"text":"   "}`}, {`not json`}, {`{"text":"` + strings.Repeat("x", guestMaxText+1) + `"}`}} {
		if code, _, _ := g.req("POST", path+"say", c.body, nil); code < 400 {
			t.Errorf("say %.20q = %d", c.body, code)
		}
	}
	if n := len(g.guestRequests()); n != 0 {
		t.Fatalf("bad messages queued %d requests", n)
	}

	for _, txt := range []string{"/clear", "!touch pwned", "read @/etc/hostname please", "second"} {
		b, _ := json.Marshal(map[string]string{"text": txt})
		if code, body, _ := g.req("POST", path+"say", string(b), nil); code != 200 {
			t.Fatalf("say %q = %d %s", txt, code, body)
		}
	}
	reqs := g.guestRequests()
	if len(reqs) != 4 {
		t.Fatalf("requests = %+v", reqs)
	}
	// Nothing ran: a guest message is only a request until the owner sends it.
	if len(g.s.entriesCopy()) != 0 {
		t.Fatalf("a guest message ran by itself: %+v", g.s.entriesCopy())
	}
	if code, body := g.owner("GET", "/api/state", ""); code != 200 || !strings.Contains(body, `"guest"`) || !strings.Contains(body, "pwned") {
		t.Errorf("owner state does not show the requests: %d %s", code, body)
	}

	// Dismiss drops it without a turn.
	if code, b := g.owner("POST", "/api/guest", `{"action":"dismiss","id":"`+reqs[3].ID+`"}`); code != 200 {
		t.Fatalf("dismiss = %d %s", code, b)
	}
	if code, _ := g.owner("POST", "/api/guest", `{"action":"dismiss","id":"`+reqs[3].ID+`"}`); code == 200 {
		t.Error("dismissing twice succeeded")
	}
	if len(g.s.entriesCopy()) != 0 || len(g.guestRequests()) != 3 {
		t.Fatalf("dismiss: entries %d requests %d", len(g.s.entriesCopy()), len(g.guestRequests()))
	}

	// Send runs "/clear" as plain text for the model, not as the command.
	if code, b := g.owner("POST", "/api/guest", `{"action":"send","id":"`+reqs[0].ID+`"}`); code != 200 {
		t.Fatalf("send = %d %s", code, b)
	}
	waitFor(t, func() bool {
		for _, e := range g.s.entriesCopy() {
			if e.Kind == "assistant" && e.Text == "ok" {
				return true
			}
		}
		return false
	})
	var user string
	for _, e := range g.s.entriesCopy() {
		if e.Kind == "user" {
			user = e.Text
		}
	}
	if user != "/clear" {
		t.Fatalf("the turn's user text = %q", user)
	}
	if g.s.sess() == nil {
		t.Fatal("/clear ran as a command and dropped the session")
	}

	// "@path" in a guest's text reads no file of the owner's.
	waitFor(t, func() bool { g.s.mu.Lock(); defer g.s.mu.Unlock(); return !g.s.running })
	if code, b := g.owner("POST", "/api/guest", `{"action":"send","id":"`+reqs[2].ID+`"}`); code != 200 {
		t.Fatalf("send = %d %s", code, b)
	}
	waitFor(t, func() bool { g.s.mu.Lock(); defer g.s.mu.Unlock(); return !g.s.running })
	for _, e := range g.s.entriesCopy() {
		if e.Kind == "notice" && strings.HasPrefix(e.Text, "attached") {
			t.Errorf("a guest's @path attached a file: %q", e.Text)
		}
	}
	// "!touch pwned" waits and is never run by the guest.
	if len(g.guestRequests()) != 1 || g.guestRequests()[0].ID != reqs[1].ID {
		t.Fatalf("remaining = %+v", g.guestRequests())
	}
}

func (s *server) entriesCopy() []entry {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]entry, 0, len(s.entries))
	for _, e := range s.entries {
		out = append(out, *e)
	}
	return out
}

func TestGuestPendingCap(t *testing.T) {
	g := guestServer(t)
	path, _ := g.share()
	for i := 0; i < guestMaxPending; i++ {
		if code, _, _ := g.req("POST", path+"say", `{"text":"hi"}`, nil); code != 200 {
			t.Fatalf("message %d = %d", i, code)
		}
	}
	if code, _, _ := g.req("POST", path+"say", `{"text":"hi"}`, nil); code != http.StatusTooManyRequests {
		t.Errorf("over the cap = %d", code)
	}
}

func TestGuestLinkExpires(t *testing.T) {
	g := guestServer(t)
	path, tok := g.share()
	sr := g.stream(path)
	sr.until("hello", func(e envelope) bool { return e.Type == "hello" })

	g.s.guest.mu.Lock()
	g.s.guest.link.expires = time.Now().Add(-time.Second)
	g.s.guest.mu.Unlock()

	if code, _, _ := g.req("GET", path, "", nil); code != 404 {
		t.Errorf("page after expiry = %d", code)
	}
	if code, _, _ := g.req("POST", path+"say", `{"text":"late"}`, nil); code != 404 {
		t.Errorf("say after expiry = %d", code)
	}
	sr.until("end of stream", func(e envelope) bool { return e.Type == "end" })
	if p := g.s.guestPayload(); p.Active || len(p.Requests) != 0 {
		t.Errorf("payload after expiry = %+v", p)
	}
	_ = tok
}

func TestGuestLinkExpiryIsTwentyFourHours(t *testing.T) {
	g := guestServer(t)
	g.share()
	g.s.guest.mu.Lock()
	left := time.Until(g.s.guest.link.expires)
	g.s.guest.mu.Unlock()
	if left < 23*time.Hour+55*time.Minute || left > 24*time.Hour {
		t.Fatalf("link lasts %v", left)
	}
}

func TestGuestLinkRevocation(t *testing.T) {
	g := guestServer(t)
	path, _ := g.share()
	sr := g.stream(path)
	sr.until("hello", func(e envelope) bool { return e.Type == "hello" })
	g.req("POST", path+"say", `{"text":"queued"}`, nil)

	// A new link replaces the old one at once, and its waiting messages.
	path2, _ := g.share()
	if path2 == path {
		t.Fatal("a new link reused the token")
	}
	if code, _, _ := g.req("GET", path, "", nil); code != 404 {
		t.Errorf("old link after a new one = %d", code)
	}
	sr.until("end of the old stream", func(e envelope) bool { return e.Type == "end" })
	if code, _, _ := g.req("GET", path2, "", nil); code != 200 {
		t.Errorf("new link = %d", code)
	}
	if n := len(g.guestRequests()); n != 0 {
		t.Errorf("the old link's %d requests survived", n)
	}

	// Stop ends the link.
	g.req("POST", path2+"say", `{"text":"x"}`, nil)
	if code, _ := g.owner("POST", "/api/guest", `{"action":"stop"}`); code != 200 {
		t.Fatal("stop failed")
	}
	if code, _, _ := g.req("GET", path2, "", nil); code != 404 {
		t.Errorf("stopped link = %d", code)
	}
	if code, _, _ := g.req("POST", path2+"say", `{"text":"x"}`, nil); code != 404 {
		t.Errorf("say on a stopped link = %d", code)
	}
	if n := len(g.guestRequests()); n != 0 {
		t.Errorf("%d requests outlived the link", n)
	}
}

// The link follows one session: moving the server to another ends it.
func TestGuestLinkEndsWhenSessionChanges(t *testing.T) {
	g := guestServer(t)
	path, _ := g.share()
	other, err := session.New(t.TempDir(), t.TempDir(), "fake", "m")
	if err != nil {
		t.Fatal(err)
	}
	g.s.setSession(other)
	if code, _, _ := g.req("GET", path, "", nil); code != 404 {
		t.Errorf("link after the session changed = %d", code)
	}
	// And it does not come back when the first session does.
	g.s.setSession(other)
	if code, _, _ := g.req("GET", path, "", nil); code != 404 {
		t.Errorf("link = %d", code)
	}
}

func TestShareNeedsARecordedSession(t *testing.T) {
	s := newServer(context.Background(), Options{Addr: "0.0.0.0:8787", Token: ownerToken})
	if _, _, err := s.createGuestLink(); err == nil {
		t.Fatal("a link was made with no session")
	}
}

func TestShareCommand(t *testing.T) {
	g := guestServer(t)
	if code, _ := g.owner("POST", "/api/command", `{"text":"/share"}`); code != 200 {
		t.Fatalf("/share = %d", code)
	}
	var url string
	for _, e := range g.s.entriesCopy() {
		if e.Kind == "notice" && strings.Contains(e.Text, "/guest/") {
			url = e.Text[strings.Index(e.Text, "http"):]
		}
	}
	if url == "" {
		t.Fatalf("no link in %+v", g.s.entriesCopy())
	}
	if code, _, _ := g.req("GET", url[strings.Index(url, "/guest/"):], "", nil); code != 200 {
		t.Errorf("link from /share = %d", code)
	}
	g.owner("POST", "/api/command", `{"text":"/share stop"}`)
	if code, _, _ := g.req("GET", url[strings.Index(url, "/guest/"):], "", nil); code != 404 {
		t.Errorf("link after /share stop = %d", code)
	}
}

// The owner's page carries the way to share and to answer a request, and
// the owner's state, not the guest's stream, is where the link's path is.
func TestOwnerPageHasShareControls(t *testing.T) {
	g := guestServer(t)
	_, body := g.owner("GET", "/", "")
	for _, want := range []string{`id="share-btn"`, `id="guest-bar"`, `id="guest-reqs"`} {
		if !strings.Contains(body, want) {
			t.Errorf("owner page missing %s", want)
		}
	}
	path, tok := g.share()
	if _, st := g.owner("GET", "/api/state", ""); !strings.Contains(st, tok) {
		t.Error("owner state does not carry the link for Copy link")
	}
	sr := g.stream(path)
	g.s.emitGuest()
	g.s.add(entry{Kind: "assistant", Text: "marker"})
	sr.until("marker", func(e envelope) bool {
		b, _ := json.Marshal(e)
		if strings.Contains(string(b), tok) {
			t.Errorf("the guest stream carries its own token back: %s", b)
		}
		return e.Type == "entry" && e.Entry.Text == "marker"
	})
}
