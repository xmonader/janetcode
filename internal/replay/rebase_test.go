package replay

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/llm"
)

const rebaseRecording = `{"type":"meta","meta":{"id":"s1","cwd":"/tmp/ws/a-s1/workspace"}}
{"type":"event","seq":1,"t":"assistant/message","turn":1,"message":{"role":"assistant","blocks":[{"type":"thinking","text":"look at /tmp/ws/a-s1/workspace/BRIEF.md"},{"type":"tool_use","id":"c1","name":"read_file","input":{"path":"/tmp/ws/a-s1/workspace/BRIEF.md"}}]}}
{"type":"event","seq":2,"t":"assistant/message","turn":1,"message":{"role":"assistant","blocks":[{"type":"tool_use","id":"c2","name":"bash","input":{"command":"cd /tmp/ws/a-s1/workspace && ls /tmp/ws/a-s1/workspace-old"}}]}}
{"type":"event","seq":3,"t":"assistant/message","turn":1,"message":{"role":"assistant","blocks":[{"type":"text","text":"done"}]}}
`

// TestRebaseMovesTheRecordingToAnotherWorkspace: a branched arm runs in a
// fresh workspace, and the replayed calls must act on it, not on the
// directory the recording ran in — but a sibling that only shares the
// prefix is not the workspace and keeps its name.
func TestRebaseMovesTheRecordingToAnotherWorkspace(t *testing.T) {
	p, err := Read(strings.NewReader(rebaseRecording))
	if err != nil {
		t.Fatal(err)
	}
	if n := p.Rebase("/tmp/ws/a-s1/workspace/", "/tmp/ws/b-s1/workspace"); n != 3 {
		t.Fatalf("changed %d blocks, want 3 (thinking, two tool calls)", n)
	}
	r1, _ := p.Stream(context.Background(), llm.Request{}, nil)
	if got := string(r1.Message.ToolUses()[0].Input); got != `{"path":"/tmp/ws/b-s1/workspace/BRIEF.md"}` {
		t.Fatalf("read_file input = %s", got)
	}
	if !strings.Contains(r1.Message.Blocks[0].Text, "/tmp/ws/b-s1/workspace/BRIEF.md") {
		t.Fatalf("thinking = %q", r1.Message.Blocks[0].Text)
	}
	r2, _ := p.Stream(context.Background(), llm.Request{}, nil)
	if got := string(r2.Message.ToolUses()[0].Input); got != `{"command":"cd /tmp/ws/b-s1/workspace && ls /tmp/ws/a-s1/workspace-old"}` {
		t.Fatalf("bash input = %s", got)
	}
	if p.Calls() != 3 {
		t.Fatalf("calls = %d", p.Calls())
	}
	if n := p.Rebase("/x", "/x"); n != 0 {
		t.Fatalf("a no-op rebase changed %d", n)
	}
}

// TestRebaseEscapesForJSON: a directory with a character JSON escapes is
// matched in its escaped form inside tool arguments.
func TestRebaseEscapesForJSON(t *testing.T) {
	p := &Provider{responses: []*llm.Response{{Message: llm.Message{Blocks: []llm.Block{
		{Type: llm.BlockToolUse, Input: []byte(`{"path":"C:\\ws\\one\\f.go"}`)},
	}}}}}
	if n := p.Rebase(`C:\ws\one`, `C:\ws\two`); n != 1 {
		t.Fatalf("changed %d", n)
	}
	if got := string(p.responses[0].Message.Blocks[0].Input); got != `{"path":"C:\\ws\\two\\f.go"}` {
		t.Fatalf("input = %s", got)
	}
}

func TestRecordedCwd(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "s.jsonl")
	os.WriteFile(path, []byte(rebaseRecording), 0o644)
	if cwd, err := RecordedCwd(path); err != nil || cwd != "/tmp/ws/a-s1/workspace" {
		t.Fatalf("cwd = %q, %v", cwd, err)
	}
	os.WriteFile(path, []byte(`{"type":"event","t":"turn/start"}`+"\n"), 0o644)
	if _, err := RecordedCwd(path); err == nil {
		t.Fatal("a file without a meta record has no cwd")
	}
}

// The recorded directory inside a longer path is not that directory,
// so it is not rewritten; a whole occurrence next to it still is.
func TestRebaseTextLeavesTheTailOfALongerPath(t *testing.T) {
	got, did := rebaseText("cd /x/home/u/proj && ls /home/u/proj/a", "/home/u/proj", "/w")
	if want := "cd /x/home/u/proj && ls /w/a"; got != want || !did {
		t.Fatalf("got %q (%v), want %q", got, did, want)
	}
}
