package inspect

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func testRegistry() *Registry {
	r := New()
	r.AddText("context", "what the request will carry", func() string { return "4.1k of 80k" })
	r.Add(Subject{Name: "events", Summary: "the log", Usage: "[N]", Report: func(_ context.Context, arg string) (string, error) {
		if arg == "boom" {
			return "", errors.New("no such filter")
		}
		return "events for " + arg, nil
	}})
	r.AddText("empty", "reports nothing", func() string { return "  \n " })
	return r
}

func TestReportAndList(t *testing.T) {
	r := testRegistry()
	ctx := context.Background()
	if out, err := r.Report(ctx, "context"); err != nil || out != "4.1k of 80k" {
		t.Fatalf("context = %q %v", out, err)
	}
	// The argument is what follows the name.
	if out, err := r.Report(ctx, "events 25"); err != nil || out != "events for 25" {
		t.Fatalf("events = %q %v", out, err)
	}
	// A subject with nothing to say says so rather than returning blank.
	if out, _ := r.Report(ctx, "empty"); !strings.Contains(out, "nothing to report") {
		t.Fatalf("empty = %q", out)
	}
	// No subject, or "list", lists them with their usage.
	for _, req := range []string{"", "list", "  "} {
		out, err := r.Report(ctx, req)
		if err != nil || !strings.Contains(out, "events [N]") || !strings.Contains(out, "the log") {
			t.Fatalf("list(%q) = %q %v", req, out, err)
		}
	}
	// A subject's own error comes back.
	if _, err := r.Report(ctx, "events boom"); err == nil || !strings.Contains(err.Error(), "no such filter") {
		t.Fatalf("err = %v", err)
	}
}

func TestUnknownSubjectSuggests(t *testing.T) {
	r := testRegistry()
	ctx := context.Background()
	// A typo is answered with the name it was probably meant to be.
	for _, typo := range []string{"contxt", "conte", "evnts", "eventss"} {
		_, err := r.Report(ctx, typo)
		if err == nil {
			t.Fatalf("%q accepted", typo)
		}
		if !strings.Contains(err.Error(), "did you mean") {
			t.Fatalf("%q: no suggestion in %v", typo, err)
		}
	}
	// Something unrelated is not guessed at.
	_, err := r.Report(ctx, "quantumfoam")
	if err == nil || strings.Contains(err.Error(), "did you mean") {
		t.Fatalf("unrelated name got a suggestion: %v", err)
	}
}

func TestAddIgnoresIncomplete(t *testing.T) {
	r := New()
	r.Add(Subject{Name: "", Report: func(context.Context, string) (string, error) { return "x", nil }})
	r.Add(Subject{Name: "noreport"})
	if len(r.Names()) != 0 {
		t.Fatalf("names = %v", r.Names())
	}
	if out := r.List(); !strings.Contains(out, "nothing is registered") {
		t.Fatalf("empty list = %q", out)
	}
	// Registering the same name again replaces it.
	r.AddText("a", "first", func() string { return "one" })
	r.AddText("a", "second", func() string { return "two" })
	if out, _ := r.Report(context.Background(), "a"); out != "two" {
		t.Fatalf("replace = %q", out)
	}
}

func TestToolReportsThroughTheRegistry(t *testing.T) {
	tl := Tool{Registry: testRegistry()}
	if !strings.Contains(tl.Description(), "context") {
		t.Fatalf("the description should name the subjects: %q", tl.Description())
	}
	out, err := tl.Run(context.Background(), []byte(`{"subject":"events","arg":"7"}`))
	if err != nil || out != "events for 7" {
		t.Fatalf("run = %q %v", out, err)
	}
	if out, err := tl.Run(context.Background(), []byte(`{}`)); err != nil || !strings.Contains(out, "inspect <subject>") {
		t.Fatalf("no subject = %q %v", out, err)
	}
	if _, err := (Tool{}).Run(context.Background(), []byte(`{"subject":"x"}`)); err == nil {
		t.Fatal("a tool with no registry should say so")
	}
}

func TestDoctorSubject(t *testing.T) {
	for in, want := range map[string]string{
		"mcp":           "extensions",
		"MCP json":      "extensions json",
		" extensions ":  "extensions",
		"security":      "security",
		"security json": "security json",
	} {
		got, ok := DoctorSubject(in)
		if !ok || got != want {
			t.Errorf("DoctorSubject(%q) = %q, %v; want %q", in, got, ok, want)
		}
	}
	for _, in := range []string{"", "  ", "fix", "mcpx", "securities"} {
		if got, ok := DoctorSubject(in); ok {
			t.Errorf("DoctorSubject(%q) = %q; a bare or unknown topic keeps the ordinary checkup", in, got)
		}
	}
}
