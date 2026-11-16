package secret

import (
	"strings"
	"testing"
)

func TestRegisteredValueIsRedactedWhereNoPatternWould(t *testing.T) {
	t.Cleanup(Forget)
	const v = "hunter2-correct horse"
	if out, n := Redact("echo " + v); n != 0 || !strings.Contains(out, v) {
		t.Fatalf("unregistered value must be untouched, got %q", out)
	}
	if !Register(v) {
		t.Fatal("Register refused a normal value")
	}
	out, n := Redact("line one\ntoken=" + v + " and again " + v)
	if n != 2 || strings.Contains(out, v) || !strings.Contains(out, "line one") {
		t.Fatalf("got %q (%d)", out, n)
	}
	if Scan("x "+v) == "" {
		t.Fatal("Scan must report a registered value")
	}
}

func TestRegisteredJSONEscapedFormIsRedacted(t *testing.T) {
	t.Cleanup(Forget)
	const v = `pa"ss\word99`
	Register(v)
	out, _ := Redact(`{"password":"pa\"ss\\word99"}`)
	if strings.Contains(out, `pa\"ss`) {
		t.Fatalf("escaped form leaked: %q", out)
	}
}

func TestRegisterRefusesShortValues(t *testing.T) {
	t.Cleanup(Forget)
	if Register("abc") || Register("") {
		t.Fatal("short values must not register")
	}
	if out, n := Redact("abc abc"); n != 0 || out != "abc abc" {
		t.Fatalf("got %q", out)
	}
}
