package webui

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestTicketIsOneTimeAndExpires(t *testing.T) {
	now := time.Now()
	tk := newTickets()
	tk.now = func() time.Time { return now }
	id, err := tk.mint()
	if err != nil {
		t.Fatal(err)
	}
	if !tk.redeem(id) {
		t.Fatal("a fresh ticket should redeem")
	}
	if tk.redeem(id) {
		t.Fatal("a ticket redeemed twice")
	}
	if tk.redeem("nope") || tk.redeem("") {
		t.Fatal("an unknown ticket redeemed")
	}
	old, _ := tk.mint()
	now = now.Add(ticketTTL + time.Second)
	if tk.redeem(old) {
		t.Fatal("an expired ticket redeemed")
	}
}

func TestTicketTableIsBounded(t *testing.T) {
	tk := newTickets()
	first, _ := tk.mint()
	var last string
	for i := 0; i < maxTickets+5; i++ {
		last, _ = tk.mint()
	}
	if len(tk.m) > maxTickets || len(tk.ord) != len(tk.m) {
		t.Fatalf("table has %d tickets (%d ordered), cap %d", len(tk.m), len(tk.ord), maxTickets)
	}
	if tk.redeem(first) {
		t.Fatal("the oldest ticket should have been dropped")
	}
	if !tk.redeem(last) {
		t.Fatal("the newest ticket was dropped")
	}
}

// The link trades itself for the cookie once, never shows the token, and
// lands on the approval.
func TestNotifyLinkSignsInOnceWithoutTheToken(t *testing.T) {
	t.Setenv(publicURLEnv, "")
	s := newServer(context.Background(), Options{Addr: "10.1.2.3:8787", Token: "s3cret"})
	link := s.notifyLink(true)
	if strings.Contains(link, "s3cret") || !strings.HasPrefix(link, "http://10.1.2.3:8787/?") {
		t.Fatalf("link = %q", link)
	}
	u, err := url.Parse(link)
	if err != nil || u.Query().Get("approve") != "1" || u.Query().Get("ticket") == "" {
		t.Fatalf("link = %q, %v", link, err)
	}
	h := s.handler()
	get := func() *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", u.RequestURI(), nil)
		r.Host = "10.1.2.3:8787"
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	w := get()
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/?approve=1" {
		t.Fatalf("first open = %d -> %q", w.Code, w.Header().Get("Location"))
	}
	c := w.Result().Cookies()
	if len(c) != 1 || c[0].Value != "s3cret" {
		t.Fatalf("cookie = %+v", c)
	}
	if w := get(); w.Code != http.StatusUnauthorized || !strings.Contains(w.Body.String(), "already used") {
		t.Fatalf("second open = %d", w.Code)
	}
	if loc := s.notifyLink(false); strings.Contains(loc, "approve") {
		t.Fatalf("an idle link should not ask for the approval: %q", loc)
	}
}

func TestBaseURLUsesTheReachableAddress(t *testing.T) {
	t.Setenv(publicURLEnv, "")
	s := newServer(context.Background(), Options{Addr: "0.0.0.0:9000", Token: "t"})
	if got := s.baseURL(); strings.Contains(got, "0.0.0.0") || !strings.HasSuffix(got, ":9000") {
		t.Fatalf("a wildcard bind gave %q", got)
	}
	t.Setenv(publicURLEnv, "https://box.tail1234.ts.net/")
	if got := s.baseURL(); got != "https://box.tail1234.ts.net" {
		t.Fatalf("JANETCODE_WEB_URL gave %q", got)
	}
}

// Behind tailscale serve the Host is the tailnet name: a loopback-bound
// server lets that one name in when JANETCODE_WEB_URL says so, and no
// other.
func TestPublicHostPassesTheLoopbackGuard(t *testing.T) {
	s := newServer(context.Background(), Options{Addr: "127.0.0.1:8787", Token: "t"})
	h := s.handler()
	code := func(host string) int {
		r := httptest.NewRequest("GET", "/api/state", nil)
		r.Host = host
		r.Header.Set("Authorization", "Bearer t")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Code
	}
	t.Setenv(publicURLEnv, "")
	if c := code("box.tail1234.ts.net"); c != http.StatusForbidden {
		t.Fatalf("without the variable = %d", c)
	}
	t.Setenv(publicURLEnv, "https://box.tail1234.ts.net")
	if c := code("box.tail1234.ts.net"); c != http.StatusOK {
		t.Fatalf("the named host = %d", c)
	}
	if c := code("evil.example"); c != http.StatusForbidden {
		t.Fatalf("another host = %d", c)
	}
}
