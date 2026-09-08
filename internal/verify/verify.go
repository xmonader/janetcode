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

	"github.com/xmonader/janetcode/internal/tool"
)

// ProjectFile lists the commands, relative to the project root.
const ProjectFile = ".janetcode/verify.json"

// Config is the file's shape.
type Config struct {
	// Commands run in order; the first failure stops the run.
	Commands []string `json:"commands"`
	// Timeout in seconds per command; zero means 600.
	Timeout int `json:"timeout,omitempty"`
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
	return tool.AtomicWrite(p, append(data, '\n'))
}

// Result is the outcome of one verification run.
type Result struct {
	Passed   []string
	Failed   string // the command that failed, or ""
	Exit     int
	Output   string
	Duration time.Duration
}

// OK reports whether every command passed.
func (r Result) OK() bool { return r.Failed == "" }

// Summary is a one-line account for notices.
func (r Result) Summary() string {
	if r.OK() {
		return fmt.Sprintf("verified: %s (%s)", strings.Join(r.Passed, " · "), r.Duration.Round(100*time.Millisecond))
	}
	return fmt.Sprintf("verification failed: %s (exit %d, %s)", r.Failed, r.Exit, r.Duration.Round(100*time.Millisecond))
}

// Feedback is the message the model gets when a check fails.
func (r Result) Feedback() string {
	return fmt.Sprintf("Verification failed. This project requires the following to pass before a change is done.\n\nCommand: %s\nExit code: %d\nOutput:\n```\n%s\n```\n\nFix the cause, then finish. Do not disable or skip the check.",
		r.Failed, r.Exit, tool.TruncateTo(strings.TrimSpace(r.Output), 6000))
}

// Runner runs the commands in a directory.
type Runner struct {
	Config Config
	Cwd    string
}

// Enabled reports whether there is anything to run.
func (r *Runner) Enabled() bool { return r != nil && len(r.Config.Commands) > 0 }

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
		cmd := exec.CommandContext(cctx, "bash", "-c", command)
		cmd.Dir = r.Cwd
		cmd.Env = append(os.Environ(), "JANETCODE_VERIFY=1")
		out, err := cmd.CombinedOutput()
		timedOut := errors.Is(cctx.Err(), context.DeadlineExceeded)
		cancel()
		if err == nil {
			res.Passed = append(res.Passed, command)
			continue
		}
		res.Failed, res.Output = command, string(out)
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
	res.Duration = time.Since(start)
	return res
}
