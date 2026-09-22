package webui

import (
	"bytes"
	"context"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/agent"
)

func postJSON(t *testing.T, h http.Handler, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}

func TestFoldActivityOneTurnIsOneRun(t *testing.T) {
	facts := []fact{
		{Kind: "user"},
		{Kind: "thinking"},
		{Kind: "tool", Name: "edit_file"},
		{Kind: "tool", Name: "edit_file"},
		{Kind: "tool", Name: "bash"},
		{Kind: "thinking"},
		{Kind: "assistant"},
	}
	got := foldActivity(facts, false)
	if len(got) != 1 {
		t.Fatalf("runs = %d, want 1", len(got))
	}
	if got[0].Head != 2 || got[0].Count != 3 || got[0].Live {
		t.Fatalf("run = %+v", got[0])
	}
	if got[0].Label != "Made 2 edits · Ran 1 command" {
		t.Fatalf("label = %q", got[0].Label)
	}
}

func TestFoldActivityOneToolDoesNotFold(t *testing.T) {
	facts := []fact{{Kind: "user"}, {Kind: "tool", Name: "bash"}, {Kind: "assistant"}}
	if got := foldActivity(facts, false); len(got) != 0 {
		t.Fatalf("runs = %+v, want none", got)
	}
}

func TestFoldActivityLiveTailStaysOpen(t *testing.T) {
	facts := []fact{
		{Kind: "user"},
		{Kind: "tool", Name: "bash", Error: true},
		{Kind: "tool", Name: "bash"},
	}
	got := foldActivity(facts, true)
	if len(got) != 1 || !got[0].Live {
		t.Fatalf("run = %+v", got)
	}
	if got[0].Label != "Ran 2 commands (1 failed)" {
		t.Fatalf("label = %q", got[0].Label)
	}
}

func TestFoldActivityAssistantClosesTheRun(t *testing.T) {
	facts := []fact{
		{Kind: "user"},
		{Kind: "tool", Name: "read_file"},
		{Kind: "tool", Name: "write_file"},
		{Kind: "assistant"},
		{Kind: "tool", Name: "bash"},
		{Kind: "tool", Name: "grep"},
	}
	got := foldActivity(facts, false)
	if len(got) != 2 {
		t.Fatalf("runs = %+v", got)
	}
	if got[0].Label != "Wrote 1 file · Read 1 file" {
		t.Fatalf("first = %q", got[0].Label)
	}
	if got[1].Label != "Ran 1 command · 1 tool call" || got[1].Count != 2 {
		t.Fatalf("second = %+v", got[1])
	}
}

func TestToolResultClearsTheToolPhase(t *testing.T) {
	s := newServer(context.Background(), Options{})
	call := agent.ToolCall{ID: "1", Name: "read_file", Input: json.RawMessage(`{"path":"main.go"}`)}
	s.apply(agent.Event{Kind: agent.EvToolRun, Call: &call})
	s.apply(agent.Event{Kind: agent.EvToolResult, Call: &call, Output: "package main"})
	s.mu.Lock()
	phase := s.phase
	s.mu.Unlock()
	if phase != "thinking" {
		t.Fatalf("phase = %q, want thinking", phase)
	}
}

func TestApplyStampsALiveRun(t *testing.T) {
	s := newServer(context.Background(), Options{})
	s.mu.Lock()
	s.running = true
	s.mu.Unlock()
	s.add(entry{Kind: "user", Text: "go"})
	for _, id := range []string{"1", "2"} {
		call := agent.ToolCall{ID: id, Name: "bash", Input: json.RawMessage(`{"command":"ls"}`)}
		s.apply(agent.Event{Kind: agent.EvToolStart, Call: &call})
		s.apply(agent.Event{Kind: agent.EvToolResult, Call: &call, Output: "ok"})
	}
	_, ents, _, _, _ := s.snapshot()
	heads := 0
	for _, e := range ents {
		if e.Kind != "tool" {
			continue
		}
		if e.Run == "" {
			t.Fatalf("tool missing run: %+v", e)
		}
		if e.RunHead {
			heads++
			if e.RunLabel != "Ran 2 commands" || !e.RunLive {
				t.Fatalf("head = %+v", e)
			}
		}
	}
	if heads != 1 {
		t.Fatalf("heads = %d", heads)
	}
}

func TestMermaidEndpointFallsBack(t *testing.T) {
	s := newServer(context.Background(), Options{})
	h := s.handler()
	okBody := `{"src":"flowchart TD\n  A[Start] --> B[End]\n"}`
	w := postJSON(t, h, "/api/mermaid", okBody)
	if w.Code != 200 {
		t.Fatalf("status %d %s", w.Code, w.Body.String())
	}
	var drawn struct {
		OK    bool     `json:"ok"`
		Lines []string `json:"lines"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &drawn); err != nil {
		t.Fatal(err)
	}
	if !drawn.OK || len(drawn.Lines) == 0 {
		t.Fatalf("diagram = %+v", drawn)
	}
	bad := postJSON(t, h, "/api/mermaid", `{"src":"this is not a diagram"}`)
	var refused struct {
		OK bool `json:"ok"`
	}
	if err := json.Unmarshal(bad.Body.Bytes(), &refused); err != nil {
		t.Fatal(err)
	}
	if refused.OK {
		t.Fatal("nonsense diagram was drawn")
	}
}

func TestSteerDoesNotQueue(t *testing.T) {
	s, _ := planServer(t)
	s.mu.Lock()
	s.running = true
	s.mu.Unlock()
	w := postJSON(t, s.handler(), "/api/steer", `{"text":"look at main.go"}`)
	if w.Code != 200 {
		t.Fatalf("steer %d %s", w.Code, w.Body.String())
	}
	s.mu.Lock()
	n := len(s.queued)
	s.running = false
	s.mu.Unlock()
	if n != 0 {
		t.Fatalf("steer queued %d prompts", n)
	}
	idle := postJSON(t, s.handler(), "/api/steer", `{"text":"nope"}`)
	if idle.Code != http.StatusConflict {
		t.Fatalf("idle steer = %d", idle.Code)
	}
}

func TestWorkspaceNamesTheBranch(t *testing.T) {
	s, root := planServer(t)
	cmd := exec.Command("git", "init", "-b", "feature")
	cmd.Dir = root
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v %s", err, out)
	}
	if got := s.workspace().Branch; got != "feature" {
		t.Fatalf("branch = %q", got)
	}
}

func TestUploadReturnsAWorkspaceRelativePath(t *testing.T) {
	dir := t.TempDir()
	s := newServer(context.Background(), Options{Cwd: dir})
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	part, err := mw.CreateFormFile("files", "note.txt")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write([]byte("hello")); err != nil {
		t.Fatal(err)
	}
	mw.Close()
	req := httptest.NewRequest(http.MethodPost, "/api/upload", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	w := httptest.NewRecorder()
	s.handler().ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("upload %d %s", w.Code, w.Body.String())
	}
	var resp struct {
		Paths []string `json:"paths"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Paths) != 1 || strings.Contains(resp.Paths[0], "..") || !strings.Contains(resp.Paths[0], "note.txt") {
		t.Fatalf("paths = %v", resp.Paths)
	}
}
