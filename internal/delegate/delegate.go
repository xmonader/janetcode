// Package delegate hands a task to an official agent CLI the user already
// has installed and is signed into — Claude Code or Codex — so the work runs
// on that CLI's own login and terms instead of a provider key janetcode
// holds. It is opt-in: with Config.Enabled false the tool refuses to run.
//
// janetcode does not read, copy or proxy the CLI's credentials. The child
// runs with the same scrubbed environment as a model-run bash command, so
// none of janetcode's provider keys reach it, and its own permission flags
// default to the safe, non-bypass mode of each CLI.
package delegate

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"

	"github.com/xmonader/janetcode/internal/procgroup"
	"github.com/xmonader/janetcode/internal/progress"
	"github.com/xmonader/janetcode/internal/shell"
	"github.com/xmonader/janetcode/internal/textfmt"
)

// DefaultTimeoutMinutes bounds one delegated run when none is configured.
const DefaultTimeoutMinutes = 30

// promptToken in a Spec's args is replaced by the task prompt. When no arg
// holds it the prompt is appended as the last argument.
const promptToken = "{prompt}"

// Spec is one delegate: how to start the CLI and how to read what it says.
type Spec struct {
	// Command is the executable, looked up on PATH. Empty means the
	// delegate's name.
	Command string `json:"command,omitempty"`
	// Args replace the built-in arguments for this delegate entirely, so
	// a user who sets them owns the permission mode. "{prompt}" marks
	// where the task goes.
	Args []string `json:"args,omitempty"`
	// Format says how stdout is read: "claude" (stream-json), "codex"
	// (--json events) or "text" (the whole of stdout is the answer).
	// Empty means the delegate's name if it is one of the first two,
	// else "text".
	Format string `json:"format,omitempty"`
}

// Config is the "delegates" section of the settings file.
type Config struct {
	// Enabled turns the delegate tool on. Off by default.
	Enabled bool `json:"enabled,omitempty"`
	// TimeoutMinutes bounds one run. Zero means DefaultTimeoutMinutes.
	TimeoutMinutes int `json:"timeout_minutes,omitempty"`
	// Agents adds to or overrides the built-in "claude" and "codex".
	Agents map[string]Spec `json:"agents,omitempty"`
}

// builtin is what "claude" and "codex" mean without configuration. Both
// run read-only: claude's default permission mode denies, in headless
// mode, whatever it would otherwise ask about, and codex's sandbox is
// read-only. Letting one edit is a deliberate choice made in args
// (--permission-mode acceptEdits, --sandbox workspace-write).
var builtin = map[string]Spec{
	"claude": {Command: "claude", Format: "claude", Args: []string{
		"-p", promptToken, "--output-format", "stream-json", "--verbose",
		"--permission-mode", "default", "--no-session-persistence"}},
	"codex": {Command: "codex", Format: "codex", Args: []string{
		"exec", "--sandbox", "read-only", "--json", "--ephemeral", promptToken}},
}

// Resolve merges the built-ins with the configured delegates. A configured
// entry replaces only the fields it sets.
func (c Config) Resolve() map[string]Spec {
	out := map[string]Spec{}
	for n, s := range builtin {
		out[n] = s
	}
	for n, s := range c.Agents {
		base := out[n]
		if s.Command != "" {
			base.Command = s.Command
		}
		if len(s.Args) > 0 {
			base.Args = s.Args
		}
		if s.Format != "" {
			base.Format = s.Format
		}
		if base.Command == "" {
			base.Command = n
		}
		if base.Format == "" {
			base.Format = "text"
		}
		out[n] = base
	}
	return out
}

// Tool is the delegate tool the model calls.
type Tool struct {
	Config Config
	// Dir returns the directory the CLI runs in: the session's current
	// working directory.
	Dir func() string
	// Env is the environment the child starts from before scrubbing;
	// nil means this process's.
	Env []string
}

func (t *Tool) Name() string { return "delegate" }

func (t *Tool) names() []string {
	var n []string
	for name := range t.Config.Resolve() {
		n = append(n, name)
	}
	sort.Strings(n)
	return n
}

func (t *Tool) Description() string {
	return "Hand a self-contained task to another agent CLI installed on this machine (" +
		strings.Join(t.names(), ", ") + "), which runs it on its own login in the current directory " +
		"and returns its final answer plus cost when it reports one. It starts read-only unless the user configured otherwise. " +
		"Use it when the user asked for that agent or for work it is better placed to do; give it the full context in prompt, since it cannot see this conversation."
}

func (t *Tool) Schema() json.RawMessage {
	enum, _ := json.Marshal(t.names())
	return json.RawMessage(`{"type":"object","properties":{
"agent":{"type":"string","enum":` + string(enum) + `,"description":"Which delegate to run"},
"prompt":{"type":"string","description":"The complete task, with all the context the agent needs"}},
"required":["agent","prompt"],"additionalProperties":false}`)
}

// Mutating is true: the child is an agent that runs commands, so a call
// is put to the user like any bash command.
func (t *Tool) Mutating() bool { return true }

// Result is what a finished run reports.
type Result struct {
	Answer       string
	CostUSD      float64
	HasCost      bool // the CLI reported a price
	InputTokens  int
	OutputTokens int
	Failure      string // the CLI's own account of why it failed, if it gave one
}

func (t *Tool) Run(ctx context.Context, input json.RawMessage) (string, error) {
	var in struct {
		Agent  string `json:"agent"`
		Prompt string `json:"prompt"`
	}
	if err := json.Unmarshal(input, &in); err != nil {
		return "", fmt.Errorf("bad input: %w", err)
	}
	if !t.Config.Enabled {
		return "", errors.New("delegates are disabled; the user can enable them with \"delegates\": {\"enabled\": true} in the settings file")
	}
	if strings.TrimSpace(in.Prompt) == "" {
		return "", errors.New("prompt must not be empty")
	}
	spec, ok := t.Config.Resolve()[in.Agent]
	if !ok {
		return "", fmt.Errorf("no delegate named %q (have %s)", in.Agent, strings.Join(t.names(), ", "))
	}
	bin, err := exec.LookPath(spec.Command)
	if err != nil {
		return "", fmt.Errorf("delegate %s: %q is not installed or not on PATH", in.Agent, spec.Command)
	}
	dir := ""
	if t.Dir != nil {
		dir = t.Dir()
	}
	minutes := t.Config.TimeoutMinutes
	if minutes <= 0 {
		minutes = DefaultTimeoutMinutes
	}
	return t.run(ctx, in.Agent, spec, bin, dir, in.Prompt, time.Duration(minutes)*time.Minute)
}

func (t *Tool) run(ctx context.Context, name string, spec Spec, bin, dir, prompt string, timeout time.Duration) (string, error) {
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(runCtx, bin, buildArgs(spec.Args, prompt)...)
	cmd.Dir = dir
	env := t.Env
	if env == nil {
		env = os.Environ()
	}
	cmd.Env = shell.ScrubEnv(env)
	procgroup.KillOnCancel(cmd)
	cmd.WaitDelay = 2 * time.Second
	var stderr cappedBuffer
	cmd.Stderr = &stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return "", err
	}
	start := time.Now()
	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("delegate %s: %w", name, err)
	}
	res := parse(spec.Format, stdout, func(line string) {
		progress.Report(ctx, name+": "+line)
	})
	waitErr := cmd.Wait()
	dur := time.Since(start).Round(100 * time.Millisecond)

	switch {
	case ctx.Err() != nil:
		return "", ctx.Err()
	case errors.Is(runCtx.Err(), context.DeadlineExceeded):
		return "", fmt.Errorf("delegate %s timed out after %s and was stopped%s", name, timeout, partial(res))
	case waitErr != nil:
		msg := res.Failure
		if msg == "" {
			msg = strings.TrimSpace(stderr.String())
		}
		return "", fmt.Errorf("delegate %s failed (%v): %s%s", name, waitErr, textfmt.Clip(msg, 2000), partial(res))
	case res.Failure != "":
		return "", fmt.Errorf("delegate %s reported an error: %s", name, textfmt.Clip(res.Failure, 2000))
	case strings.TrimSpace(res.Answer) == "":
		return "", fmt.Errorf("delegate %s finished without an answer; stderr: %s", name, textfmt.Clip(strings.TrimSpace(stderr.String()), 500))
	}
	return strings.TrimSpace(res.Answer) + "\n\n" + footer(name, res, dur), nil
}

func partial(r Result) string {
	if strings.TrimSpace(r.Answer) == "" {
		return ""
	}
	return "\npartial answer so far:\n" + textfmt.Clip(r.Answer, 4000)
}

func footer(name string, r Result, d time.Duration) string {
	parts := []string{"delegate " + name, d.String()}
	if r.HasCost {
		parts = append(parts, fmt.Sprintf("cost $%.4f", r.CostUSD))
	}
	if r.InputTokens > 0 || r.OutputTokens > 0 {
		parts = append(parts, fmt.Sprintf("%d in / %d out tokens", r.InputTokens, r.OutputTokens))
	}
	return "[" + strings.Join(parts, " · ") + "]"
}

func buildArgs(tmpl []string, prompt string) []string {
	out := make([]string, 0, len(tmpl)+1)
	used := false
	for _, a := range tmpl {
		if strings.Contains(a, promptToken) {
			a = strings.ReplaceAll(a, promptToken, prompt)
			used = true
		}
		out = append(out, a)
	}
	if !used {
		out = append(out, prompt)
	}
	return out
}

// parse reads the CLI's stdout to the end, calling progress for what it is
// seen doing, and returns what it concluded.
func parse(format string, r io.Reader, progress func(string)) Result {
	var res Result
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	var text bytes.Buffer
	for sc.Scan() {
		line := sc.Bytes()
		switch format {
		case "claude":
			parseClaudeLine(line, &res, progress)
		case "codex":
			parseCodexLine(line, &res, progress)
		default:
			text.Write(line)
			text.WriteByte('\n')
			if s := strings.TrimSpace(string(line)); s != "" {
				progress(textfmt.Clip(s, 100))
			}
		}
	}
	if format != "claude" && format != "codex" {
		res.Answer = text.String()
	}
	// Drain so a CLI that wrote a line past the scanner's limit is not
	// left blocked on a full pipe until the timeout.
	_, _ = io.Copy(io.Discard, r)
	return res
}

func parseClaudeLine(line []byte, res *Result, progress func(string)) {
	var ev struct {
		Type    string  `json:"type"`
		IsError bool    `json:"is_error"`
		Result  string  `json:"result"`
		Cost    float64 `json:"total_cost_usd"`
		Usage   struct {
			In         int `json:"input_tokens"`
			CacheRead  int `json:"cache_read_input_tokens"`
			CacheWrite int `json:"cache_creation_input_tokens"`
			Out        int `json:"output_tokens"`
		} `json:"usage"`
		Message struct {
			Content []struct {
				Type  string          `json:"type"`
				Text  string          `json:"text"`
				Name  string          `json:"name"`
				Input json.RawMessage `json:"input"`
			} `json:"content"`
		} `json:"message"`
	}
	if json.Unmarshal(line, &ev) != nil {
		return
	}
	switch ev.Type {
	case "assistant":
		for _, b := range ev.Message.Content {
			switch b.Type {
			case "tool_use":
				progress(b.Name + " " + textfmt.Clip(string(b.Input), 80))
			case "text":
				if s := strings.TrimSpace(b.Text); s != "" {
					progress(textfmt.Clip(firstLine(s), 100))
				}
			}
		}
	case "result":
		res.HasCost = ev.Cost > 0
		res.CostUSD = ev.Cost
		res.InputTokens = ev.Usage.In + ev.Usage.CacheRead + ev.Usage.CacheWrite
		res.OutputTokens = ev.Usage.Out
		if ev.IsError {
			res.Failure = ev.Result
			if res.Failure == "" {
				res.Failure = "the CLI reported an error"
			}
		} else {
			res.Answer = ev.Result
		}
	}
}

func parseCodexLine(line []byte, res *Result, progress func(string)) {
	var ev struct {
		Type  string `json:"type"`
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
		Item struct {
			Type    string `json:"type"`
			Text    string `json:"text"`
			Command string `json:"command"`
		} `json:"item"`
		Usage struct {
			In  int `json:"input_tokens"`
			Out int `json:"output_tokens"`
		} `json:"usage"`
	}
	if json.Unmarshal(line, &ev) != nil {
		return
	}
	switch ev.Type {
	case "item.started", "item.completed":
		switch ev.Item.Type {
		case "agent_message":
			if ev.Type == "item.completed" {
				res.Answer = ev.Item.Text
				progress(textfmt.Clip(firstLine(strings.TrimSpace(ev.Item.Text)), 100))
			}
		case "command_execution":
			if ev.Type == "item.started" {
				progress("run " + textfmt.Clip(ev.Item.Command, 90))
			}
		}
	case "turn.completed":
		res.InputTokens += ev.Usage.In
		res.OutputTokens += ev.Usage.Out
	case "turn.failed":
		res.Failure = ev.Error.Message
		if res.Failure == "" {
			res.Failure = "the turn failed"
		}
	}
	// Item-type "error" events also carry warnings (a deprecated config
	// key, say) on runs that go on to succeed, so they are not a failure
	// on their own; a non-zero exit or turn.failed is.
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// cappedBuffer keeps the tail of what is written, so a chatty stderr
// cannot grow without bound.
type cappedBuffer struct{ b []byte }

const stderrCap = 16 * 1024

func (c *cappedBuffer) Write(p []byte) (int, error) {
	c.b = append(c.b, p...)
	if len(c.b) > stderrCap {
		c.b = c.b[len(c.b)-stderrCap:]
	}
	return len(p), nil
}

func (c *cappedBuffer) String() string { return string(c.b) }
