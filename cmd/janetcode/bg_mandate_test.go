package main

import (
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/session"
)

func TestWiderMandate(t *testing.T) {
	orig := session.Mandate{Trust: "instructions", Mode: "default"}
	for _, c := range []struct {
		now  session.Mandate
		wide bool
	}{
		{orig, false},
		{session.Mandate{Trust: "untrusted", Mode: "readonly"}, false},
		{session.Mandate{Trust: "instructions", Mode: "yolo"}, true},
		{session.Mandate{Trust: "full", Mode: "default"}, true},
	} {
		got := widerMandate(orig, c.now)
		if (got != "") != c.wide {
			t.Errorf("%+v: %q", c.now, got)
		}
	}
	if w := widerMandate(orig, session.Mandate{Trust: "full", Mode: "yolo"}); !strings.Contains(w, "mode") || !strings.Contains(w, "trust") {
		t.Fatalf("both reasons expected: %q", w)
	}
}
