// Package hook runs user-supplied commands at points in the agent loop:
// before and after a tool call, before compaction, and when the model
// wants to stop. A hook can deny or rewrite a tool call and add notes the
// model sees. Hooks are fail-open: a crash, timeout or unexpected exit
// code is reported and otherwise ignored, so a broken hook cannot wedge a
// session; only exit code 2 (or a JSON deny) blocks.
package hook

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/xmonader/janetcode/internal/shell"
)

// Event names.
const (
	PreTool    = "pre_tool"    // before a tool runs; may deny or rewrite input
	PostTool   = "post_tool"   // after a tool ran; may add a note to the result
	PreCompact = "pre_compact" // before the history is summarised
	Stop       = "stop"        // the model ended its turn; may send it back to work
)

// Events lists every event name, for validation and docs.
var Events = []string{PreTool, PostTool, PreCompact, Stop}

// Hook is one command bound to an event.
type Hook struct {
	// Match is a regexp on the tool name for tool events; empty matches all.
	Match string `json:"match,omitempty"`
	// Command is run through the shell with the event JSON on stdin.
	Command string `json:"command"`
	// Timeout in seconds; zero means 10.
	Timeout int `json:"timeout,omitempty"`
	// Network is the hook's own word for whether it reaches off the
	// machine. It is what decides whether a project hook may run (see
	// Restrict); left out, the command text is guessed at instead. A
	// pointer so that "not said" and "said no" are different answers.
	Network *bool `json:"network,omitempty"`
}

// ReachesNetwork reports whether the hook goes off the machine: what the
// hook says about itself where it says anything, and a guess from the
// command text where it does not.
func (h Hook) ReachesNetwork() bool {
	if h.Network != nil {
		return *h.Network
	}
	return networkCommand.MatchString(h.Command)
}

// Hooks maps event names to the hooks bound to them.
type Hooks map[string][]Hook

// Limits bound what hooks may cost a session. Every hook is a shell
// command the harness runs on the critical path of a tool call, so the
// bounds were always there -- they were constants nobody could reach.
// A user who writes slow hooks, or many of them, is entitled to say so;
// a user surprised by a hook that hangs a turn for ten seconds is
// entitled to lower it. Zero in any field means the default.
type Limits struct {
	// Timeout is how many seconds a hook that names no timeout of its
	// own may run.
	Timeout int `json:"timeout,omitempty"`
	// ProjectTimeout caps what a hook from the project file may ask for.
	// The project's own limits are never read, so a repository cannot
	// raise its own ceiling.
	ProjectTimeout int `json:"project_timeout,omitempty"`
	// Output is how many bytes of a hook's stdout and stderr are kept.
	// A hook's note is pasted into the conversation, so an unbounded one
	// is an unbounded context bill.
	Output int `json:"output,omitempty"`
	// PerEvent is how many hooks may run for one event. Hooks run in
	// order on every tool call, so a long list is a long turn.
	PerEvent int `json:"per_event,omitempty"`
}

// Limit defaults. DefaultTimeout and DefaultProjectTimeout are what the
// package enforced as constants before the limits were configurable.
const (
	DefaultTimeout        = 10
	DefaultProjectTimeout = 60
	DefaultOutput         = 64 * 1024
	DefaultPerEvent       = 20
)

// WithDefaults fills the zero fields.
func (l Limits) WithDefaults() Limits {
	if l.Timeout == 0 {
		l.Timeout = DefaultTimeout
	}
	if l.ProjectTimeout == 0 {
		l.ProjectTimeout = DefaultProjectTimeout
	}
	if l.Output == 0 {
		l.Output = DefaultOutput
	}
	if l.PerEvent == 0 {
		l.PerEvent = DefaultPerEvent
	}
	return l
}

// Validate rejects limits that cannot mean anything.
func (l Limits) Validate() error {
	for _, f := range []struct {
		name string
		v    int
	}{{"timeout", l.Timeout}, {"project_timeout", l.ProjectTimeout}, {"output", l.Output}, {"per_event", l.PerEvent}} {
		if f.v < 0 {
			return fmt.Errorf("hooks: limits.%s is %d: it cannot be negative (0 means the default)", f.name, f.v)
		}
	}
	return nil
}

// Config is the "hooks" object: the hooks by event name, plus the
// "limits" object bounding them. It is the shape used in config.json
// under "hooks" and in <project>/.janetcode/hooks.json.
type Config struct {
	Hooks  Hooks
	Limits Limits
}

// limitsKey is the one key of the hooks object that is not an event.
const limitsKey = "limits"

// For returns the hooks bound to an event.
func (c Config) For(event string) []Hook { return c.Hooks[event] }

// Len is how many hooks are configured in total. It is what a caller
// means by "are there any hooks here".
func (c Config) Len() int {
	n := 0
	for _, hs := range c.Hooks {
		n += len(hs)
	}
	return n
}

// UnmarshalJSON reads the object, taking "limits" as the limits and every
// other key as an event.
func (c *Config) UnmarshalJSON(data []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	c.Hooks = Hooks{}
	for k, v := range raw {
		if k == limitsKey {
			dec := json.NewDecoder(bytes.NewReader(v))
			dec.DisallowUnknownFields()
			if err := dec.Decode(&c.Limits); err != nil {
				return fmt.Errorf("hooks: limits: %w", err)
			}
			continue
		}
		var hs []Hook
		if err := json.Unmarshal(v, &hs); err != nil {
			return fmt.Errorf("hooks: %s: %w", k, err)
		}
		c.Hooks[k] = hs
	}
	return nil
}

// MarshalJSON writes the same shape back.
func (c Config) MarshalJSON() ([]byte, error) {
	raw := map[string]any{}
	for k, hs := range c.Hooks {
		raw[k] = hs
	}
	if c.Limits != (Limits{}) {
		raw[limitsKey] = c.Limits
	}
	return json.Marshal(raw)
}

// ProjectFile is the per-project hook list, relative to the project root.
const ProjectFile = ".janetcode/hooks.json"

// Validate rejects unknown events, bad regexps, empty commands and
// limits that cannot mean anything.
func (c Config) Validate() error {
	if err := c.Limits.Validate(); err != nil {
		return err
	}
	for ev, hooks := range c.Hooks {
		known := false
		for _, e := range Events {
			known = known || e == ev
		}
		if !known {
			return fmt.Errorf("hooks: unknown event %q (want one of %s, or %q)", ev, strings.Join(Events, ", "), limitsKey)
		}
		for i, h := range hooks {
			if strings.TrimSpace(h.Command) == "" {
				return fmt.Errorf("hooks: %s[%d] has no command", ev, i)
			}
			if h.Match != "" {
				if _, err := regexp.Compile(h.Match); err != nil {
					return fmt.Errorf("hooks: %s[%d] match: %w", ev, i, err)
				}
			}
		}
	}
	return nil
}

// LoadProject reads the project hook file. A missing file gives an empty
// config and empty hash. The hash identifies the exact content that was
// approved, like the MCP project file.
func LoadProject(root string) (Config, string, error) {
	data, err := os.ReadFile(filepath.Join(root, ProjectFile))
	if errors.Is(err, os.ErrNotExist) {
		return Config{}, "", nil
	}
	if err != nil {
		return Config{}, "", err
	}
	var c Config
	if err := json.Unmarshal(data, &c); err != nil {
		return Config{}, "", fmt.Errorf("%s: %w", ProjectFile, err)
	}
	if err := c.Validate(); err != nil {
		return Config{}, "", fmt.Errorf("%s: %w", ProjectFile, err)
	}
	sum := sha256.Sum256(data)
	return c, hex.EncodeToString(sum[:]), nil
}

// Merge returns a config with the hooks of both, a's first. The limits
// are a's: they are the user's, and b is where a project's hooks come
// from. A repository that could raise the ceiling on its own hooks would
// have no ceiling.
func Merge(a, b Config) Config {
	out := Config{Hooks: Hooks{}, Limits: a.Limits}
	for _, c := range []Config{a, b} {
		for ev, hs := range c.Hooks {
			out.Hooks[ev] = append(out.Hooks[ev], hs...)
		}
	}
	return out
}

// A hook runs a shell command on the user's machine every time the agent
// touches a tool, and the project's hooks come from the repository rather
// than from the user. Trusting a project's hook file is a decision about
// this checkout, not a decision to hand the repository every power the
// user has: the narrower scope must not be able to widen what the wider
// one allows.
//
// So a project hook may not reach the network. The user's own config may
// — it is the user's config — and a project that needs one says so, and
// the user copies it into their own file, which is a deliberate act.
//
// A hook says what it is with "network": true or false, and that is what
// decides. The regexp below is only for hooks that say nothing, which is
// every hook written before the key existed: guessing from command text
// refuses `grep -n "https://" .` for containing a URL and lets through a
// python one-liner that opens a socket, so a hook that knows the answer
// should be believed over a guess.

// networkCommand matches the obvious ways a shell command reaches off the
// machine. It is not a sandbox: a project hook can still run a script that
// does anything. It is the same defence in depth as the self-modification
// guard — the obvious move is the one that is stopped and named.
var networkCommand = regexp.MustCompile(`(?i)\b(curl|wget|nc|ncat|netcat|telnet|ssh|scp|sftp|rsync|ftp)\b|https?://`)

// Restrict returns the project hooks that a workspace scope is allowed to
// contribute, and a note for each one dropped or trimmed. The cap comes
// from the user's limits, never the project's. An empty config gives
// nothing back and no notes.
func Restrict(project Config, lim Limits) (Config, []string) {
	if project.Len() == 0 {
		return project, nil
	}
	maxSecs := lim.WithDefaults().ProjectTimeout
	out := Config{Hooks: Hooks{}}
	var notes []string
	for ev, hooks := range project.Hooks {
		for i, h := range hooks {
			where := fmt.Sprintf("%s %s[%d] (%s)", ProjectFile, ev, i, firstWords(h.Command))
			if h.ReachesNetwork() {
				why := " is not run: a project hook may not reach the network. Copy it into your own config if you want it."
				if h.Network == nil {
					why += ` If it does not reach the network, say so with "network": false.`
				}
				notes = append(notes, where+why)
				continue
			}
			if h.Timeout > maxSecs {
				notes = append(notes, fmt.Sprintf("%s asked for a %ds timeout; a project hook is capped at %ds.", where, h.Timeout, maxSecs))
				h.Timeout = maxSecs
			}
			out.Hooks[ev] = append(out.Hooks[ev], h)
		}
	}
	if project.Limits != (Limits{}) {
		notes = append(notes, ProjectFile+" sets hooks.limits; a project cannot raise its own limits, so the user's apply.")
	}
	return out, notes
}

// MergeScoped is Merge with the project side held to what a workspace
// scope may contribute. It returns the combined config and the notes
// explaining anything the project asked for and did not get.
func MergeScoped(user, project Config) (Config, []string) {
	kept, notes := Restrict(project, user.Limits)
	return Merge(user, kept), notes
}

// Input is what a hook reads on stdin, as JSON.
type Input struct {
	Event   string          `json:"event"`
	Cwd     string          `json:"cwd"`
	Tool    string          `json:"tool,omitempty"`
	Input   json.RawMessage `json:"input,omitempty"`
	Output  string          `json:"output,omitempty"`
	IsError bool            `json:"is_error,omitempty"`
	// Text is the model's final message for stop events.
	Text string `json:"text,omitempty"`
	// Tokens is the estimated context size for pre_compact events.
	Tokens int `json:"tokens,omitempty"`
}

// Output is what a hook may print on stdout, as JSON. Plain text on
// stdout is taken as a note.
type Output struct {
	// Decision "deny" blocks the tool call (pre_tool) with Reason; "allow"
	// runs it without asking the user (guarded calls still ask).
	Decision string `json:"decision,omitempty"`
	Reason   string `json:"reason,omitempty"`
	// Input replaces the tool input (pre_tool).
	Input json.RawMessage `json:"input,omitempty"`
	// Note is appended to the tool result (post_tool) or shown to the
	// model as context (pre_tool, stop).
	Note string `json:"note,omitempty"`
	// Continue sends the model back to work with this text (stop).
	Continue string `json:"continue,omitempty"`
}

// Result is the combined verdict of every hook that ran for an event.
type Result struct {
	Denied bool
	Reason string
	// Allowed means a pre_tool hook approved the call on the user's behalf.
	Allowed bool
	// Input is the final tool input after rewrites (pre_tool).
	Input json.RawMessage
	// Notes collects notes from every hook.
	Notes []string
	// Continue is the text to feed back on a stop event, or "".
	Continue string
}

// Runner executes hooks. Log receives one line per failure that was
// ignored (fail-open); nil discards them.
type Runner struct {
	Config Config
	Cwd    string
	Log    func(string)
	// Shell overrides the shell used with -c (bash on Unix and PowerShell
	// on Windows by default, see shell.Default).
	Shell string
}

// Has reports whether any hook is bound to the event.
func (r *Runner) Has(event string) bool { return r != nil && len(r.Config.For(event)) > 0 }

// Run executes the hooks of in.Event in order. A deny stops the chain;
// rewrites chain, so each hook sees the previous hook's input.
func (r *Runner) Run(ctx context.Context, in Input) Result {
	res := Result{Input: in.Input}
	if r == nil {
		return res
	}
	lim := r.Config.Limits.WithDefaults()
	for i, h := range r.Config.For(in.Event) {
		if i >= lim.PerEvent {
			r.logf("%s: only the first %d hooks ran (hooks.limits.per_event)", in.Event, lim.PerEvent)
			break
		}
		if h.Match != "" && in.Tool != "" {
			re, err := matchRe(h.Match)
			if err != nil {
				// Validate rejects these when the file is loaded, so this
				// is a hook that reached the runner another way. Saying so
				// beats a hook that silently never runs.
				r.logf("%s hook %d (%s) ignored: match %q is not a valid regexp: %v", in.Event, i+1, firstWords(h.Command), h.Match, err)
				continue
			}
			if !re.MatchString(in.Tool) {
				continue
			}
		}
		in.Input = res.Input
		out, exit, err := r.exec(ctx, h, in)
		label := fmt.Sprintf("%s hook %d (%s)", in.Event, i+1, firstWords(h.Command))
		switch {
		case ctx.Err() != nil:
			return res
		case exit == 2:
			reason := strings.TrimSpace(out.stderr)
			if reason == "" {
				reason = "blocked by " + label
			}
			if in.Event == Stop {
				res.Continue = reason
				return res
			}
			res.Denied, res.Reason = true, reason
			return res
		case err != nil || exit != 0:
			msg := strings.TrimSpace(out.stderr)
			if err != nil {
				msg = err.Error()
			}
			r.logf("%s ignored: exit %d: %s", label, exit, msg)
			continue
		}
		var o Output
		text := strings.TrimSpace(out.stdout)
		if text == "" {
			continue
		}
		if err := json.Unmarshal([]byte(text), &o); err != nil {
			o.Note = text
		}
		if o.Decision == "deny" || o.Decision == "block" {
			if o.Reason == "" {
				o.Reason = "blocked by " + label
			}
			if in.Event == Stop {
				res.Continue = o.Reason
				return res
			}
			res.Denied, res.Reason = true, o.Reason
			return res
		}
		if o.Decision == "allow" || o.Decision == "approve" {
			res.Allowed = true
		}
		if len(o.Input) > 0 && in.Event == PreTool {
			if !json.Valid(o.Input) {
				r.logf("%s ignored: rewritten input is not valid JSON", label)
			} else {
				res.Input = o.Input
			}
		}
		if o.Note != "" {
			res.Notes = append(res.Notes, o.Note)
		}
		if o.Continue != "" && in.Event == Stop {
			res.Continue = o.Continue
			return res
		}
	}
	return res
}

// matchRe compiles a hook's tool-name pattern once. Hooks run on every
// tool call, and regexp.MatchString recompiles the pattern each time.
var matchRe = func() func(string) (*regexp.Regexp, error) {
	var mu sync.Mutex
	cache := map[string]*regexp.Regexp{}
	return func(pat string) (*regexp.Regexp, error) {
		mu.Lock()
		defer mu.Unlock()
		if re, ok := cache[pat]; ok {
			return re, nil
		}
		re, err := regexp.Compile(pat)
		if err != nil {
			return nil, err
		}
		cache[pat] = re
		return re, nil
	}
}()

type execOut struct{ stdout, stderr string }

func (r *Runner) exec(ctx context.Context, h Hook, in Input) (execOut, int, error) {
	lim := r.Config.Limits.WithDefaults()
	secs := h.Timeout
	if secs <= 0 {
		secs = lim.Timeout
	}
	timeout := time.Duration(secs) * time.Second
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	payload, err := json.Marshal(in)
	if err != nil {
		return execOut{}, -1, err
	}
	var cmd *exec.Cmd
	if r.Shell != "" {
		cmd = exec.CommandContext(ctx, r.Shell, "-c", h.Command)
	} else {
		cmd = shell.Command(ctx, h.Command)
	}
	// A hook runs on the critical path of a tool call, so its timeout has
	// to be a real bound. Without a process group the context kills only
	// the shell, and Wait still blocks on the output pipes for as long as
	// any grandchild holds them open — a hook that starts a daemon hangs
	// the turn for ever. Supervise puts the whole tree in one group and
	// bounds the wait on the pipes after the kill.
	shell.Supervise(cmd)
	cmd.Dir = r.Cwd
	cmd.Stdin = bytes.NewReader(payload)
	// The environment is scrubbed of the agent's credentials: a project
	// hook is project-controlled code on the model's critical path, and
	// anything it prints (stdout becomes a conversation note) must not be
	// able to smuggle a key back into the context.
	cmd.Env = append(shell.ScrubEnv(os.Environ()),
		"JANETCODE_EVENT="+in.Event,
		"JANETCODE_TOOL="+in.Tool,
		"JANETCODE_CWD="+in.Cwd,
	)
	// A hook's stdout becomes a note in the conversation and its stderr a
	// denial reason, so neither may be unbounded: a hook that cats a log
	// would otherwise be paid for by every following model call.
	stdout, stderr := &capped{max: lim.Output}, &capped{max: lim.Output}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	err = cmd.Run()
	out := execOut{stdout.String(), stderr.String()}
	if ctx.Err() != nil {
		return out, -1, fmt.Errorf("timed out after %s", timeout)
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return out, ee.ExitCode(), nil
	}
	if err != nil {
		return out, -1, err
	}
	return out, 0, nil
}

// capped is a writer that keeps the first max bytes and counts the rest,
// so a hook cannot be stopped by writing but cannot flood the context
// either.
type capped struct {
	max     int
	buf     bytes.Buffer
	dropped int
}

func (c *capped) Write(p []byte) (int, error) {
	if room := c.max - c.buf.Len(); room > 0 {
		if room > len(p) {
			room = len(p)
		} else {
			// Stop on a rune boundary: the kept text is pasted into the
			// conversation, and half a character there is a replacement
			// char the model reads as the hook's output.
			for room > 0 && p[room]&0xC0 == 0x80 {
				room--
			}
		}
		c.buf.Write(p[:room])
		c.dropped += len(p) - room
	} else {
		c.dropped += len(p)
	}
	return len(p), nil
}

func (c *capped) String() string {
	if c.dropped == 0 {
		return c.buf.String()
	}
	return fmt.Sprintf("%s\n[%d more bytes from this hook were dropped: hooks.limits.output is %d]", c.buf.String(), c.dropped, c.max)
}

func (r *Runner) logf(format string, args ...any) {
	if r.Log != nil {
		r.Log(fmt.Sprintf(format, args...))
	}
}

func firstWords(s string) string {
	f := strings.Fields(s)
	if len(f) > 3 {
		f = f[:3]
	}
	return strings.Join(f, " ")
}
