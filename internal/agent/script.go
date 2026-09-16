package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/xmonader/janetcode/internal/shell"
	"github.com/xmonader/janetcode/internal/textfmt"
	"github.com/xmonader/janetcode/internal/tool"
)

// Programmatic tool calling. A model that has to read six files, keep the
// two that matter and edit one of them spends six round trips on it, and
// every round trip re-sends the whole conversation. The same work written
// as a script is one round trip: the script calls the tools, decides what
// to do with the results locally, and prints only the answer. The
// intermediate output never enters the context at all.
//
// Calls made from a script are dispatched through the same gates as calls
// made by the model — hooks, permission rules, the approval prompt, the
// diff, the event log — so a script is a way to save round trips, not a
// way around the user.

const (
	// scriptTimeout is how long a script may run before it is killed.
	scriptTimeout = 5 * time.Minute
	// scriptMaxCalls bounds a runaway loop. Fifty tool calls is far more
	// than a script that is doing something sensible needs.
	scriptMaxCalls = 50
	// scriptMaxOutput caps what a script can put into the context. The
	// whole point is that the script reduces; one that prints everything
	// it read has not reduced anything.
	scriptMaxOutput = 40_000
)

// scriptSkip are the tools a script may not call. Nesting a script inside
// a script buys nothing and makes the accounting recursive, and a subagent
// launched from a script would have no way to report to the user.
var scriptSkip = map[string]bool{"execute_code": true, "run_code": true, "workflow": true, "task": true}

// Script runs a Python program that can call the session's tools.
type Script struct {
	Parent *Agent
	// Root is the directory the script runs in. /cd moves it, so it is
	// written through SetRoot and read through root().
	Root string
	// Python is the interpreter to run. Empty means look for python3.
	Python string

	mu sync.Mutex
}

// SetRoot moves the directory scripts run in.
func (s *Script) SetRoot(dir string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Root = dir
}

func (s *Script) root() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.Root
}

func (*Script) Name() string { return "execute_code" }

func (s *Script) Description() string {
	var b strings.Builder
	b.WriteString("Run a Python script that calls your own tools, so a chain of calls costs one turn instead of one turn each. " +
		"Use it when you need three or more calls with logic between them: reading N files and reporting only what matches, " +
		"looping over search results, retrying something that may fail, or making the same edit across a set of files. " +
		"Use ordinary tool calls for a single call, or when you need to read the full result yourself.\n\n" +
		"Import the tools you want from janetcode_tools and call them with the same arguments as the tool takes; " +
		"each returns the tool's output as a string and raises ToolError if the call fails. Available: ")
	names := make([]string, 0, len(s.tools()))
	for _, t := range s.tools() {
		if t.primary != "" {
			names = append(names, t.name+"("+t.primary+", ...)")
		} else {
			names = append(names, t.name+"(...)")
		}
	}
	b.WriteString(strings.Join(names, ", "))
	b.WriteString(".\n\nOnly what the script prints comes back to you — everything the tools returned inside it stays out of your context, " +
		"so print the conclusion, not the raw output. Also available without importing: plain(text), which strips the line-number gutter that read_file and grep put on every line — match on plain(read_file(...)), not on the raw string; shell_quote(s); and retry(fn, attempts=3). " +
		fmt.Sprintf("Limits: %s, %d tool calls, %dKB of output. The script runs in the working directory.",
			scriptTimeout, scriptMaxCalls, scriptMaxOutput/1000))
	return b.String()
}

// Mutating reports true: a script is arbitrary code on the user's machine,
// the same as a shell command, whatever tools it happens to call.
func (*Script) Mutating() bool { return true }

func (*Script) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{
"code":{"type":"string","description":"The Python script. Import tools with ` + "`from janetcode_tools import read_file, grep, ...`" + ` and print your conclusion."}},
"required":["code"],"additionalProperties":false}`)
}

// Preview shows the user the code they are approving.
func (s *Script) Preview(_ context.Context, input json.RawMessage) (string, error) {
	var in struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(input, &in); err != nil {
		return "", err
	}
	return in.Code, nil
}

// stubbed is one tool as a Python function: its name and the argument a
// caller most likely passes first.
type stubbed struct{ name, primary string }

// tools lists the tools a script may call, in the order they are shown to
// the model, each with its first required argument. A stub that took
// keyword arguments only made a quarter of the scripts in a measured run
// fail on read_file("path") with a TypeError, and every one of those cost
// a whole extra turn, so the first argument is positional.
func (s *Script) tools() []stubbed {
	if s.Parent == nil || s.Parent.Tools == nil {
		return nil
	}
	var out []stubbed
	for _, n := range s.Parent.Tools.Names() {
		if scriptSkip[n] || !pyName(n) {
			continue
		}
		out = append(out, stubbed{name: n, primary: primaryArg(s.Parent.Tools, n)})
	}
	return out
}

// primaryArg is a tool's first required argument, when it has one that can
// be a Python parameter name. It comes from the schema the model is
// already reading, so the stub and the tool cannot disagree.
func primaryArg(reg *tool.Registry, name string) string {
	t, ok := reg.Get(name)
	if !ok {
		return ""
	}
	raw := t.Schema()
	var schema struct {
		Required []string `json:"required"`
	}
	if json.Unmarshal(raw, &schema) == nil && len(schema.Required) > 0 {
		if first := schema.Required[0]; usableArg(first) {
			return first
		}
	}
	// A tool whose arguments are all optional still has a natural first
	// one: list_dir(".") is how it is called, and a stub that refuses it
	// was a quarter of the script failures in a measured run.
	if first := firstProperty(raw); usableArg(first) {
		return first
	}
	return ""
}

func usableArg(s string) bool { return s != "" && pyName(s) && !pyKeyword(s) }

// firstProperty is the first key of the schema's properties object, in the
// order the schema declares it. Decoding into a map would lose that order,
// so the token stream is read directly.
func firstProperty(raw json.RawMessage) string {
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	depth := 0
	for {
		t, err := dec.Token()
		if err != nil {
			return ""
		}
		switch v := t.(type) {
		case json.Delim:
			switch v {
			case '{', '[':
				depth++
			case '}', ']':
				depth--
			}
		case string:
			if depth == 1 && v == "properties" {
				// The next tokens are '{' then the first key.
				if d, err := dec.Token(); err != nil {
					return ""
				} else if delim, ok := d.(json.Delim); !ok || delim != '{' {
					return ""
				}
				key, err := dec.Token()
				if err != nil {
					return ""
				}
				name, _ := key.(string)
				return name
			}
		}
	}
}

// pyKeyword guards against naming a parameter after a Python keyword.
func pyKeyword(s string) bool {
	switch s {
	case "and", "as", "assert", "async", "await", "break", "class", "continue",
		"def", "del", "elif", "else", "except", "finally", "for", "from",
		"global", "if", "import", "in", "is", "lambda", "none", "nonlocal",
		"not", "or", "pass", "raise", "return", "try", "while", "with", "yield":
		return true
	}
	return false
}

// pyName reports whether a tool name can be a Python identifier. An MCP
// tool called "server/thing" cannot, and is reached through call() instead.
func pyName(s string) bool {
	if s == "" || s[0] >= '0' && s[0] <= '9' {
		return false
	}
	for _, r := range s {
		if r != '_' && !(r >= 'a' && r <= 'z') && !(r >= 'A' && r <= 'Z') && !(r >= '0' && r <= '9') {
			return false
		}
	}
	return true
}

// interpreter is the Python to run, or an error explaining that there is
// none. The tool is only useful where Python 3 exists, which is nearly
// everywhere it matters, but not everywhere.
func (s *Script) interpreter() (string, error) {
	if s.Python != "" {
		return s.Python, nil
	}
	for _, name := range []string{"python3", "python"} {
		if p, err := exec.LookPath(name); err == nil {
			return p, nil
		}
	}
	return "", errors.New("execute_code needs Python 3 on the PATH and there is none; use ordinary tool calls")
}

// Available reports whether there is a Python to run scripts with. Where
// there is not, the session offers run_code in its place.
func (s *Script) Available() bool {
	_, err := s.interpreter()
	return err == nil
}

func (s *Script) Run(ctx context.Context, input json.RawMessage) (string, error) {
	var in struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(input, &in); err != nil {
		return "", fmt.Errorf("bad input: %w", err)
	}
	if strings.TrimSpace(in.Code) == "" {
		return "", errors.New("code is empty")
	}
	if s.Parent == nil {
		return "", errors.New("execute_code has no agent to call tools on")
	}
	py, err := s.interpreter()
	if err != nil {
		return "", err
	}

	dir, err := os.MkdirTemp("", "janetcode-script-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(dir)

	sock := filepath.Join(dir, "rpc.sock")
	if len(sock) > 100 {
		// Unix socket paths are short; fall back to the shortest place
		// there is rather than failing on a long TMPDIR.
		sock = filepath.Join(os.TempDir(), fmt.Sprintf("jc-%d.sock", time.Now().UnixNano()))
		defer os.Remove(sock)
	}
	if err := os.WriteFile(filepath.Join(dir, "janetcode_tools.py"), []byte(s.stub()), 0o600); err != nil {
		return "", err
	}
	script := filepath.Join(dir, "script.py")
	if err := os.WriteFile(script, []byte(in.Code), 0o600); err != nil {
		return "", err
	}

	ln, err := net.Listen("unix", sock)
	if err != nil {
		return "", fmt.Errorf("could not open the tool socket: %w", err)
	}
	defer ln.Close()

	ctx, cancel := context.WithTimeout(ctx, scriptTimeout)
	defer cancel()

	srv := &rpc{parent: s.Parent, max: scriptMaxCalls}
	go srv.serve(ctx, ln)

	cmd := exec.CommandContext(ctx, py, script)
	cmd.Dir = s.root()
	cmd.Env = append(os.Environ(),
		"JANETCODE_RPC_SOCKET="+sock,
		"PYTHONPATH="+dir,
		"PYTHONDONTWRITEBYTECODE=1",
		"PYTHONUNBUFFERED=1",
	)
	shell.SetProcessGroup(cmd)
	var stdout, stderr strings.Builder
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	runErr := cmd.Run()
	ln.Close()

	return s.report(stdout.String(), stderr.String(), runErr, srv.count(), ctx.Err()), nil
}

// report is what the model sees: the script's own output, the reason it
// stopped when it failed, and how many tool calls it made — the last so
// the model can see what the turn actually cost.
func (s *Script) report(stdout, stderr string, runErr error, calls int, ctxErr error) string {
	var b strings.Builder
	out := strings.TrimRight(stdout, "\n")
	if len(out) > scriptMaxOutput {
		// Both cuts are pulled back to a rune boundary: a script printing
		// anything but ASCII would otherwise put a half-written rune into
		// the conversation at each end of the gap.
		head := runeStartAtOrBefore(out, scriptMaxOutput*2/3)
		tail := runeStartAtOrBefore(out, len(out)-(scriptMaxOutput-head))
		out = out[:head] + fmt.Sprintf("\n\n... [%d bytes of output dropped; print less] ...\n\n", tail-head) + out[tail:]
	}
	if out != "" {
		b.WriteString(out)
	}
	switch {
	case errors.Is(ctxErr, context.DeadlineExceeded):
		nl(&b)
		fmt.Fprintf(&b, "[the script was killed after %s]", scriptTimeout)
	case runErr != nil:
		nl(&b)
		b.WriteString("[the script failed: " + runErr.Error() + "]")
		if tail := strings.TrimSpace(textfmt.TruncateTo(stderr, 4000)); tail != "" {
			b.WriteString("\n" + tail)
		}
	}
	if out == "" && runErr == nil && ctxErr == nil {
		b.WriteString("(the script printed nothing)")
	}
	nl(&b)
	fmt.Fprintf(&b, "[%s]", textfmt.Plural(calls, "tool call"))
	return b.String()
}

// runeStartAtOrBefore is i moved back until it indexes the first byte of
// a rune (or the end of the string), so a byte-budgeted cut never splits
// one.
func runeStartAtOrBefore(s string, i int) int {
	if i >= len(s) {
		return len(s)
	}
	if i < 0 {
		return 0
	}
	for i > 0 && !utf8.RuneStart(s[i]) {
		i--
	}
	return i
}

func nl(b *strings.Builder) {
	if b.Len() > 0 {
		b.WriteString("\n")
	}
}

// rpc dispatches the tool calls a running script makes.
type rpc struct {
	parent *Agent
	max    int

	mu sync.Mutex
	n  int
	// call serialises the calls themselves. The stub keeps one connection
	// behind a lock, but a script that opens a second one (a thread that
	// imports the module again, a subprocess) would otherwise reach
	// runNested from two goroutines at once — and the agent state it
	// touches, the provenance record and the per-turn mutation counters,
	// belongs to the serial half of the tool loop.
	call sync.Mutex
}

func (r *rpc) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.n
}

func (r *rpc) serve(ctx context.Context, ln net.Listener) {
	for {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		go r.handle(ctx, conn)
	}
}

func (r *rpc) handle(ctx context.Context, conn net.Conn) {
	defer conn.Close()
	dec := json.NewDecoder(bufio.NewReaderSize(conn, 1<<16))
	enc := json.NewEncoder(conn)
	for {
		var req struct {
			Tool  string          `json:"tool"`
			Input json.RawMessage `json:"input"`
		}
		if err := dec.Decode(&req); err != nil {
			return
		}
		out, errMsg := r.dispatch(ctx, req.Tool, req.Input)
		if err := enc.Encode(map[string]string{"output": out, "error": errMsg}); err != nil {
			return
		}
	}
}

// dispatch runs one call for the script. Everything is serialised: the
// agent's tool state is not built for a script fanning out, and a script
// that needs speed is saving round trips, not milliseconds.
func (r *rpc) dispatch(ctx context.Context, name string, input json.RawMessage) (string, string) {
	r.mu.Lock()
	if r.n >= r.max {
		r.mu.Unlock()
		return "", fmt.Sprintf("this script has used its %d tool calls; do the rest in another script", r.max)
	}
	r.n++
	n := r.n
	r.mu.Unlock()

	if scriptSkip[name] {
		return "", fmt.Sprintf("%s cannot be called from a script", name)
	}
	if len(input) == 0 {
		input = json.RawMessage("{}")
	}
	r.call.Lock()
	defer r.call.Unlock()
	out, isErr, _ := r.parent.runNested(ctx, fmt.Sprintf("script-%d", n), name, input)
	if isErr {
		return "", out
	}
	return out, ""
}

// stub is the janetcode_tools module the script imports.
func (s *Script) stub() string {
	var b strings.Builder
	b.WriteString(scriptStub)
	for _, t := range s.tools() {
		if t.primary == "" {
			fmt.Fprintf(&b, "\ndef %s(**kwargs):\n    return call(%q, **kwargs)\n", t.name, t.name)
			continue
		}
		// The first argument may be given positionally or by name; the
		// sentinel keeps an explicit None from overriding a keyword form.
		fmt.Fprintf(&b, "\ndef %s(%s=_unset, **kwargs):\n    if %s is not _unset:\n        kwargs[%q] = %s\n    return call(%q, **kwargs)\n",
			t.name, t.primary, t.primary, t.primary, t.primary, t.name)
	}
	return b.String()
}

const scriptStub = `"""Tools of the janetcode session this script is running in.

Every function takes the same arguments as the tool of that name and
returns its output as a string, raising ToolError when the call fails.
"""
import json, os, shlex, socket, threading, time

__all__ = ["call", "ToolError", "plain", "shell_quote", "retry"]

_lock = threading.Lock()
_fh = None


class _Unset:
    """Sentinel for an argument that was not given at all."""


_unset = _Unset()


class ToolError(Exception):
    """A tool call that did not succeed."""


def _channel():
    global _fh
    if _fh is None:
        s = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
        s.connect(os.environ["JANETCODE_RPC_SOCKET"])
        _fh = s.makefile("rwb")
    return _fh


def call(tool, **kwargs):
    """Call any tool by name, including one whose name is not an identifier."""
    payload = json.dumps({"tool": tool, "input": kwargs}) + "\n"
    with _lock:
        fh = _channel()
        fh.write(payload.encode())
        fh.flush()
        line = fh.readline()
    if not line:
        raise ToolError("the session closed the tool connection")
    reply = json.loads(line)
    if reply.get("error"):
        raise ToolError(reply["error"])
    return reply.get("output", "")


def plain(text):
    """Strip the line-number gutter read_file and grep put on every line.

    Those tools return lines as "<number>\\t<text>" so that you can cite a
    line. A script that is matching on the text itself wants this first:
    "func F()" does not start a line that reads "12\\tfunc F()".
    """
    out = []
    for line in str(text).split("\n"):
        n, tab, rest = line.partition("\t")
        out.append(rest if tab and n.strip().lstrip("-").isdigit() else line)
    return "\n".join(out)


def shell_quote(s):
    """Quote a string for safe interpolation into a shell command."""
    return shlex.quote(str(s))


def retry(fn, attempts=3, delay=1.0):
    """Call fn, retrying on any exception with a widening pause."""
    last = None
    for i in range(attempts):
        try:
            return fn()
        except Exception as e:  # noqa: BLE001 - the point is to retry anything
            last = e
            if i < attempts - 1:
                time.sleep(delay * (2 ** i))
    raise last

`
