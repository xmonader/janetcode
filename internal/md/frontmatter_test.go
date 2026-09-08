package md

import (
	"reflect"
	"strings"
	"testing"
)

func TestParseRenderRoundTrip(t *testing.T) {
	meta := map[string]string{"name": "use-make", "description": "Run tests: make test, not go test", "type": "feedback", "empty": ""}
	text := Render(meta, "Body line\n\n**Why:** speed\n")
	if !strings.HasPrefix(text, "---\ndescription: \"Run tests: make test, not go test\"\nname: use-make\ntype: feedback\n---\nBody line\n") {
		t.Fatalf("render = %q", text)
	}
	got, body := Parse(text)
	delete(meta, "empty")
	if !reflect.DeepEqual(got, meta) || body != "Body line\n\n**Why:** speed\n" {
		t.Fatalf("parse = %q %q", got, body)
	}
	if m, b := Parse("\ufeff---\r\nname: 'x y'\r\n---\r\nbody\n"); m["name"] != "x y" || strings.TrimSpace(b) != "body" {
		t.Fatalf("crlf/bom: %q %q", m, b)
	}
	if m, b := Parse("plain\n---\n"); len(m) != 0 || b != "plain\n---\n" {
		t.Fatalf("plain text mangled: %q %q", m, b)
	}
	if got := List(`[read_file, "grep"] glob`); !reflect.DeepEqual(got, []string{"read_file", "grep", "glob"}) {
		t.Fatalf("list = %q", got)
	}
}
