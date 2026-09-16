package whatif_test

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xmonader/janetcode/internal/agent"
	"github.com/xmonader/janetcode/internal/config"
	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/session"
	"github.com/xmonader/janetcode/internal/snapshot"
	"github.com/xmonader/janetcode/internal/whatif"
)

// runGit runs a git command in dir for a test, failing it on error.
func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// fakeLive is a stub live provider: it answers from a fixed queue and
// counts how many times it was asked, so a test can prove the recorded
// portion of a run never reached it.
type fakeLive struct {
	responses []*llm.Response
	at        int
	calls     int
}

func (f *fakeLive) Name() string { return "fake-live" }

func (f *fakeLive) Stream(ctx context.Context, req llm.Request, emit func(llm.Event)) (*llm.Response, error) {
	f.calls++
	if f.at >= len(f.responses) {
		return &llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "fake-live: out of responses"), StopReason: llm.StopEndTurn}, nil
	}
	r := f.responses[f.at]
	f.at++
	return r, nil
}

// toolUseBlock builds a tool_use content block for a fabricated recording.
func toolUseBlock(id, name, input string) llm.Block {
	return llm.Block{Type: llm.BlockToolUse, ID: id, Name: name, Input: json.RawMessage(input)}
}

// buildSession fabricates a two-turn session, on disk, as if a real run
// had recorded it: turn 1 writes a.txt, turn 2 (as originally recorded)
// writes b.txt. --from-turn 2 in the test below never lets that second
// tool call run for real; it exists so Original's side of the comparison
// has something to diverge from.
func buildSession(t *testing.T, sessDir, root string, start session.StartState) *session.Session {
	t.Helper()
	sess, err := session.New(sessDir, root, "anthropic", "orig-model")
	if err != nil {
		t.Fatal(err)
	}
	appendEvent := func(e session.Event) {
		t.Helper()
		if _, err := sess.Append(e); err != nil {
			t.Fatal(err)
		}
	}

	// Turn 1: recorded, and replayed for real (--from-turn 2 keeps it
	// pre-live).
	userMsg1 := llm.TextMessage(llm.RoleUser, "create a.txt")
	asstToolUse1 := llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{toolUseBlock("call-1", "write_file", `{"path":"a.txt","content":"hello from turn 1\n"}`)}}
	toolResult1 := llm.ToolResultMessage(llm.ToolResult("call-1", "wrote a.txt", false))
	asstFinal1 := llm.TextMessage(llm.RoleAssistant, "created a.txt")
	usage := &llm.Usage{InputTokens: 10, OutputTokens: 5}

	appendEvent(session.Event{Type: session.TypeTurnStart, Turn: 1})
	appendEvent(session.Event{Type: session.TypeUserMessage, Turn: 1, Surface: true, Message: &userMsg1})
	appendEvent(session.Event{Type: session.TypeAssistant, Turn: 1, Surface: true, Message: &asstToolUse1, Usage: usage})
	appendEvent(session.Event{Type: session.TypeToolCall, Turn: 1, CallID: "call-1", Name: "write_file", Arguments: `{"path":"a.txt","content":"hello from turn 1\n"}`})
	appendEvent(session.Event{Type: session.TypeToolResult, Turn: 1, CallID: "call-1", Name: "write_file", Arguments: `{"path":"a.txt","content":"hello from turn 1\n"}`, Output: "wrote a.txt", Surface: true, Message: &toolResult1})
	appendEvent(session.Event{Type: session.TypeAssistant, Turn: 1, Surface: true, Message: &asstFinal1, Usage: usage})
	appendEvent(session.Event{Type: session.TypeTurnEnd, Turn: 1, Reason: "end_turn"})

	// Turn 2: recorded as having written b.txt; --from-turn 2 means this
	// never actually runs, only the live provider's own reply does.
	userMsg2 := llm.TextMessage(llm.RoleUser, "create b.txt")
	asstToolUse2 := llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{toolUseBlock("call-2", "write_file", `{"path":"b.txt","content":"original turn 2\n"}`)}}
	toolResult2 := llm.ToolResultMessage(llm.ToolResult("call-2", "wrote b.txt", false))
	asstFinal2 := llm.TextMessage(llm.RoleAssistant, "created b.txt")

	appendEvent(session.Event{Type: session.TypeTurnStart, Turn: 2})
	appendEvent(session.Event{Type: session.TypeUserMessage, Turn: 2, Surface: true, Message: &userMsg2})
	appendEvent(session.Event{Type: session.TypeAssistant, Turn: 2, Surface: true, Message: &asstToolUse2, Usage: usage})
	appendEvent(session.Event{Type: session.TypeToolCall, Turn: 2, CallID: "call-2", Name: "write_file", Arguments: `{"path":"b.txt","content":"original turn 2\n"}`})
	appendEvent(session.Event{Type: session.TypeToolResult, Turn: 2, CallID: "call-2", Name: "write_file", Arguments: `{"path":"b.txt","content":"original turn 2\n"}`, Output: "wrote b.txt", Surface: true, Message: &toolResult2})
	appendEvent(session.Event{Type: session.TypeAssistant, Turn: 2, Surface: true, Message: &asstFinal2, Usage: usage})
	appendEvent(session.Event{Type: session.TypeTurnEnd, Turn: 2, Reason: "end_turn"})

	if err := sess.SetStart(start); err != nil {
		t.Fatal(err)
	}

	// The session's own undo sidecar is where a real run would have left
	// agent.Change records for /undo, /proof and (here) Original.Files to
	// read back (see internal/agent/provenance.go); a fabricated event
	// log has no such run behind it, so the record is written directly.
	writeUndoChanges(t, sess.UndoPath(), []agent.Change{
		{Turn: 1, Tool: "write_file", Path: "a.txt", Existed: false, After: []byte("hello from turn 1\n")},
		{Turn: 2, Tool: "write_file", Path: "b.txt", Existed: false, After: []byte("original turn 2\n")},
	})
	return sess
}

// writeUndoChanges writes a minimal undo sidecar (see
// internal/agent/undo.go's undoFile) carrying only Changes, the part
// Original.Files reads.
func writeUndoChanges(t *testing.T, path string, changes []agent.Change) {
	t.Helper()
	data, err := json.Marshal(struct {
		Changes []agent.Change `json:"changes"`
	}{changes})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestRunReplaysThenGoesLive is the point of the package: turn 1 must be
// re-executed for real from the recording with no call to the live
// provider, turn 2 must go live instead of replaying its own recorded
// (different) tool call, and the report must say where the two runs
// first diverged, name the per-turn verdicts and files touched on each
// side, and leave no worktree behind.
func TestRunReplaysThenGoesLive(t *testing.T) {
	root := t.TempDir()
	runGit(t, root, "init", "-q")
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, root, "add", "-A")
	runGit(t, root, "commit", "-q", "-m", "init")

	repo, err := snapshot.Open(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	snap, err := repo.Take(context.Background(), "session start")
	if err != nil {
		t.Fatal(err)
	}

	sess := buildSession(t, t.TempDir(), root, session.StartState{Head: snap.Head, Snap: snap.Commit, At: time.Now().UTC()})
	path := sess.Path()

	live := &fakeLive{responses: []*llm.Response{
		{Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{toolUseBlock("live-1", "write_file", `{"path":"c.txt","content":"live turn 2\n"}`)}}, StopReason: llm.StopToolUse},
		{Message: llm.TextMessage(llm.RoleAssistant, "done"), StopReason: llm.StopEndTurn},
	}}

	cfg := &config.Config{Model: "fake-live-model", MaxIterations: 10, MaxTokens: 1000}

	// The run's tool-output archive is a temp directory; nothing outside
	// this function ever hears of it, so Run has to reclaim it itself.
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	before, err := os.ReadDir(tmp)
	if err != nil {
		t.Fatal(err)
	}

	report, err := whatif.Run(context.Background(), whatif.Options{
		SessionArg: path,
		Root:       root,
		FromTurn:   2,
		Config:     cfg,
		Provider:   live,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	after, err := os.ReadDir(tmp)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(before) {
		t.Fatalf("the run left %d temporary director(ies) behind (the archive is not removed)", len(after)-len(before))
	}

	// Turn 1 replayed for free: the live provider was only ever asked for
	// turn 2's two model calls (the tool call and the end-of-turn reply).
	if live.calls != 2 {
		t.Fatalf("live.calls = %d, want 2 (only turn 2 should reach the live provider)", live.calls)
	}

	// The report must show the two runs diverging at turn 2: the
	// original wrote b.txt, this run wrote c.txt instead.
	if report.Divergence == nil {
		t.Fatal("want a divergence: turn 2's tool call differs between the two runs")
	}
	if report.Divergence.Turn != 2 {
		t.Fatalf("divergence turn = %d, want 2", report.Divergence.Turn)
	}
	if report.Divergence.Original == nil || !strings.Contains(report.Divergence.Original.Arguments, "b.txt") {
		t.Fatalf("divergence.Original = %+v, want the recorded write_file b.txt call", report.Divergence.Original)
	}
	if report.Divergence.Now == nil || !strings.Contains(report.Divergence.Now.Arguments, "c.txt") {
		t.Fatalf("divergence.Now = %+v, want the live write_file c.txt call", report.Divergence.Now)
	}

	// Per-turn verdicts: both sides list turns 1 and 2.
	if len(report.Original.Turns) != 2 || len(report.Now.Turns) != 2 {
		t.Fatalf("turns = original %v, now %v, want 2 each", report.Original.Turns, report.Now.Turns)
	}

	// Files: the original session's log says it wrote a.txt and b.txt;
	// this run actually wrote a.txt (replayed) and c.txt (live) — never
	// b.txt, since turn 2's recorded call never actually ran.
	if !hasFile(report.Original.Files, "b.txt") {
		t.Fatalf("original files = %v, want b.txt", report.Original.Files)
	}
	if hasFile(report.Now.Files, "b.txt") {
		t.Fatalf("now files = %v, must not contain b.txt: turn 2 ran live, not from the recording", report.Now.Files)
	}
	if !hasFile(report.Now.Files, "a.txt") || !hasFile(report.Now.Files, "c.txt") {
		t.Fatalf("now files = %v, want a.txt (replayed) and c.txt (live)", report.Now.Files)
	}
	if _, err := os.Stat(filepath.Join(root, "c.txt")); err == nil {
		t.Fatal("c.txt must not exist in the real repository: the run should have stayed inside its worktree")
	}

	// Report renders without panicking, in both formats.
	if txt := report.Text(); !strings.Contains(txt, "first divergence") {
		t.Fatalf("Text() = %q, want it to mention the divergence", txt)
	}
	if _, err := report.JSON(); err != nil {
		t.Fatalf("JSON: %v", err)
	}

	// Cleanup: no worktree left registered against root.
	out := runGit(t, root, "worktree", "list", "--porcelain")
	if strings.Count(out, "worktree ") != 1 {
		t.Fatalf("git worktree list = %q, want only the main worktree", out)
	}
}

// hasFile matches by basename: a FileStat's Path is whatever the tool that
// touched it resolved to, which is absolute for the live agent's own
// worktree paths.
func hasFile(files []whatif.FileStat, name string) bool {
	for _, f := range files {
		if filepath.Base(f.Path) == name {
			return true
		}
	}
	return false
}

// TestRunRefusesOutOfRangeTurn covers the "N out of range" and "fewer
// turns than recorded" refusals.
func TestRunRefusesOutOfRangeTurn(t *testing.T) {
	root := t.TempDir()
	runGit(t, root, "init", "-q")
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, root, "add", "-A")
	runGit(t, root, "commit", "-q", "-m", "init")
	repo, err := snapshot.Open(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	snap, err := repo.Take(context.Background(), "session start")
	if err != nil {
		t.Fatal(err)
	}
	sess := buildSession(t, t.TempDir(), root, session.StartState{Head: snap.Head, Snap: snap.Commit, At: time.Now().UTC()})
	cfg := &config.Config{Model: "fake-live-model", MaxIterations: 10, MaxTokens: 1000}
	live := &fakeLive{}

	if _, err := whatif.Run(context.Background(), whatif.Options{SessionArg: sess.Path(), Root: root, FromTurn: 0, Config: cfg, Provider: live}); err == nil {
		t.Fatal("--from-turn 0 must be refused")
	}
	if _, err := whatif.Run(context.Background(), whatif.Options{SessionArg: sess.Path(), Root: root, FromTurn: 99, Config: cfg, Provider: live}); err == nil {
		t.Fatal("--from-turn beyond the recording must be refused")
	}
	if _, err := whatif.Run(context.Background(), whatif.Options{SessionArg: sess.Path(), Root: root, FromTurn: 1, Config: nil, Provider: nil}); err == nil {
		t.Fatal("no configured provider must be refused")
	}
}

// TestRunRefusesNoStartState covers a session recorded before --what-if
// existed: no Start, no replay possible.
func TestRunRefusesNoStartState(t *testing.T) {
	root := t.TempDir()
	runGit(t, root, "init", "-q")
	sessDir := t.TempDir()
	sess, err := session.New(sessDir, root, "anthropic", "test-model")
	if err != nil {
		t.Fatal(err)
	}
	msg := llm.TextMessage(llm.RoleUser, "do something")
	if _, err := sess.Append(session.Event{Type: session.TypeTurnStart, Turn: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := sess.Append(session.Event{Type: session.TypeUserMessage, Turn: 1, Surface: true, Message: &msg}); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{Model: "fake-live-model"}
	if _, err := whatif.Run(context.Background(), whatif.Options{SessionArg: sess.Path(), Root: root, FromTurn: 1, Config: cfg, Provider: &fakeLive{}}); err == nil {
		t.Fatal("a session with no recorded start state must be refused")
	} else if !strings.Contains(err.Error(), "no recorded start state") {
		t.Fatalf("error = %v, want it to name the missing start state", err)
	}
}
