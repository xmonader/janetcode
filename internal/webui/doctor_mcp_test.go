package webui

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/xmonader/janetcode/internal/agent"
	"github.com/xmonader/janetcode/internal/inspect"
	"github.com/xmonader/janetcode/internal/session"
	"github.com/xmonader/janetcode/internal/tool"
)

// /doctor mcp shows what /inspect extensions shows, and does not run the
// ordinary checkup (which makes a live model call).
func TestDoctorMCPIsTheExtensionsSubject(t *testing.T) {
	root := t.TempDir()
	sess, err := session.New(t.TempDir(), root, "fake", "m")
	if err != nil {
		t.Fatal(err)
	}
	ag := &agent.Agent{Provider: &scripted{}, Tools: tool.NewRegistry(), Model: "m", Log: sess, Root: root}
	reg := inspect.New()
	var asked []string
	reg.Add(inspect.Subject{Name: "extensions", Summary: "x", Usage: "[json]",
		Report: func(_ context.Context, arg string) (string, error) {
			asked = append(asked, arg)
			return "EXTENSION REPORT " + arg, nil
		}})
	var checkups atomic.Int32
	h := testHandler(Options{Agent: ag, Session: sess, Provider: "fake", Cwd: root, Version: "test", Inspect: reg,
		Doctor: func(context.Context, io.Writer) error { checkups.Add(1); return nil }})
	ts := httptest.NewServer(h)
	defer ts.Close()

	post := func(text string) {
		t.Helper()
		body, _ := json.Marshal(map[string]string{"text": text})
		res, err := http.Post(ts.URL+"/api/command", "application/json", bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != 200 {
			t.Fatalf("%s: %d", text, res.StatusCode)
		}
	}
	post("/doctor mcp")
	post("/doctor mcp json")
	post("/inspect extensions")
	if checkups.Load() != 0 {
		t.Fatal("/doctor mcp ran the ordinary checkup")
	}
	if len(asked) != 3 || asked[0] != "" || asked[1] != "json" || asked[2] != "" {
		t.Fatalf("subject was asked %q", asked)
	}
	res, err := http.Get(ts.URL + "/api/state")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	if n := strings.Count(string(raw), "EXTENSION REPORT"); n != 3 {
		t.Fatalf("the sheet appeared %d times in the thread, want 3:\n%s", n, raw)
	}

	// A bare /doctor is still the checkup.
	post("/doctor")
	if checkups.Load() != 1 {
		t.Fatalf("/doctor ran %d checkups", checkups.Load())
	}
}
