package tool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Job is one background command.
type Job struct {
	ID      int
	Command string
	Pid     int
	Started time.Time
	Ended   time.Time
	Exit    int
	Done    bool
	OutPath string
	// notified is set once Finished reported the job.
	notified bool
	cmd      *exec.Cmd
	done     chan struct{}
}

// Jobs runs commands detached from the tool call that started them and
// remembers their state until the session ends.
type Jobs struct {
	// Dir holds output files; empty means the OS temp directory.
	Dir  string
	mu   sync.Mutex
	jobs []*Job
	next int
}

// Start launches the command in its own process group with output going
// to a file, and returns immediately.
func (j *Jobs) Start(root, command string) (*Job, error) {
	if j == nil {
		return nil, errors.New("background jobs are not available")
	}
	dir := j.Dir
	if dir == "" {
		dir = filepath.Join(os.TempDir(), "janetcode-jobs")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	j.mu.Lock()
	j.next++
	id := j.next
	j.mu.Unlock()
	out, err := os.CreateTemp(dir, fmt.Sprintf("job-%d-*.log", id))
	if err != nil {
		return nil, err
	}
	cmd := ShellCommand(context.Background(), command) // never cancelled; Kill uses the process group
	cmd.Dir = root
	cmd.Env = scrubEnv(os.Environ())
	setProcessGroup(cmd)
	cmd.Stdout, cmd.Stderr, cmd.Stdin = out, out, nil
	if err := cmd.Start(); err != nil {
		out.Close()
		os.Remove(out.Name())
		return nil, fmt.Errorf("start: %w", err)
	}
	job := &Job{ID: id, Command: command, Pid: cmd.Process.Pid, Started: time.Now(), OutPath: out.Name(), cmd: cmd, done: make(chan struct{})}
	j.mu.Lock()
	j.jobs = append(j.jobs, job)
	j.mu.Unlock()
	go func() {
		err := cmd.Wait()
		out.Close()
		j.mu.Lock()
		job.Done, job.Ended = true, time.Now()
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			job.Exit = ee.ExitCode()
			if job.Exit == -1 {
				job.Exit = signalExitCode(ee)
			}
		} else if err != nil {
			job.Exit = 1
		}
		j.mu.Unlock()
		close(job.done)
	}()
	return job, nil
}

// Adopt takes over a command that is already running in the foreground:
// its output starts going to a job log from the beginning, and it becomes
// a job like one started with background: true. wait is the channel the
// caller's own Wait goroutine will send the command's result on, so the
// process is waited for exactly once.
func (j *Jobs) Adopt(command string, cmd *exec.Cmd, out *swapBuffer, wait <-chan error) (*Job, error) {
	if j == nil {
		return nil, errors.New("background jobs are not available")
	}
	dir := j.Dir
	if dir == "" {
		dir = filepath.Join(os.TempDir(), "janetcode-jobs")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	j.mu.Lock()
	j.next++
	id := j.next
	j.mu.Unlock()
	f, err := os.CreateTemp(dir, fmt.Sprintf("job-%d-*.log", id))
	if err != nil {
		return nil, err
	}
	if err := out.attach(f); err != nil {
		f.Close()
		os.Remove(f.Name())
		return nil, err
	}
	pid := 0
	if cmd.Process != nil {
		pid = cmd.Process.Pid
	}
	job := &Job{ID: id, Command: command, Pid: pid, Started: time.Now(), OutPath: f.Name(), cmd: cmd, done: make(chan struct{})}
	j.mu.Lock()
	j.jobs = append(j.jobs, job)
	j.mu.Unlock()
	go func() {
		err := <-wait
		f.Close()
		j.mu.Lock()
		job.Done, job.Ended = true, time.Now()
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			job.Exit = ee.ExitCode()
			if job.Exit == -1 {
				job.Exit = signalExitCode(ee)
			}
		} else if err != nil {
			job.Exit = 1
		}
		j.mu.Unlock()
		close(job.done)
	}()
	return job, nil
}

// List returns every job, oldest first, as copies.
func (j *Jobs) List() []Job {
	if j == nil {
		return nil
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	out := make([]Job, 0, len(j.jobs))
	for _, job := range j.jobs {
		out = append(out, *job)
	}
	sort.Slice(out, func(a, b int) bool { return out[a].ID < out[b].ID })
	return out
}

// Get returns a copy of one job.
func (j *Jobs) Get(id int) (Job, bool) {
	job := j.find(id)
	if job == nil {
		return Job{}, false
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	return *job, true
}

func (j *Jobs) find(id int) *Job {
	if j == nil {
		return nil
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	for _, job := range j.jobs {
		if job.ID == id {
			return job
		}
	}
	return nil
}

// Wait blocks until the job ends or the context is done.
func (j *Jobs) Wait(ctx context.Context, id int) (Job, error) {
	job := j.find(id)
	if job == nil {
		return Job{}, fmt.Errorf("no job %d", id)
	}
	select {
	case <-job.done:
	case <-ctx.Done():
		return Job{}, ctx.Err()
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	return *job, nil
}

// Kill stops a running job's whole process group.
func (j *Jobs) Kill(id int) error {
	job := j.find(id)
	if job == nil {
		return fmt.Errorf("no job %d", id)
	}
	j.mu.Lock()
	done := job.Done
	j.mu.Unlock()
	if done {
		return fmt.Errorf("job %d already finished", id)
	}
	if job.cmd.Cancel != nil {
		return job.cmd.Cancel()
	}
	return job.cmd.Process.Kill()
}

// KillAll stops every running job, for shutdown.
func (j *Jobs) KillAll() {
	for _, job := range j.List() {
		if !job.Done {
			_ = j.Kill(job.ID)
		}
	}
}

// Output returns the last n lines of a job's output (all when n <= 0).
func (j *Jobs) Output(id, n int) (string, error) {
	job := j.find(id)
	if job == nil {
		return "", fmt.Errorf("no job %d", id)
	}
	data, err := os.ReadFile(job.OutPath)
	if err != nil {
		return "", err
	}
	s := strings.TrimRight(string(data), "\n")
	if n > 0 {
		lines := strings.Split(s, "\n")
		if len(lines) > n {
			lines = lines[len(lines)-n:]
		}
		s = strings.Join(lines, "\n")
	}
	return s, nil
}

// Finished returns a note for every job that ended since the last call,
// with the tail of its output, and marks them as reported.
func (j *Jobs) Finished() []string {
	if j == nil {
		return nil
	}
	j.mu.Lock()
	var ended []*Job
	for _, job := range j.jobs {
		if job.Done && !job.notified {
			job.notified = true
			ended = append(ended, job)
		}
	}
	j.mu.Unlock()
	var notes []string
	for _, job := range ended {
		tail, _ := j.Output(job.ID, 20)
		note := job.Summary()
		if strings.TrimSpace(tail) != "" {
			note += "\nlast output:\n" + TruncateTo(tail, 2000)
		}
		notes = append(notes, note)
	}
	return notes
}

// Summary is one line about the job's state.
func (job Job) Summary() string {
	cmd := job.Command
	if len(cmd) > 80 {
		cmd = cmd[:79] + "…"
	}
	if !job.Done {
		return fmt.Sprintf("job %d running for %s (pid %d): %s", job.ID, time.Since(job.Started).Round(time.Second), job.Pid, cmd)
	}
	return fmt.Sprintf("job %d finished with exit %d after %s: %s", job.ID, job.Exit, job.Ended.Sub(job.Started).Round(time.Second), cmd)
}

// JobTool lets the model inspect, wait for and stop background jobs.
type JobTool struct{ Jobs *Jobs }

func (JobTool) Name() string { return "job" }
func (JobTool) Description() string {
	return "Manage background jobs started with bash background:true. list shows them; output returns the last lines of a job's output; wait blocks until a job finishes (up to timeout_ms); kill stops one. You are told automatically when a job finishes."
}
func (JobTool) Mutating() bool { return false }
func (JobTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{
"action":{"type":"string","enum":["list","output","wait","kill"]},
"id":{"type":"integer","description":"Job id (from the bash result or list)"},
"lines":{"type":"integer","description":"For output: how many trailing lines (default 100)"},
"timeout_ms":{"type":"integer","description":"For wait: how long to wait (default 120000, max 600000)"}},
"required":["action"],"additionalProperties":false}`)
}

func (t JobTool) Run(ctx context.Context, input json.RawMessage) (string, error) {
	var in struct {
		Action    string `json:"action"`
		ID        int    `json:"id"`
		Lines     int    `json:"lines"`
		TimeoutMS int    `json:"timeout_ms"`
	}
	if err := decode(input, &in); err != nil {
		return "", err
	}
	switch in.Action {
	case "list":
		jobs := t.Jobs.List()
		if len(jobs) == 0 {
			return "no background jobs", nil
		}
		var lines []string
		for _, job := range jobs {
			lines = append(lines, job.Summary())
		}
		return strings.Join(lines, "\n"), nil
	case "output":
		if in.Lines <= 0 {
			in.Lines = 100
		}
		job, ok := t.Jobs.Get(in.ID)
		if !ok {
			return "", fmt.Errorf("no job %d", in.ID)
		}
		out, err := t.Jobs.Output(in.ID, in.Lines)
		if err != nil {
			return "", err
		}
		if strings.TrimSpace(out) == "" {
			out = "(no output yet)"
		}
		return job.Summary() + "\n" + Truncate(out), nil
	case "wait":
		timeout := 2 * time.Minute
		if in.TimeoutMS > 0 {
			timeout = time.Duration(in.TimeoutMS) * time.Millisecond
		}
		if timeout > 10*time.Minute {
			timeout = 10 * time.Minute
		}
		wctx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		job, err := t.Jobs.Wait(wctx, in.ID)
		if errors.Is(err, context.DeadlineExceeded) {
			j, _ := t.Jobs.Get(in.ID)
			return j.Summary() + fmt.Sprintf("\n[still running after %s]", timeout), nil
		}
		if err != nil {
			return "", err
		}
		out, _ := t.Jobs.Output(in.ID, 100)
		return job.Summary() + "\n" + Truncate(out), nil
	case "kill":
		if err := t.Jobs.Kill(in.ID); err != nil {
			return "", err
		}
		return fmt.Sprintf("job %d killed", in.ID), nil
	}
	return "", fmt.Errorf("unknown action %q", in.Action)
}
