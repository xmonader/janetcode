package tool

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/xmonader/janetcode/internal/shell"
	"github.com/xmonader/janetcode/internal/textfmt"
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
	// stdin is the write end of the pipe attached to the process.
	// Background jobs get one so a later Write can drive them; a
	// one-shot bash call still uses EOF (cmd.Stdin = nil).
	stdin io.WriteCloser
}

// Jobs runs commands detached from the tool call that started them and
// remembers their state until the session ends.
type Jobs struct {
	// Dir holds output files; empty means the OS temp directory.
	Dir string
	// Sandbox, when enabled, wraps local commands the same way Bash does.
	Sandbox *shell.Sandbox
	mu      sync.Mutex
	jobs    []*Job
	next    int
}

// Start launches the command in its own process group with output going
// to a file, and returns immediately.
func (j *Jobs) Start(root, command string) (*Job, error) {
	return j.StartOn(nil, root, command)
}

// StartOn is Start on a workspace: a remote host runs the command through
// ssh or docker exec, as a local process the job can still kill.
func (j *Jobs) StartOn(ws *Workspace, root, command string) (*Job, error) {
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
	// Not the caller's context: a background job outlives the tool call
	// that started it, and is stopped through Kill rather than by the
	// turn ending.
	cmd, err := localShell(context.Background(), ws, j.Sandbox, root, command)
	if err != nil {
		out.Close()
		os.Remove(out.Name())
		return nil, err
	}
	pr, pw, err := os.Pipe()
	if err != nil {
		out.Close()
		os.Remove(out.Name())
		return nil, err
	}
	// *os.File stdin is handed to the child directly. io.Pipe would make
	// Wait hang on a copy goroutine until we closed the write end.
	cmd.Stdout, cmd.Stderr, cmd.Stdin = out, out, pr
	if err := cmd.Start(); err != nil {
		pw.Close()
		pr.Close()
		out.Close()
		os.Remove(out.Name())
		return nil, fmt.Errorf("start: %w", err)
	}
	pr.Close() // parent copy; the child holds the read end
	job := &Job{ID: id, Command: command, Pid: cmd.Process.Pid, Started: time.Now(), OutPath: out.Name(), cmd: cmd, done: make(chan struct{}), stdin: pw}
	j.mu.Lock()
	j.jobs = append(j.jobs, job)
	j.mu.Unlock()
	go func() {
		err := cmd.Wait()
		out.Close()
		j.mu.Lock()
		if job.stdin != nil {
			_ = job.stdin.Close()
			job.stdin = nil
		}
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
//
// The job's Done flag is not on its own enough to say the job is still
// running. It is set by the goroutine waiting on the command, after
// cmd.Wait has returned — that is, after the process has been reaped and
// its pid handed back to the kernel to give to somebody else. A kill that
// arrived in that window signalled a process group that was no longer
// ours: SIGKILL to whatever tree had just been handed the recycled
// number. So the process itself is asked too, under the lock, and a job
// that has already been reaped is reported as finished, not signalled.
func (j *Jobs) Kill(id int) error {
	job := j.find(id)
	if job == nil {
		return fmt.Errorf("no job %d", id)
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if job.Done || processDone(job.cmd) {
		return fmt.Errorf("job %d already finished", id)
	}
	if job.stdin != nil {
		_ = job.stdin.Close()
		job.stdin = nil
	}
	if job.cmd.Cancel != nil {
		return job.cmd.Cancel()
	}
	return job.cmd.Process.Kill()
}

// Write sends a line to a running job's stdin. A missing trailing newline
// is added so a typed line is a line. One-shot bash calls still get EOF
// on stdin; only background jobs have a pipe.
func (j *Jobs) Write(id int, data string) error {
	if strings.TrimSpace(data) == "" {
		return fmt.Errorf("nothing to write")
	}
	if !strings.HasSuffix(data, "\n") {
		data += "\n"
	}
	job := j.find(id)
	if job == nil {
		return fmt.Errorf("no job %d", id)
	}
	j.mu.Lock()
	w, done := job.stdin, job.Done || processDone(job.cmd)
	j.mu.Unlock()
	if done {
		return fmt.Errorf("job %d already finished", id)
	}
	if w == nil {
		return fmt.Errorf("job %d does not accept stdin", id)
	}
	_, err := io.WriteString(w, data)
	return err
}

// KillAll stops every running job, for shutdown.
func (j *Jobs) KillAll() {
	for _, job := range j.List() {
		if !job.Done {
			_ = j.Kill(job.ID)
		}
	}
}

// maxOutputTail bounds how much of a job's log is read to answer for it.
// Nothing above asks for more than a hundred lines, and a background
// server or a verbose build writes a log with no bound on it at all.
const maxOutputTail = 1 << 20

// Output returns the last n lines of a job's output (all when n <= 0, up
// to the last maxOutputTail bytes of the log).
//
// Only the tail is read. Finished() asks every job that ended for its last
// twenty lines at each turn boundary, so reading the whole file to keep
// twenty lines of it made a chatty job — a dev server, a test loop, a
// watcher logging all session — cost its entire log in memory, twice
// over, at a moment that has nothing to do with it.
func (j *Jobs) Output(id, n int) (string, error) {
	job := j.find(id)
	if job == nil {
		return "", fmt.Errorf("no job %d", id)
	}
	f, err := os.Open(job.OutPath)
	if err != nil {
		return "", err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return "", err
	}
	start, size := int64(0), st.Size()
	if size > maxOutputTail {
		start = size - maxOutputTail
	}
	data := make([]byte, size-start)
	if _, err := f.ReadAt(data, start); err != nil && err != io.EOF {
		return "", err
	}
	if start > 0 {
		// The read began mid-line; drop the fragment.
		if i := bytes.IndexByte(data, '\n'); i >= 0 {
			data = data[i+1:]
		}
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
			note += "\nlast output:\n" + textfmt.TruncateTo(tail, 2000)
		}
		notes = append(notes, note)
	}
	return notes
}

// Summary is one line about the job's state.
func (job Job) Summary() string {
	cmd := textfmt.ClipBytes(job.Command, 80)
	if !job.Done {
		return fmt.Sprintf("job %d running for %s (pid %d): %s", job.ID, time.Since(job.Started).Round(time.Second), job.Pid, cmd)
	}
	return fmt.Sprintf("job %d finished with exit %d after %s: %s", job.ID, job.Exit, job.Ended.Sub(job.Started).Round(time.Second), cmd)
}

// JobTool lets the model inspect, wait for and stop background jobs.
type JobTool struct{ Jobs *Jobs }

func (JobTool) Name() string { return "job" }
func (JobTool) Description() string {
	return "Manage background jobs started with bash background:true. list shows them; output returns the last lines of a job's output; wait blocks until a job finishes (up to timeout_ms); kill stops one; stdin writes a line to a running job (interactive programs). You are told automatically when a job finishes."
}
func (JobTool) Mutating() bool { return false }
func (JobTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{
"action":{"type":"string","enum":["list","output","wait","kill","stdin"]},
"id":{"type":"integer","description":"Job id (from the bash result or list)"},
"lines":{"type":"integer","description":"For output: how many trailing lines (default 100)"},
"timeout_ms":{"type":"integer","description":"For wait: how long to wait (default 120000, max 600000)"},
"text":{"type":"string","description":"For stdin: the line to write to the running job"}},
"required":["action"],"additionalProperties":false}`)
}

func (t JobTool) Run(ctx context.Context, input json.RawMessage) (string, error) {
	var in struct {
		Action    string `json:"action"`
		ID        int    `json:"id"`
		Lines     int    `json:"lines"`
		TimeoutMS int    `json:"timeout_ms"`
		Text      string `json:"text"`
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
		return job.Summary() + "\n" + textfmt.Truncate(out), nil
	case "wait":
		timeout := clampTimeout(in.TimeoutMS, 2*time.Minute, 10*time.Minute)
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
		return job.Summary() + "\n" + textfmt.Truncate(out), nil
	case "kill":
		if err := t.Jobs.Kill(in.ID); err != nil {
			return "", err
		}
		return fmt.Sprintf("job %d killed", in.ID), nil
	case "stdin":
		if err := t.Jobs.Write(in.ID, in.Text); err != nil {
			return "", err
		}
		return fmt.Sprintf("wrote to job %d stdin", in.ID), nil
	}
	return "", fmt.Errorf("unknown action %q", in.Action)
}
