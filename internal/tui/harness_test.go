package tui

import (
	"context"
	"encoding/json"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/xmonader/janetcode/internal/agent"
	"github.com/xmonader/janetcode/internal/config"
	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/session"
	"github.com/xmonader/janetcode/internal/tool"
)

type scripted struct {
	responses []*llm.Response
	mu        sync.Mutex
	requests  []llm.Request
}

// reqs returns the requests seen so far.
func (s *scripted) reqs() []llm.Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]llm.Request(nil), s.requests...)
}

func (s *scripted) Name() string { return "fake" }
func (s *scripted) Stream(ctx context.Context, req llm.Request, emit func(llm.Event)) (*llm.Response, error) {
	if emit == nil {
		emit = func(llm.Event) {}
	}
	s.mu.Lock()
	s.requests = append(s.requests, req)
	s.mu.Unlock()
	r := s.responses[0]
	s.responses = s.responses[1:]
	for _, b := range r.Message.Blocks {
		switch b.Type {
		case llm.BlockText:
			emit(llm.Event{Type: llm.EventTextDelta, Text: b.Text})
		case llm.BlockToolUse:
			bb := b
			emit(llm.Event{Type: llm.EventToolUseStart, Block: &bb})
			emit(llm.Event{Type: llm.EventToolUseEnd, Block: &bb})
		}
	}
	return r, nil
}

// harness runs the model's Update loop by hand: background goroutines send
// into a channel and the test pumps them through Update.
type harness struct {
	t    *testing.T
	m    *model
	msgs chan tea.Msg
}

// newSession is a session under its own temporary directory, for a model
// rooted at root.
func newSession(t *testing.T, root string) *session.Session {
	t.Helper()
	sess, err := session.New(t.TempDir(), root, "fake", "m")
	if err != nil {
		t.Fatal(err)
	}
	return sess
}

// fakeOptions is the Options every harness in this package starts from: a
// scripted provider, a session of its own, and a working directory with
// nothing in it. A test that needs one thing different takes these and
// changes that field before handing them to newHarnessWith.
func fakeOptions(t *testing.T, responses ...*llm.Response) Options {
	t.Helper()
	root := t.TempDir()
	sess := newSession(t, root)
	ag := &agent.Agent{Provider: &scripted{responses: responses}, Tools: tool.NewRegistry(tool.WriteFile{Root: root}), Model: "m", Log: sess, Root: root}
	return Options{Agent: ag, Session: sess, Provider: "fake", Cwd: root, Version: "test"}
}

func newHarness(t *testing.T, responses ...*llm.Response) *harness {
	t.Helper()
	return newHarnessFor(t, fakeOptions(t, responses...))
}

// newHarnessFor builds the model for the given options and wraps it in
// the pump harness. It is the one place that knows the wiring: every
// background sender goes to the channel the test drains, the approver and
// the asker are the model's, the terminal integration writes nowhere, and
// the model is given its first window so it is laid out.
func newHarnessFor(t *testing.T, opts Options) *harness {
	t.Helper()
	m := newModel(opts)
	m.term.out = io.Discard // no escape sequences into the test's stdout
	h := &harness{t: t, m: m, msgs: make(chan tea.Msg, 100)}
	m.send = func(msg tea.Msg) { h.msgs <- msg }
	m.queue.send = m.send
	m.approver.send = m.send
	m.asker.send = m.send
	if ag := opts.Agent; ag != nil {
		ag.Approver = m.approver
		ag.Asker = m.asker
	}
	h.update(tea.WindowSizeMsg{Width: 100, Height: 30})
	// A test that ends with a turn still running (a goal round it started,
	// a quit it tested) leaves the agent writing to the session directory
	// while TempDir removes it. Registered after the TempDirs, so it runs
	// before them: cancel the turn and let it finish.
	t.Cleanup(func() {
		// A test that sets running by hand has no goroutine to wait for:
		// only a turn with a context to cancel is really running.
		if !h.m.turn.running || h.m.turn.cancel == nil {
			return
		}
		h.m.cancelTurn()
		deadline := time.After(2 * time.Second)
		for h.m.turn.running {
			select {
			case msg := <-h.msgs:
				h.update(msg)
			case <-deadline:
				return
			}
		}
	})
	return h
}

func (h *harness) update(msg tea.Msg) {
	mm, _ := h.m.Update(msg)
	h.m = mm.(*model)
}

func (h *harness) keys(s string) {
	for _, r := range s {
		h.update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
}

func (h *harness) enter() { h.update(tea.KeyMsg{Type: tea.KeyEnter}) }

// pump feeds background messages until the predicate holds or time runs out.
func (h *harness) pump(until func() bool) {
	deadline := time.After(5 * time.Second)
	for !until() {
		select {
		case msg := <-h.msgs:
			h.update(msg)
		case <-deadline:
			h.t.Fatalf("timed out waiting; view:\n%s", plain(h.m.View()))
		}
	}
}

type blocking struct{ started chan struct{} }

func (b *blocking) Name() string { return "blocking" }
func (b *blocking) Stream(ctx context.Context, req llm.Request, emit func(llm.Event)) (*llm.Response, error) {
	close(b.started)
	<-ctx.Done()
	return nil, ctx.Err()
}

func (h *harness) inputReset() {
	h.m.input.Reset()
	h.m.layout()
}

// raceEnabled is set by race_on_test.go; the race detector slows rendering
// roughly tenfold, so timing bounds scale with it.
var raceEnabled bool

// listing is a provider that can enumerate models.
type listing struct {
	scripted
	models []llm.ModelInfo
	err    error
}

func (l *listing) ListModels(ctx context.Context) ([]llm.ModelInfo, error) { return l.models, l.err }

// updateCmd runs Update and then any command it returned, feeding the
// resulting message back in (enough for the dialog's single async step).
func (h *harness) updateCmd(msg tea.Msg) {
	mm, cmd := h.m.Update(msg)
	h.m = mm.(*model)
	h.runCmd(cmd)
}

// runCmd executes a command (or batch) and feeds what it produces back.
func (h *harness) runCmd(cmd tea.Cmd) {
	if cmd == nil {
		return
	}
	switch out := cmd().(type) {
	case nil:
	case tea.BatchMsg:
		for _, c := range out {
			h.runCmd(c)
		}
	default:
		h.update(out)
	}
}

func (h *harness) withConfig(t *testing.T, cfg *config.Config, build func(*config.Config) (llm.Provider, error)) string {
	path := filepath.Join(t.TempDir(), "config.json")
	h.m.opts.Config, h.m.opts.ConfigPath, h.m.opts.Build = cfg, path, build
	return path
}

func attachTask(h *harness) *agent.Task {
	h.t.Helper()
	task := &agent.Task{Parent: h.m.opts.Agent}
	h.m.opts.Agent.Tools.Add(tool.ReadFile{Root: h.m.opts.Cwd})
	h.m.opts.Agent.Tools.Add(task)
	return task
}

func runAgents(h *harness) {
	h.t.Helper()
	h.keys("/agents")
	h.enter()
}

// gateProvider holds Stream until release is closed, so a test can open
// /agents while the child is still running.
type gateProvider struct {
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (g *gateProvider) Name() string { return "fake" }
func (g *gateProvider) Stream(ctx context.Context, req llm.Request, emit func(llm.Event)) (*llm.Response, error) {
	g.once.Do(func() { close(g.started) })
	select {
	case <-g.release:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return &llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "surveyed"), StopReason: llm.StopEndTurn}, nil
}

func countUserTurns(h *harness) int {
	h.t.Helper()
	s := h.m.opts.Session
	_, msgs, err := session.Open(dirOf(s), s.Meta.ID)
	if err != nil {
		h.t.Fatal(err)
	}
	n := 0
	for _, m := range msgs {
		if m.Role == llm.RoleUser {
			n++
		}
	}
	return n
}

func countInfo(h *harness) int {
	n := 0
	for _, e := range h.m.transcript.entries {
		if e.kind == entryInfo {
			n++
		}
	}
	return n
}

func gitInitTUI(t *testing.T, root string) {
	t.Helper()
	for _, args := range [][]string{{"init", "-q"}, {"add", "-A"}, {"commit", "-q", "-m", "init"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
}

// failMidStream emits some assistant text and the start of a tool call and
// then fails, the way a provider does when the connection drops.
type failMidStream struct{ err error }

func (failMidStream) Name() string { return "fake" }
func (p failMidStream) Stream(ctx context.Context, req llm.Request, emit func(llm.Event)) (*llm.Response, error) {
	b := llm.Block{Type: llm.BlockToolUse, ID: "t1", Name: "write_file", Input: json.RawMessage(`{"path":"x"}`)}
	emit(llm.Event{Type: llm.EventTextDelta, Text: "I will start by"})
	emit(llm.Event{Type: llm.EventToolUseStart, Block: &b})
	return nil, p.err
}

// contrast is the WCAG contrast ratio between two "#rrggbb" colours.
func contrast(a, b string) float64 {
	l1, l2 := relLuminance(a), relLuminance(b)
	if l1 < l2 {
		l1, l2 = l2, l1
	}
	return (l1 + 0.05) / (l2 + 0.05)
}

func relLuminance(hex string) float64 {
	hex = strings.TrimPrefix(hex, "#")
	var out float64
	for i, w := range []float64{0.2126, 0.7152, 0.0722} {
		v, err := strconv.ParseInt(hex[i*2:i*2+2], 16, 32)
		if err != nil {
			return 0
		}
		c := float64(v) / 255
		if c <= 0.03928 {
			c /= 12.92
		} else {
			c = math.Pow((c+0.055)/1.055, 2.4)
		}
		out += w * c
	}
	return out
}

// heldOpen holds the first call open until a channel closes, so a test can
// act while a turn is running.
type heldOpen struct {
	until chan struct{}
	inner llm.Provider
	done  bool
}

func (b *heldOpen) Name() string { return "heldOpen" }
func (b *heldOpen) Stream(ctx context.Context, req llm.Request, emit func(llm.Event)) (*llm.Response, error) {
	if !b.done {
		b.done = true
		select {
		case <-b.until:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return b.inner.Stream(ctx, req, emit)
}

// typed feeds text the way bubbletea's reader does: every run of
// characters between spaces arrives as one KeyRunes message, and a space
// as KeySpace. h.keys sends one rune at a time, which hides the whole
// class of collisions this models.
func (h *harness) typed(s string) {
	flush := func(run []rune) {
		if len(run) > 0 {
			h.update(tea.KeyMsg{Type: tea.KeyRunes, Runes: run})
		}
	}
	var run []rune
	for _, r := range s {
		if r == ' ' {
			flush(run)
			run = nil
			h.update(tea.KeyMsg{Type: tea.KeySpace, Runes: []rune{' '}})
			continue
		}
		run = append(run, r)
	}
	flush(run)
}

func tailOf(s string) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > 6 {
		lines = lines[len(lines)-6:]
	}
	return strings.Join(lines, "\n")
}
