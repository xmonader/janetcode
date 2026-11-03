package tool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/xmonader/janetcode/internal/shell"
	"github.com/xmonader/janetcode/internal/textfmt"
)

// Bash runs a shell command in the working directory.
type Bash struct {
	Root string
	WS   *Workspace
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
	// Sandbox, when enabled, wraps local commands in docker or sandbox-exec.
	// Shared with Jobs so background starts take the same wrapping.
	Sandbox *shell.Sandbox
	// NoIdleBound disables the idle bound on inline interpreter checks
	// (JANETCODE_OFF=idlebound); see shell_idle.go.
	NoIdleBound bool
	// Probe, when set, lets a plain grep or rg that finds nothing carry
	// the empty-search probes (see bash_probe.go); nil is off.
	Probe *EmptyProbe
}

func (Bash) Name() string { return "bash" }
func (Bash) Description() string {
	return "Run a shell command with " + shell.Default().Name + " in the working directory. Returns combined stdout/stderr and the exit code. Set timeout_ms for a long-running command, or background: true for servers, watchers and long builds: the command keeps running, you get a job id at once and a note when it finishes (see the job tool). A command that asks questions: pass the answers in stdin, or set interactive: true and answer each prompt with the job tool's stdin action. Do not use for reading or editing files; use the dedicated tools."
}
func (Bash) Mutating() bool { return true }
func (Bash) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{
"command":{"type":"string","description":"The command to run"},
"timeout_ms":{"type":"integer","description":"Timeout in milliseconds (default 120000, max 600000)"},
"background":{"type":"boolean","description":"Run detached and return immediately with a job id; output is collected and reported when the job ends"},
"stdin":{"type":"string","description":"Standard input, then EOF; a newline after each answer"},
"interactive":{"type":"boolean","description":"Background job on a terminal, for programs that prompt"}},
"required":["command"],"additionalProperties":false}`)
}

func (t Bash) Run(ctx context.Context, input json.RawMessage) (string, error) {
	var in struct {
		Command   string `json:"command"`
		TimeoutMS int    `json:"timeout_ms"`
		// Timeout is the name models reach for when they mean timeout_ms,
		// and a rejected call costs a whole round trip: a recorded
		// session lost a step to `"timeout": "30000"`. Accepted as
		// milliseconds, as a numeric string, or as a Go duration.
		Timeout     json.RawMessage `json:"timeout"`
		Background  bool            `json:"background"`
		Stdin       *string         `json:"stdin"`
		Interactive bool            `json:"interactive"`
	}
	if err := decode(input, &in); err != nil {
		return "", err
	}
	if in.TimeoutMS == 0 {
		in.TimeoutMS = timeoutAlias(in.Timeout)
	}
	if strings.TrimSpace(in.Command) == "" {
		return "", fmt.Errorf("command is empty")
	}
	root := rootOf(t.Root, t.WS)
	if risk, why := shell.Classify(in.Command, root); risk == shell.RiskCatastrophic {
		return "", fmt.Errorf("refused: this command %s. janetcode never runs it, not even with auto-approve; do not retry it in another form", why)
	}
	if in.Background || in.Interactive {
		if t.Jobs == nil {
			return "", fmt.Errorf("background jobs are not available in this session; run the command in the foreground with a timeout, giving any answers it needs in stdin")
		}
		if in.Stdin != nil && in.Interactive {
			return "", fmt.Errorf("stdin and interactive do not go together: an interactive job is answered with the job tool's stdin action as it asks")
		}
		job, err := t.Jobs.StartWith(t.WS, root, in.Command, JobOptions{Terminal: in.Interactive})
		if err != nil {
			return "", err
		}
		t.Cache.invalidate()
		if in.Stdin != nil && *in.Stdin != "" {
			if err := t.Jobs.WriteRaw(ctx, job.ID, *in.Stdin); err != nil {
				return "", fmt.Errorf("started job %d but could not write its stdin: %w", job.ID, err)
			}
		}
		if in.Interactive {
			first := t.Jobs.Settle(ctx, job.ID, 0, 3*time.Second)
			msg := fmt.Sprintf("Started interactive job %d (pid %d) on a terminal: %s\n", job.ID, job.Pid, in.Command)
			if job.Note != "" {
				msg += "[" + job.Note + "]\n"
			}
			if first != "" {
				msg += "It printed:\n" + textfmt.TruncateTo(first, 4000) + "\n"
			} else {
				msg += "It has printed nothing yet.\n"
			}
			return msg + "Answer a prompt with the job tool: action stdin, the job id, and text (an empty text presses Enter); the result shows what it printed next. You will be told when it finishes.", nil
		}
		return fmt.Sprintf("Started background job %d (pid %d): %s\nOutput is collected in %s. You will be told when it finishes; the job tool shows output, waits, or kills it.", job.ID, job.Pid, in.Command, job.OutPath), nil
	}
	cacheKey := t.Cache.key(root, in.Command)
	if in.Stdin == nil && (t.WS == nil || t.WS.Local()) && (t.Sandbox == nil || !t.Sandbox.Enabled()) {
		if out, age, ok := t.Cache.get(cacheKey); ok {
			return out + fmt.Sprintf("\n[cached: this exact command ran %s ago on an identical tree and nothing has changed since; append '%s' to force a rerun]", age.Round(time.Second), noCacheMarker), nil
		}
	}
	def, max := t.DefaultTimeout, t.MaxTimeout
	if def == 0 {
		def = 2 * time.Minute
	}
	if max == 0 {
		max = 10 * time.Minute
	}
	// An inline check the call gave no timeout for is bounded by silence.
	inline := !t.NoIdleBound && in.TimeoutMS == 0 && in.Stdin == nil && isInlineCheck(in.Command)
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

	cmd, err := localShell(runCtx, t.WS, t.Sandbox, root, in.Command)
	if err != nil {
		return "", err
	}
	cmd.WaitDelay = 2 * time.Second
	out := &swapBuffer{max: maxCommandOutput}
	cmd.Stdout = out
	cmd.Stderr = out
	cmd.Stdin = nil
	if inline && cmd.Env != nil {
		cmd.Env = append(cmd.Env, "PYTHONUNBUFFERED=1", "NODE_DISABLE_COLORS=1")
	}
	// Standard input. Given text, the command reads it and then EOF.
	// Otherwise it used to get EOF at once (a nil Stdin is /dev/null), so
	// a program that asked a question read nothing and failed, and the
	// model saw the failure, not the question. Where a read can be seen
	// (Linux, a command on this machine outside the sandbox) the command
	// now gets a pipe held open: a read that follows a question is
	// reported as a command waiting for an answer and handed to the jobs
	// registry to be answered, and any other read gets its EOF as before.
	var pipe *inputPipe
	var pipeIno uint64
	var pipeR *os.File
	if in.Stdin != nil {
		cmd.Stdin = strings.NewReader(*in.Stdin)
	} else if inline {
		// stdin stays /dev/null: a check that reads gets EOF at once.
	} else if stdinWatchable() && (t.WS == nil || t.WS.Local()) && (t.Sandbox == nil || !t.Sandbox.Enabled()) {
		if pr, pw, perr := os.Pipe(); perr == nil {
			cmd.Stdin, pipeR = pr, pr
			pipe = &inputPipe{w: pw}
			pipeIno = pipeInode(pr)
			defer pipe.eof()
		}
	}

	guard := sandboxGitGuard(t.WS, t.Sandbox, root)
	err = cmd.Start()
	if pipeR != nil {
		pipeR.Close() // the child holds its own copy
	}
	if err != nil {
		return "", fmt.Errorf("start bash: %w", err)
	}
	waiting := make(chan string, 1)
	stopWatch := make(chan struct{})
	defer close(stopWatch)
	if pipe != nil {
		go watchStdin(cmd, pipe, pipeIno, out, t.Jobs != nil, waiting, stopWatch)
	}
	// The wait runs in its own goroutine so the command can be handed to
	// the jobs registry while it is still running: the same channel is
	// then the job's wait, so the process is reaped exactly once. What
	// the guard puts back is said in the output, which by then is either
	// this result or, after a promotion, the job's log.
	wait := make(chan error, 1)
	go func() {
		err := cmd.Wait()
		if note := guard.Restore(); note != "" {
			fmt.Fprintf(out, "\n%s\n", note)
		}
		wait <- err
	}()
	idleFired := make(chan struct{})
	if inline {
		stopIdle := make(chan struct{})
		defer close(stopIdle)
		go watchIdle(out, inlineIdle, killCmd, idleFired, stopIdle)
	}
	fg := t.Running.add(in.Command, cmd, out)
	defer t.Running.remove(fg)

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
	case asked := <-waiting:
		// The command asked something and is blocked reading the answer.
		// Waiting it out would end in the timeout and killing it would
		// lose what it has done, so it becomes a job the model answers.
		job, perr := t.Jobs.AdoptWith(in.Command, cmd, out, wait, pipe.hand())
		if perr != nil {
			fg.hand(0)
			pipe.unhand()
			pipe.eof()
			err = <-wait
			break
		}
		promoted = true
		fg.hand(job.ID)
		t.Cache.invalidate()
		return fmt.Sprintf("The command is waiting for input. It asked:\n%s\n\nIt keeps running as background job %d (pid %d), so nothing it has done is lost. Answer with the job tool: action stdin, id %d, and the text to type (an empty text presses Enter); the result shows what it prints next. If it should not be answered, kill the job and rerun the command with its answers in stdin.", textfmt.TruncateTo(promptTail(asked), 2000), job.ID, job.Pid, job.ID), nil
	case <-promoteChan(fg):
		job, perr := t.Jobs.AdoptWith(in.Command, cmd, out, wait, pipe.hand())
		if perr != nil {
			// Promotion failed, so nothing was handed over: wait it out.
			fg.hand(0)
			pipe.unhand()
			pipe.eof()
			err = <-wait
			break
		}
		promoted = true
		fg.hand(job.ID)
		t.Cache.invalidate()
		// What this result says matters more than it looks. The user
		// pressed ctrl+g precisely so the turn would stop waiting; a
		// result that mentions the job tool's wait action gets one —
		// observed live, the model answered a promotion by calling job
		// wait with a thirty-second timeout, which put the turn back
		// where ctrl+g had taken it from. So it says what not to do.
		return fmt.Sprintf("Moved to the background as job %d (pid %d): %s\nThe user pressed ctrl+g so this command would stop holding the turn open. It keeps running, its output is collected in %s, and you will be told when it finishes. Carry on with the rest of the work now: do not wait on this job and do not run the command again.", job.ID, job.Pid, in.Command, job.OutPath), nil
	}
	result := out.String()
	exit := 0
	// A command that starts something with & and returns — `npm run dev
	// &`, `python -m http.server &` — exits 0 while the process it left
	// behind still holds the output pipe. WaitDelay then closes the pipe
	// and Wait reports ErrWaitDelay for a command that succeeded; that
	// used to fall to the default case below, so the model got an opaque
	// "WaitDelay expired" error and none of the output.
	// A cancel or timeout that landed meanwhile still reads as one.
	leftBehind := errors.Is(err, exec.ErrWaitDelay) && ctx.Err() == nil
	if leftBehind {
		err = nil
	}
	if err != nil {
		var ee *exec.ExitError
		switch {
		case isClosed(idleFired):
			return textfmt.Truncate(result + "\n" + idleMessage(in.Command)), nil
		case errors.Is(ctx.Err(), context.DeadlineExceeded):
			return textfmt.Truncate(result + fmt.Sprintf("\n[command timed out after %s and was killed]", timeout)), nil
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
	var probe string
	if t.Probe != nil && strings.TrimSpace(result) == "" && in.Stdin == nil && !leftBehind && ctx.Err() == nil &&
		(t.WS == nil || t.WS.Local()) && (t.Sandbox == nil || !t.Sandbox.Enabled()) {
		if argv, piped, ok := searchArgv(in.Command); ok && (exit == 1 || piped && exit == 0) {
			probe = t.probeSearch(ctx, root, argv)
		}
	}
	if result == "" {
		result = "(no output)"
	}
	if exit != 0 {
		result += exitNote(in.Command, exit)
		if pipe.sawRead() {
			result += "\n[the command read its standard input, which had nothing for it, and got end-of-file. If it needs input, give it in stdin, or run it with interactive: true and answer it with the job tool]"
		}
	}
	if leftBehind {
		// What it left running may change the tree at any moment, as a
		// background job may, so neither this result nor any earlier one
		// can be replayed.
		t.Cache.invalidate()
		return textfmt.Truncate(result + "\n[the command exited but left a process running that held its output open; that process was not stopped, and nothing it prints from now on is collected. Use background: true for servers and watchers]"), nil
	}
	result = textfmt.Truncate(result)
	if in.Stdin == nil && (t.WS == nil || t.WS.Local()) && (t.Sandbox == nil || !t.Sandbox.Enabled()) {
		t.Cache.put(cacheKey, result)
	}
	return result + probe, nil
}

func isClosed(c <-chan struct{}) bool {
	select {
	case <-c:
		return true
	default:
		return false
	}
}

// maxCommandOutput bounds what one foreground command's output may cost
// in memory. Only textfmt.MaxOutput of it is ever returned, so this is
// slack rather than a limit the model will meet by accident; what it
// stops is a command that prints without end.
const maxCommandOutput = 8 << 20

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
	root := rootOf(t.Root, t.WS)
	return t.guardFrom(in.Command, root, root, root != "")
}

// guardFrom is Guard for a command run from cwd (known or not).
func (t Bash) guardFrom(command, root, cwd string, known bool) (bool, string) {
	in := struct{ Command string }{command}
	// Anything the classifier could not grade (shell.RiskUnknown) asks
	// too: "I could not work out what this runs" is a reason to put the
	// command in front of the user, not a reason to wave it through.
	if risk, why := shell.ClassifyFrom(in.Command, root, cwd, known); risk == shell.RiskUnknown || risk >= shell.RiskEscapes {
		return true, why
	}
	if what := selfModifyingCommand(in.Command, root); what != "" {
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
	root := rootOf(t.Root, t.WS)
	return t.warnFrom(in.Command, root, root, root != "")
}

// warnFrom is Warn for a command run from cwd (known or not).
func (t Bash) warnFrom(command, root, cwd string, known bool) string {
	in := struct{ Command string }{command}
	var notes []string
	if risk, why := shell.ClassifyFrom(in.Command, root, cwd, known); risk != shell.RiskOrdinary {
		notes = append(notes, why)
	}
	if what := selfModifyingCommand(in.Command, root); what != "" {
		notes = append(notes, what)
	}
	for _, d := range dangerPatterns {
		if d.re.MatchString(in.Command) {
			notes = append(notes, d.note)
		}
	}
	return strings.Join(notes, "; ")
}

// timeoutAlias reads a `timeout` the model wrote instead of timeout_ms:
// a number of milliseconds, the same number quoted, or a duration such as
// "30s". Anything else is 0, which means the default.
func timeoutAlias(raw json.RawMessage) int {
	if len(raw) == 0 {
		return 0
	}
	var ms float64
	if json.Unmarshal(raw, &ms) == nil {
		return int(ms)
	}
	var s string
	if json.Unmarshal(raw, &s) != nil {
		return 0
	}
	if s = strings.TrimSpace(s); s == "" {
		return 0
	}
	if n, err := strconv.ParseFloat(s, 64); err == nil {
		return int(n)
	}
	if d, err := time.ParseDuration(s); err == nil && d > 0 {
		return int(d / time.Millisecond)
	}
	return 0
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
