package weburl

import (
	"net/url"
	"testing"
)

func TestNormalizeGitHubBlob(t *testing.T) {
	cases := []struct{ in, want string }{
		{"https://github.com/o/r/blob/main/a/b.go", "https://raw.githubusercontent.com/o/r/main/a/b.go"},
		{"https://github.com/o/r/raw/v1.2/x.txt", "https://raw.githubusercontent.com/o/r/v1.2/x.txt"},
		{"https://github.com/o/r", "https://github.com/o/r"},
		{"https://github.com/o/r/issues/3", "https://github.com/o/r/issues/3"},
		{"https://example.com/o/r/blob/main/x", "https://example.com/o/r/blob/main/x"},
	}
	for _, c := range cases {
		got, changed := Normalize(c.in)
		if got != c.want || changed != (c.in != c.want) {
			t.Errorf("Normalize(%q) = %q %v, want %q", c.in, got, changed, c.want)
		}
	}
}

func TestPrivateTarget(t *testing.T) {
	private := []string{
		"http://127.0.0.1:8080/x", "http://localhost/x", "http://[::1]/x",
		"http://169.254.169.254/latest/meta-data/", "http://10.0.0.5/x",
		"http://192.168.1.1/x", "http://172.16.0.1/x", "http://box.local/x",
		"http://metadata.google.internal/x",
		// Shared address space (RFC 6598): not "private" by net's
		// definition, but never the public internet either.
		"http://100.64.0.1/x", "http://100.127.255.254/x",
	}
	for _, raw := range private {
		u, _ := url.Parse(raw)
		if PrivateTarget(u) == "" {
			t.Errorf("%s not recognised as private", raw)
		}
	}
	for _, raw := range []string{"https://example.com/x", "https://8.8.8.8/x", "https://100.63.255.255/x", "https://100.128.0.1/x"} {
		u, _ := url.Parse(raw)
		if why := PrivateTarget(u); why != "" {
			t.Errorf("%s judged private: %s", raw, why)
		}
	}
}

func TestSetRemembersWhatTheUserNamed(t *testing.T) {
	s := &Set{}
	s.Note("check http://localhost:3000/health and https://example.com/docs please")
	if !s.Has("localhost:3000") || !s.Has("localhost") {
		t.Fatal("the host the user named is not remembered")
	}
	if s.Has("169.254.169.254") {
		t.Fatal("a host nobody named is remembered")
	}
	if !s.Has("example.com") {
		t.Fatal("example.com missing")
	}
	// An IPv6 literal is bracketed in a URL whether or not it has a port.
	v6 := &Set{}
	v6.Note("the service is at http://[::1]/ and http://[fd00::7]:8080/")
	for _, h := range []string{"[::1]", "[::1]:9000", "[fd00::7]:8080", "[fd00::7]"} {
		if !v6.Has(h) {
			t.Errorf("%s not matched against the IPv6 hosts the user named", h)
		}
	}
	if v6.Has("[fd00::7]:22") {
		t.Error("a port the user did not name matched")
	}
	// A URL at the end of a sentence or in a list carries the sentence's
	// punctuation, which is not part of the host.
	s.Note("the API is on http://10.0.0.7:8080. Also try http://box.lan, or http://192.168.1.2:9000!")
	for _, h := range []string{"10.0.0.7:8080", "box.lan", "192.168.1.2:9000"} {
		if !s.Has(h) {
			t.Errorf("%s, named with punctuation after it, is not remembered", h)
		}
	}
}
