package tool

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// Bash runs a shell command in the working directory.
type Bash struct {
	Root string
	// DefaultTimeout applies when the model gives none. Zero means 2 minutes.
	DefaultTimeout time.Duration
	// MaxTimeout caps what the model may request. Zero means 10 minutes.
	MaxTimeout time.Duration
}

func (Bash) Name() string { return "bash" }
func (Bash) Description() string {
	return "Run a shell command with bash in the working directory. Returns combined stdout/stderr and the exit code. Use a timeout for long-running commands. Do not use for reading or editing files; use the dedicated tools."
}
func (Bash) Mutating() bool { return true }
func (Bash) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{
"command":{"type":"string","description":"The command to run"},
"timeout_ms":{"type":"integer","description":"Timeout in milliseconds (default 120000, max 600000)"}},
"required":["command"],"additionalProperties":false}`)
}

func (t Bash) Run(ctx context.Context, input json.RawMessage) (string, error) {
	var in struct {
		Command   string `json:"command"`
		TimeoutMS int    `json:"timeout_ms"`
	}
	if err := decode(input, &in); err != nil {
		return "", err
	}
	if strings.TrimSpace(in.Command) == "" {
		return "", fmt.Errorf("command is empty")
	}
	def, max := t.DefaultTimeout, t.MaxTimeout
	if def == 0 {
		def = 2 * time.Minute
	}
	if max == 0 {
		max = 10 * time.Minute
	}
	timeout := def
	if in.TimeoutMS > 0 {
		timeout = time.Duration(in.TimeoutMS) * time.Millisecond
	}
	if timeout > max {
		timeout = max
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "bash", "-c", in.Command)
	cmd.Dir = t.Root
	cmd.Env = scrubEnv(os.Environ())
	setProcessGroup(cmd)
	cmd.WaitDelay = 2 * time.Second
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	cmd.Stdin = nil

	err := cmd.Run()
	result := out.String()
	exit := 0
	if err != nil {
		var ee *exec.ExitError
		switch {
		case errors.Is(ctx.Err(), context.DeadlineExceeded):
			return Truncate(result + fmt.Sprintf("\n[command timed out after %s and was killed]", timeout)), nil
		case errors.Is(ctx.Err(), context.Canceled):
			return "", ctx.Err()
		case errors.As(err, &ee):
			exit = ee.ExitCode()
			if exit == -1 {
				exit = signalExitCode(ee)
			}
		default:
			return "", fmt.Errorf("start bash: %w", err)
		}
	}
	if result == "" {
		result = "(no output)"
	}
	if exit != 0 {
		result += fmt.Sprintf("\n[exit code %d]", exit)
	}
	return Truncate(result), nil
}

// secretEnv lists variables that model-run commands must never see: the
// agent's own credentials. Anything else the user exported stays available.
var secretEnv = map[string]bool{
	"ANTHROPIC_API_KEY":    true,
	"ANTHROPIC_AUTH_TOKEN": true,
	"OPENAI_API_KEY":       true,
}

// nonInteractiveEnv stops common tools from waiting on a human: no pagers,
// no editors, no credential prompts. Commands get EOF on stdin regardless.
var nonInteractiveEnv = []string{
	"PAGER=cat",
	"GIT_PAGER=cat",
	"GIT_EDITOR=true",
	"GIT_TERMINAL_PROMPT=0",
	"DEBIAN_FRONTEND=noninteractive",
	"CI=true",
	"NO_COLOR=1",
}

func scrubEnv(env []string) []string {
	out := make([]string, 0, len(env)+len(nonInteractiveEnv))
	forced := map[string]bool{}
	for _, kv := range nonInteractiveEnv {
		name, _, _ := strings.Cut(kv, "=")
		forced[name] = true
	}
	for _, kv := range env {
		name, _, _ := strings.Cut(kv, "=")
		if secretEnv[name] || forced[name] {
			continue
		}
		out = append(out, kv)
	}
	return append(out, nonInteractiveEnv...)
}
