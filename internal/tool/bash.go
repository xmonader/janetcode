package tool

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
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
	// Cache, when set, replays identical commands on an unchanged tree.
	Cache *ShellCache
	// Jobs, when set, enables background: true.
	Jobs *Jobs
}

func (Bash) Name() string { return "bash" }
func (Bash) Description() string {
	return "Run a shell command with " + DefaultShell().Name + " in the working directory. Returns combined stdout/stderr and the exit code. Use a timeout for long-running commands, or background: true for servers, watchers and long builds: the command keeps running, you get a job id at once and a note when it finishes (see the job tool). Do not use for reading or editing files; use the dedicated tools."
}
func (Bash) Mutating() bool { return true }
func (Bash) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{
"command":{"type":"string","description":"The command to run"},
"timeout_ms":{"type":"integer","description":"Timeout in milliseconds (default 120000, max 600000)"},
"background":{"type":"boolean","description":"Run detached and return immediately with a job id; output is collected and reported when the job ends"}},
"required":["command"],"additionalProperties":false}`)
}

func (t Bash) Run(ctx context.Context, input json.RawMessage) (string, error) {
	var in struct {
		Command    string `json:"command"`
		TimeoutMS  int    `json:"timeout_ms"`
		Background bool   `json:"background"`
	}
	if err := decode(input, &in); err != nil {
		return "", err
	}
	if strings.TrimSpace(in.Command) == "" {
		return "", fmt.Errorf("command is empty")
	}
	if risk, why := Classify(in.Command, t.Root); risk == RiskCatastrophic {
		return "", fmt.Errorf("refused: this command %s. janetcode never runs it, not even with auto-approve; do not retry it in another form", why)
	}
	if in.Background {
		if t.Jobs == nil {
			return "", fmt.Errorf("background jobs are not available in this session; run the command in the foreground with a timeout")
		}
		job, err := t.Jobs.Start(t.Root, in.Command)
		if err != nil {
			return "", err
		}
		t.Cache.invalidate()
		return fmt.Sprintf("Started background job %d (pid %d): %s\nOutput is collected in %s. You will be told when it finishes; the job tool shows output, waits, or kills it.", job.ID, job.Pid, in.Command, job.OutPath), nil
	}
	cacheKey := t.Cache.key(t.Root, in.Command)
	if out, age, ok := t.Cache.get(cacheKey); ok {
		return out + fmt.Sprintf("\n[cached: this exact command ran %s ago on an identical tree and nothing has changed since; append '%s' to force a rerun]", age.Round(time.Second), noCacheMarker), nil
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

	cmd := ShellCommand(ctx, in.Command)
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
	result = Truncate(result)
	t.Cache.put(cacheKey, result)
	return result, nil
}

// dangerPatterns flag commands whose damage is hard to undo. The prompt
// still decides; this only makes the risk visible.
var dangerPatterns = []struct {
	re   *regexp.Regexp
	note string
}{
	{regexp.MustCompile(`\brm\s+(-[a-zA-Z]*[rR][a-zA-Z]*f|-[a-zA-Z]*f[a-zA-Z]*[rR])\b`), "recursive force delete"},
	{regexp.MustCompile(`\brm\s+-[a-zA-Z]*[rR]\b`), "recursive delete"},
	{regexp.MustCompile(`\bgit\s+push\b.*(--force|-f\b|\+\S)`), "force push rewrites remote history"},
	{regexp.MustCompile(`\bgit\s+(reset\s+--hard|clean\s+-[a-zA-Z]*[fdx]|checkout\s+--\s|restore\s)`), "discards uncommitted changes"},
	{regexp.MustCompile(`\bgit\s+branch\s+-D\b`), "deletes a branch"},
	{regexp.MustCompile(`\b(sudo|doas)\b`), "runs with elevated privileges"},
	{regexp.MustCompile(`\b(mkfs|dd\s+if=|fdisk|parted)\b`), "raw disk operation"},
	{regexp.MustCompile(`\bchmod\s+(-R\s+)?[0-7]*777\b`), "world-writable permissions"},
	{regexp.MustCompile(`\bcurl\b.*\|\s*(ba)?sh\b`), "pipes a download into a shell"},
	{regexp.MustCompile(`\b(shutdown|reboot|halt|poweroff)\b`), "power operation"},
	{regexp.MustCompile(`\bkill\s+-9\s+-1\b|\bpkill\s+-9\b`), "kills processes broadly"},
	{regexp.MustCompile(`\bDROP\s+(TABLE|DATABASE)\b`), "drops database objects"},
	{regexp.MustCompile(`>\s*/dev/sd|>\s*/etc/`), "overwrites system files"},
}

// Guard implements Guard: commands that change files outside the workspace
// always ask, whatever the permission mode.
func (t Bash) Guard(input json.RawMessage) (bool, string) {
	var in struct {
		Command string `json:"command"`
	}
	if json.Unmarshal(input, &in) != nil {
		return false, ""
	}
	if risk, why := Classify(in.Command, t.Root); risk == RiskEscapes {
		return true, why
	}
	return false, ""
}

// Warn implements Warner.
func (t Bash) Warn(input json.RawMessage) string {
	var in struct {
		Command string `json:"command"`
	}
	if json.Unmarshal(input, &in) != nil {
		return ""
	}
	var notes []string
	if risk, why := Classify(in.Command, t.Root); risk != RiskOrdinary {
		notes = append(notes, why)
	}
	for _, d := range dangerPatterns {
		if d.re.MatchString(in.Command) {
			notes = append(notes, d.note)
		}
	}
	return strings.Join(notes, "; ")
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
