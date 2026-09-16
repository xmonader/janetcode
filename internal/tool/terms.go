package tool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/xmonader/janetcode/internal/shell"
	"github.com/xmonader/janetcode/internal/textfmt"
)

const (
	maxTerms     = 4
	termSentinel = "__JC_TERM_DONE__"
)

// Term is one persistent shell session: cwd and environment survive
// across sends. It is not a one-shot bash call (those still EOF).
type Term struct {
	ID      int
	Pid     int
	Started time.Time
	Ended   time.Time
	Done    bool
	Last    string

	cmd   *exec.Cmd
	stdin io.WriteCloser
	out   *swapBuffer
	done  chan struct{}
}

// Terms holds live persistent shells for a session.
type Terms struct {
	mu   sync.Mutex
	next int
	all  []*Term
}

// Open starts an interactive shell in dir. The process stays up until Close.
func (t *Terms) Open(dir string) (*Term, error) {
	if t == nil {
		return nil, errors.New("persistent terminals are not available")
	}
	t.mu.Lock()
	live := 0
	for _, x := range t.all {
		if !x.Done {
			live++
		}
	}
	if live >= maxTerms {
		t.mu.Unlock()
		return nil, fmt.Errorf("at most %d live terminals", maxTerms)
	}
	t.next++
	id := t.next
	t.mu.Unlock()

	ctx := context.Background()
	cmd := interactiveShell(ctx, dir)
	pr, pw, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	out := &swapBuffer{}
	cmd.Stdin, cmd.Stdout, cmd.Stderr = pr, out, out
	if err := cmd.Start(); err != nil {
		pw.Close()
		pr.Close()
		return nil, fmt.Errorf("start terminal: %w", err)
	}
	pr.Close()
	term := &Term{
		ID: id, Pid: cmd.Process.Pid, Started: time.Now(),
		cmd: cmd, stdin: pw, out: out, done: make(chan struct{}),
	}
	t.mu.Lock()
	t.all = append(t.all, term)
	t.mu.Unlock()
	go func() {
		_ = cmd.Wait()
		t.mu.Lock()
		term.Done, term.Ended = true, time.Now()
		if term.stdin != nil {
			_ = term.stdin.Close()
			term.stdin = nil
		}
		t.mu.Unlock()
		close(term.done)
	}()
	return term, nil
}

func interactiveShell(ctx context.Context, dir string) *exec.Cmd {
	sh := shell.Default()
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.CommandContext(ctx, sh.Path, "-NoProfile", "-NonInteractive", "-Command", "-")
	} else {
		cmd = exec.CommandContext(ctx, sh.Path, "--noprofile", "--norc", "-s")
	}
	cmd.Dir = dir
	cmd.Env = shell.ScrubEnv(os.Environ())
	shell.SetProcessGroup(cmd)
	return cmd
}

func (t *Terms) get(id int) *Term {
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, x := range t.all {
		if x.ID == id {
			return x
		}
	}
	return nil
}

// Send writes a line to the shell and waits until it finishes or timeout.
func (t *Terms) Send(ctx context.Context, id int, line string, wait time.Duration) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	term := t.get(id)
	if term == nil {
		return "", fmt.Errorf("no terminal %d", id)
	}
	t.mu.Lock()
	if term.Done {
		t.mu.Unlock()
		return "", fmt.Errorf("terminal %d has exited", id)
	}
	term.Last = strings.TrimSpace(line)
	stdin := term.stdin
	t.mu.Unlock()
	if wait <= 0 {
		wait = 8 * time.Second
	}
	before := term.out.String()
	payload := strings.TrimRight(line, "\n") + "\n" + sentinelLine() + "\n"
	if _, err := io.WriteString(stdin, payload); err != nil {
		return "", fmt.Errorf("write terminal: %w", err)
	}
	deadline := time.Now().Add(wait)
	for time.Now().Before(deadline) {
		cur := term.out.String()
		if i := strings.Index(cur[min(len(before), len(cur)):], termSentinel); i >= 0 {
			return stripSentinel(cur[min(len(before), len(cur)):]), nil
		}
		select {
		case <-ctx.Done():
			return stripSentinel(term.out.String()[min(len(before), len(term.out.String())):]), ctx.Err()
		case <-term.done:
			return stripSentinel(term.out.String()[min(len(before), len(term.out.String())):]), nil
		case <-time.After(20 * time.Millisecond):
		}
	}
	cur := term.out.String()
	if len(cur) < len(before) {
		return "", nil
	}
	return stripSentinel(cur[len(before):]), nil
}

func sentinelLine() string {
	if runtime.GOOS == "windows" {
		return "Write-Output '" + termSentinel + "'"
	}
	return "printf '%s\\n' " + termSentinel
}

func stripSentinel(s string) string {
	s = strings.ReplaceAll(s, termSentinel, "")
	return strings.TrimSpace(s)
}

// Read returns the last n runes of collected output.
func (t *Terms) Read(id, n int) (string, error) {
	term := t.get(id)
	if term == nil {
		return "", fmt.Errorf("no terminal %d", id)
	}
	out := term.out.String()
	out = stripSentinel(out)
	if n <= 0 {
		n = 4000
	}
	if len(out) > n {
		out = out[len(out)-n:]
		// The cut is by byte; step forward to the next rune boundary so
		// the first character is not half of one.
		for len(out) > 0 && !utf8.RuneStart(out[0]) {
			out = out[1:]
		}
	}
	if out == "" {
		return "(no output)", nil
	}
	return out, nil
}

// Close kills the session.
func (t *Terms) Close(id int) error {
	term := t.get(id)
	if term == nil {
		return fmt.Errorf("no terminal %d", id)
	}
	t.mu.Lock()
	if term.Done {
		t.mu.Unlock()
		return nil
	}
	cmd := term.cmd
	stdin := term.stdin
	t.mu.Unlock()
	if stdin != nil {
		_ = stdin.Close()
	}
	if cmd != nil {
		if cmd.Cancel != nil {
			_ = cmd.Cancel()
		} else if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
	}
	select {
	case <-term.done:
	case <-time.After(2 * time.Second):
	}
	return nil
}

// Signal sends SIGINT/SIGTERM to the process group.
func (t *Terms) Signal(id int) error {
	return t.Close(id)
}

// List is a snapshot, oldest first.
func (t *Terms) List() []Term {
	if t == nil {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]Term, 0, len(t.all))
	for _, x := range t.all {
		out = append(out, Term{ID: x.ID, Pid: x.Pid, Started: x.Started, Ended: x.Ended, Done: x.Done, Last: x.Last})
	}
	return out
}

// CloseAll stops every live terminal. Call when the session ends.
func (t *Terms) CloseAll() {
	if t == nil {
		return
	}
	for _, x := range t.List() {
		if !x.Done {
			_ = t.Close(x.ID)
		}
	}
}

// TermTool is the model-facing persistent shell.
type TermTool struct {
	Terms *Terms
	Root  string
	WS    *Workspace
}

func (TermTool) Name() string { return "terminal" }
func (TermTool) Description() string {
	return "A persistent shell session that keeps cwd and environment across sends. action=open starts one in the working directory; send writes a command (cwd/env persist); read returns recent output; close/signal stops it; list shows live sessions. Use this for a REPL or a sequence of cd/export. Do not use it for a one-shot command — that is bash, which still ends with EOF."
}
func (TermTool) Mutating() bool { return true }
func (TermTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{
"action":{"type":"string","enum":["open","send","read","close","signal","list"]},
"id":{"type":"integer","description":"Terminal id from open or list"},
"text":{"type":"string","description":"For send: the command line"},
"timeout_ms":{"type":"integer","description":"For send: how long to wait for output (default 8000)"}},
"required":["action"],"additionalProperties":false}`)
}

func (t TermTool) Run(ctx context.Context, input json.RawMessage) (string, error) {
	var in struct {
		Action    string `json:"action"`
		ID        int    `json:"id"`
		Text      string `json:"text"`
		TimeoutMS int    `json:"timeout_ms"`
	}
	if err := decode(input, &in); err != nil {
		return "", err
	}
	if t.Terms == nil {
		return "", errors.New("persistent terminals are not available")
	}
	switch in.Action {
	case "open":
		term, err := t.Terms.Open(rootOf(t.Root, t.WS))
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("opened terminal %d (pid %d). send commands with terminal action=send id=%d", term.ID, term.Pid, term.ID), nil
	case "send":
		if strings.TrimSpace(in.Text) == "" {
			return "", fmt.Errorf("text is required")
		}
		wait := time.Duration(in.TimeoutMS) * time.Millisecond
		out, err := t.Terms.Send(ctx, in.ID, in.Text, wait)
		if err != nil {
			return "", err
		}
		if out == "" {
			return "(no output)", nil
		}
		return textfmt.Truncate(out), nil
	case "read":
		return t.Terms.Read(in.ID, 8000)
	case "close", "signal":
		if err := t.Terms.Close(in.ID); err != nil {
			return "", err
		}
		return fmt.Sprintf("closed terminal %d", in.ID), nil
	case "list":
		all := t.Terms.List()
		if len(all) == 0 {
			return "no terminals", nil
		}
		var b strings.Builder
		for _, x := range all {
			st := "live"
			if x.Done {
				st = "exited"
			}
			fmt.Fprintf(&b, "terminal %d pid %d %s last %q\n", x.ID, x.Pid, st, x.Last)
		}
		return strings.TrimRight(b.String(), "\n"), nil
	default:
		return "", fmt.Errorf("action must be open, send, read, close, signal or list")
	}
}
