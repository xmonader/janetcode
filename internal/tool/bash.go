package tool

import (
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
	// Running, when set with Jobs, lets a foreground command be promoted
	// to a background job while it runs instead of being waited out or
	// killed. See Running.Promote.
	Running *Running
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
	timeout := clampTimeout(in.TimeoutMS, def, max)
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// The command runs on a context of its own rather than on the turn's.
	// os/exec kills a command as soon as the context it was started with
	// is done, and the turn's context is done the moment this call
	// returns — which, for a command handed to the jobs registry, is
	// while it is still meant to be running. Cancellation and the timeout
	// reach the command through killCmd below instead, and promotion
	// simply stops forwarding them.
	runCtx, killCmd := context.WithCancel(context.WithoutCancel(ctx))
	promoted := false
	defer func() {
		if !promoted {
			killCmd()
		}
	}()

	cmd := ShellCommand(runCtx, in.Command)
	cmd.Dir = t.Root
	cmd.Env = scrubEnv(os.Environ())
	setProcessGroup(cmd)
	cmd.WaitDelay = 2 * time.Second
	out := &swapBuffer{}
	cmd.Stdout = out
	cmd.Stderr = out
	cmd.Stdin = nil

	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("start bash: %w", err)
	}
	// The wait runs in its own goroutine so the command can be handed to
	// the jobs registry while it is still running: the same channel is
	// then the job's wait, so the process is reaped exactly once.
	wait := make(chan error, 1)
	go func() { wait <- cmd.Wait() }()
	fg := t.Running.add(in.Command, cmd, out)
	defer t.Running.remove(fg)

	var err error
	select {
	case err = <-wait:
		// The command finished on its own. Anyone waiting on a promotion
		// that raced with it hears that it did not happen.
		fg.hand(0)
	case <-ctx.Done():
		// Cancelled or out of time: kill the process group and reap. A
		// promotion racing with the cancel hears the same no.
		killCmd()
		err = <-wait
		fg.hand(0)
	case <-promoteChan(fg):
		job, perr := t.Jobs.Adopt(in.Command, cmd, out, wait)
		if perr != nil {
			// Promotion failed, so nothing was handed over: wait it out.
			fg.hand(0)
			err = <-wait
			break
		}
		promoted = true
		fg.hand(job.ID)
		t.Cache.invalidate()
		return fmt.Sprintf("Moved to the background as job %d (pid %d): %s\nIt is still running; output is collected in %s. You will be told when it finishes; the job tool shows output, waits, or kills it.", job.ID, job.Pid, in.Command, job.OutPath), nil
	}
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
			return "", fmt.Errorf("bash: %w", err)
		}
	}
	if result == "" {
		result = "(no output)"
	}
	if exit != 0 {
		result += exitNote(in.Command, exit)
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
	if what := selfModifyingCommand(in.Command, t.Root); what != "" {
		return true, what + ", which decides what janetcode may do without asking"
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
	if what := selfModifyingCommand(in.Command, t.Root); what != "" {
		notes = append(notes, what)
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
	"NONINTERACTIVE=1",
	// less pages even a short file unless told not to, and -X keeps it
	// from clearing the screen on exit.
	"LESS=-FX",
}

// nonUTF8Locales are the C/POSIX locales. LC_ALL overrides every LC_*
// category, so a user or CI image that exports LC_ALL=C makes tools emit
// non-UTF-8 bytes no matter what LC_CTYPE says — and the agent then reads
// mangled output. Only that case is touched; a real locale is left alone.
var nonUTF8Locales = map[string]bool{"C": true, "POSIX": true}

func scrubEnv(env []string) []string {
	out := make([]string, 0, len(env)+len(nonInteractiveEnv))
	forced := map[string]bool{}
	for _, kv := range nonInteractiveEnv {
		name, _, _ := strings.Cut(kv, "=")
		forced[name] = true
	}
	dropLCAll := false
	for _, kv := range env {
		if name, val, _ := strings.Cut(kv, "="); name == "LC_ALL" && nonUTF8Locales[val] {
			dropLCAll = true
		}
	}
	for _, kv := range env {
		name, _, _ := strings.Cut(kv, "=")
		if secretEnv[name] || forced[name] {
			continue
		}
		if dropLCAll && (name == "LC_ALL" || name == "LC_CTYPE") {
			continue
		}
		out = append(out, kv)
	}
	if dropLCAll {
		out = append(out, "LC_CTYPE=C.UTF-8")
	}
	return append(out, nonInteractiveEnv...)
}

// clampTimeout turns a timeout_ms from the model into a duration between
// nothing and max, defaulting when it is absent.
//
// The clamp is done in milliseconds, before the multiplication: a
// time.Duration counts nanoseconds, so a number of milliseconds past
// about 2.5e11 overflows int64 and can come out negative. A negative
// duration is not greater than max, so it used to survive the clamp and
// reach context.WithTimeout as a deadline already in the past — the
// command was killed the instant it started and reported as having timed
// out after a nonsense interval. Models write 99999999999 for "no limit".
func clampTimeout(ms int, def, max time.Duration) time.Duration {
	if ms <= 0 {
		return def
	}
	if maxMS := int64(max / time.Millisecond); int64(ms) > maxMS {
		return max
	}
	return time.Duration(ms) * time.Millisecond
}
