package tui

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/inspect"
)

// /doctor mcp shows the extensions subject, the same text /inspect
// extensions shows, and does not start the ordinary checkup.
func TestDoctorMCPShowsTheExtensionsSubject(t *testing.T) {
	opts := fakeOptions(t)
	reg := inspect.New()
	reg.Add(inspect.Subject{Name: "extensions", Summary: "x", Usage: "[json]",
		Report: func(_ context.Context, arg string) (string, error) { return "EXTENSION REPORT " + arg, nil }})
	opts.Inspect = reg
	checkups := 0
	opts.Doctor = func(context.Context, io.Writer) error { checkups++; return nil }
	h := newHarnessFor(t, opts)

	h.keys("/doctor mcp json")
	h.enter()
	h.pump(func() bool { return strings.Contains(plain(h.m.View()), "EXTENSION REPORT json") })
	if checkups != 0 {
		t.Fatal("/doctor mcp ran the ordinary checkup")
	}
}
