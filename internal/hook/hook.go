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
	"github.com/xmonader/janetcode/internal/tool"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
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
}

// Config maps event names to hooks, the shape used in config.json under
// "hooks" and in <project>/.janetcode/hooks.json.
type Config map[string][]Hook

// ProjectFile is the per-project hook list, relative to the project root.
const ProjectFile = ".janetcode/hooks.json"

// Validate rejects unknown events, bad regexps and empty commands.
func (c Config) Validate() error {
	for ev, hooks := range c {
		known := false
		for _, e := range Events {
			known = known || e == ev
		}
		if !known {
			return fmt.Errorf("hooks: unknown event %q (want one of %s)", ev, strings.Join(Events, ", "))
		}
		for i, h := range hooks {
			if strings.TrimSpace(h.Command) == "" {
				return fmt.Errorf("hooks: %s[%d] has no command", ev, i)
			}
			if h.Match != "" {
				if _, err := regexp.Compile(h.Match); err != nil {
					return fmt.Errorf("hooks: %s[%d] match: %v", ev, i, err)
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
		return nil, "", nil
	}
	if err != nil {
		return nil, "", err
	}
	var c Config
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, "", fmt.Errorf("%s: %w", ProjectFile, err)
	}
	if err := c.Validate(); err != nil {
		return nil, "", fmt.Errorf("%s: %w", ProjectFile, err)
	}
	sum := sha256.Sum256(data)
	return c, hex.EncodeToString(sum[:]), nil
}

// Merge returns a config with the hooks of both, a's first.
func Merge(a, b Config) Config {
	out := Config{}
	for _, c := range []Config{a, b} {
		for ev, hs := range c {
			out[ev] = append(out[ev], hs...)
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

// networkCommand matches the obvious ways a shell command reaches off the
// machine. It is not a sandbox: a project hook can still run a script that
// does anything. It is the same defence in depth as the self-modification
// guard — the obvious move is the one that is stopped and named.
var networkCommand = regexp.MustCompile(`(?i)\b(curl|wget|nc|ncat|netcat|telnet|ssh|scp|sftp|rsync|ftp)\b|https?://`)

// projectTimeoutCap bounds how long one project hook may hold up a turn.
const projectTimeoutCap = 60

// Restrict returns the project hooks that a workspace scope is allowed to
// contribute, and a note for each one dropped or trimmed. A nil config
// gives nothing back and no notes.
func Restrict(project Config) (Config, []string) {
	if len(project) == 0 {
		return project, nil
	}
	out := Config{}
	var notes []string
	for ev, hooks := range project {
		for i, h := range hooks {
			where := fmt.Sprintf("%s %s[%d] (%s)", ProjectFile, ev, i, firstWords(h.Command))
			if networkCommand.MatchString(h.Command) {
				notes = append(notes, where+" is not run: a project hook may not reach the network. Copy it into your own config if you want it.")
				continue
			}
			if h.Timeout > projectTimeoutCap {
				notes = append(notes, fmt.Sprintf("%s asked for a %ds timeout; a project hook is capped at %ds.", where, h.Timeout, projectTimeoutCap))
				h.Timeout = projectTimeoutCap
			}
			out[ev] = append(out[ev], h)
		}
	}
	return out, notes
}

// MergeScoped is Merge with the project side held to what a workspace
// scope may contribute. It returns the combined config and the notes
// explaining anything the project asked for and did not get.
func MergeScoped(user, project Config) (Config, []string) {
	kept, notes := Restrict(project)
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
	// on Windows by default, see tool.DefaultShell).
	Shell string
}

// Has reports whether any hook is bound to the event.
func (r *Runner) Has(event string) bool { return r != nil && len(r.Config[event]) > 0 }

// Run executes the hooks of in.Event in order. A deny stops the chain;
// rewrites chain, so each hook sees the previous hook's input.
func (r *Runner) Run(ctx context.Context, in Input) Result {
	res := Result{Input: in.Input}
	if r == nil {
		return res
	}
	for i, h := range r.Config[in.Event] {
		if h.Match != "" && in.Tool != "" {
			if ok, _ := regexp.MatchString(h.Match, in.Tool); !ok {
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

type execOut struct{ stdout, stderr string }

func (r *Runner) exec(ctx context.Context, h Hook, in Input) (execOut, int, error) {
	timeout := time.Duration(h.Timeout) * time.Second
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
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
		cmd = tool.ShellCommand(ctx, h.Command)
	}
	cmd.Dir = r.Cwd
	cmd.Stdin = bytes.NewReader(payload)
	cmd.Env = append(os.Environ(),
		"JANETCODE_EVENT="+in.Event,
		"JANETCODE_TOOL="+in.Tool,
		"JANETCODE_CWD="+in.Cwd,
	)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
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
