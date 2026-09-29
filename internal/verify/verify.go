// Package verify runs a project's definition of done (build, test, lint
// commands) after the model says it has finished a turn that changed
// something. A failure goes back to the model before the user sees
// "done"; a pass is reported. It generalises --until into the normal loop.
package verify

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/xmonader/janetcode/internal/fsutil"
	"github.com/xmonader/janetcode/internal/secret"
	"github.com/xmonader/janetcode/internal/session"
	"github.com/xmonader/janetcode/internal/shell"
	"github.com/xmonader/janetcode/internal/textfmt"
	"github.com/xmonader/janetcode/internal/trust"
)

// ProjectFile lists the commands, relative to the project root.
const ProjectFile = ".janetcode/verify.json"

// Config is the file's shape.
type Config struct {
	// Commands run in order; the first failure stops the run.
	Commands []string `json:"commands"`
	// Timeout in seconds per command; zero means 600.
	Timeout int `json:"timeout,omitempty"`
	// Mutate, when set, is the test command run against small mutations
	// of the lines the agent changed once the commands pass; mutants the
	// tests do not catch are sent back like a failure.
	Mutate string `json:"mutate,omitempty"`
	// MaxMutants bounds mutants per run; zero means the mutation runner's default.
	MaxMutants int `json:"max_mutants,omitempty"`
}

// MaxRounds bounds how often one turn is sent back for failing checks.
const MaxRounds = 3

// Load reads the project file. A missing file gives an empty config and
// hash; the hash identifies the approved content.
func Load(root string) (Config, string, error) {
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
	for i, cmd := range c.Commands {
		if strings.TrimSpace(cmd) == "" {
			return Config{}, "", fmt.Errorf("%s: command %d is empty", ProjectFile, i+1)
		}
	}
	sum := sha256.Sum256(data)
	return c, hex.EncodeToString(sum[:]), nil
}

// Save writes the project file.
func Save(root string, c Config) error {
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	p := filepath.Join(root, ProjectFile)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	if err := fsutil.AtomicWrite(p, append(data, '\n')); err != nil {
		return err
	}
	trust.SelfWrote(root)
	return nil
}

// Result is the outcome of one verification run.
type Result struct {
	Passed   []string
	Failed   string // the command that failed, or ""
	Exit     int
	Output   string
	Duration time.Duration
	// Mutation is the mutation-testing report when Config.Mutate is set
	// and the commands passed.
	Mutation Mutation
	// MutationErr is why mutation testing could not run (not a failure).
	MutationErr error
}

// OK reports whether every command passed and no mutant survived.
func (r Result) OK() bool { return r.Failed == "" && (r.Mutation == nil || r.Mutation.OK()) }

// Summary is a one-line account for notices.
func (r Result) Summary() string {
	switch {
	case r.Failed != "":
		return fmt.Sprintf("verification failed: %s (exit %d, %s)", r.Failed, r.Exit, r.Duration.Round(100*time.Millisecond))
	case r.Mutation != nil && !r.Mutation.OK():
		return "verification failed: " + r.Mutation.Summary()
	}
	s := fmt.Sprintf("verified: %s (%s)", strings.Join(r.Passed, " · "), r.Duration.Round(100*time.Millisecond))
	if r.Mutation != nil {
		s += " · " + r.Mutation.Summary()
	}
	if r.MutationErr != nil {
		s += " · mutation testing unavailable: " + r.MutationErr.Error()
	}
	return s
}

// Feedback is the message the model gets when a check fails.
func (r Result) Feedback() string {
	if r.Failed == "" && r.Mutation != nil {
		return r.Mutation.Feedback()
	}
	return fmt.Sprintf("Verification failed. This project requires the following to pass before a change is done.\n\nCommand: %s\nExit code: %d\nOutput:\n```\n%s\n```\n\nFix the cause, then finish. Do not disable or skip the check.",
		r.Failed, r.Exit, textfmt.TruncateTo(strings.TrimSpace(r.Output), 6000))
}

// Mutation is a mutation-testing report (internal/mutate.Report): verify
// only needs to judge and describe it, so it does not import the runner.
type Mutation interface {
	// OK reports whether every mutant was caught.
	OK() bool
	Summary() string
	Feedback() string
	// Scored reports whether mutants actually ran (the run was not
	// skipped), so there is a score for Event to record.
	Scored() bool
	Event() session.Event
}

// MutationRun is what a MutateFunc is asked to do: mutate the changed
// lines under Root and run Command against each mutant.
type MutationRun struct {
	Root    string
	Command string
	Timeout time.Duration
	Max     int
	Log     func(string)
}

// MutateFunc runs mutation testing; internal/mutate.ForVerify is the one
// janetcode uses.
type MutateFunc func(ctx context.Context, run MutationRun) (Mutation, error)

// errNoMutator is reported when a config asks for mutation testing but
// the runner was built without a MutateFunc.
var errNoMutator = errors.New("no mutation runner configured")

// Runner runs the commands in a directory.
type Runner struct {
	Config Config
	Cwd    string
	// Log receives mutation progress lines when set.
	Log func(string)
	// Mutate runs Config.Mutate once the commands pass.
	Mutate MutateFunc
	// Display renames a command in everything that is reported: the
	// Result's Passed and Failed entries, and so the summary, the
	// feedback the model reads and the gate event. A held-out acceptance
	// gate runs a script whose path is exactly what the run must not
	// disclose — the path is what runs, the label is what is said.
	Display map[string]string
}

// name is how a command is reported: its Display entry, else itself.
func (r *Runner) name(command string) string {
	if n, ok := r.Display[command]; ok && n != "" {
		return n
	}
	return command
}

// CommandNames are the commands as they are reported, for a notice that
// says what is about to run.
func (r *Runner) CommandNames() []string {
	if r == nil {
		return nil
	}
	out := make([]string, 0, len(r.Config.Commands))
	for _, c := range r.Config.Commands {
		out = append(out, r.name(c))
	}
	return out
}

// Enabled reports whether there is anything to run.
func (r *Runner) Enabled() bool {
	return r != nil && (len(r.Config.Commands) > 0 || strings.TrimSpace(r.Config.Mutate) != "")
}

// Run executes the commands in order and stops at the first failure.
func (r *Runner) Run(ctx context.Context) Result {
	start := time.Now()
	res := Result{}
	timeout := time.Duration(r.Config.Timeout) * time.Second
	if timeout <= 0 {
		timeout = 10 * time.Minute
	}
	for _, command := range r.Config.Commands {
		cctx, cancel := context.WithTimeout(ctx, timeout)
		cmd := shell.Command(cctx, command)
		// The timeout has to be a real bound: without a process group the
		// context kills the shell alone, and the wait then blocks on the
		// output pipes for as long as anything the command started still
		// holds them. Supervise kills the group and bounds that wait.
		shell.Supervise(cmd)
		cmd.Dir = r.Cwd
		// Scrubbed of the agent's credentials: verify commands come from
		// the project, and their output goes back to the model.
		cmd.Env = append(shell.ScrubEnv(os.Environ()), "JANETCODE_VERIFY=1")
		// Bounded rather than CombinedOutput: a verbose suite can print
		// for as long as it runs, and only the ends are ever read.
		out := &capture{}
		cmd.Stdout, cmd.Stderr = out, out
		err := cmd.Run()
		// A command that exits 0 but leaks a grandchild holding its pipes
		// surfaces as ErrWaitDelay once the bounded pipe wait cuts the
		// read — the command itself passed.
		if errors.Is(err, exec.ErrWaitDelay) {
			err = nil
		}
		timedOut := errors.Is(cctx.Err(), context.DeadlineExceeded)
		cancel()
		if err == nil {
			res.Passed = append(res.Passed, r.name(command))
			continue
		}
		// Redacted as a bash result is: this output goes to the model,
		// the session log and eval records, and a suite that prints the
		// environment or a .env on failure would otherwise hand over what
		// the same command run through bash does not.
		red, _ := secret.Redact(out.String())
		res.Failed, res.Output = r.name(command), red
		var ee *exec.ExitError
		switch {
		case timedOut:
			res.Exit, res.Output = 124, res.Output+"\n[timed out after "+timeout.String()+"]"
		case errors.As(err, &ee):
			res.Exit = ee.ExitCode()
		default:
			res.Exit, res.Output = 1, res.Output+"\n"+err.Error()
		}
		break
	}
	if res.Failed == "" && strings.TrimSpace(r.Config.Mutate) != "" && ctx.Err() == nil {
		if r.Mutate == nil {
			res.MutationErr = errNoMutator
		} else if rep, err := r.Mutate(ctx, MutationRun{Root: r.Cwd, Command: r.Config.Mutate, Timeout: timeout, Max: r.Config.MaxMutants, Log: r.Log}); err != nil {
			res.MutationErr = err
		} else {
			res.Mutation = rep
		}
	}
	res.Duration = time.Since(start)
	return res
}
