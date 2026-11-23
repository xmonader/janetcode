package headless

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/xmonader/janetcode/internal/textfmt"
	"github.com/xmonader/janetcode/internal/tool"
)

// A command started with bash background: true outlives the turn that began
// it. In an interactive session that is the point; in a -p run the process
// exits with the last turn, so a build or a server the model started "to
// finish later" was killed the moment the answer was printed and the caller
// never learned whether it worked. The run now waits for such jobs, says
// which it is waiting for, and reports each job's start and end on the
// stream-json event stream.

// bgJobEvent is the stream-json line for a background job. It is its own
// type rather than a streamEvent: the job id is a number there and
// exit_code must be present (even 0) once the job has ended.
type bgJobEvent struct {
	Type    string `json:"type"`
	ID      int    `json:"id"`
	Command string `json:"command"`
	// Status is running, finished, failed or killed.
	Status      string  `json:"status"`
	ExitCode    *int    `json:"exit_code,omitempty"`
	ElapsedSecs float64 `json:"elapsed_secs"`
}

// jobEvent turns a job snapshot into its event line.
func jobEvent(j tool.Job) bgJobEvent {
	ev := bgJobEvent{Type: "background_job", ID: j.ID, Command: j.Command, Status: "running"}
	if !j.Done {
		ev.ElapsedSecs = time.Since(j.Started).Seconds()
		return ev
	}
	code := j.Exit
	ev.ExitCode = &code
	ev.ElapsedSecs = j.Ended.Sub(j.Started).Seconds()
	switch {
	case j.Killed:
		ev.Status = "killed"
	case j.Exit != 0:
		ev.Status = "failed"
	default:
		ev.Status = "finished"
	}
	return ev
}

// observeJobs streams the session's background jobs as they start and end.
// It returns the function that stops listening.
func observeJobs(opts Options, rec *recorder) func() {
	if opts.Agent == nil || opts.Agent.Jobs == nil || rec.enc == nil {
		return func() {}
	}
	return opts.Agent.Jobs.Observe(func(j tool.Job) {
		rec.mu.Lock()
		defer rec.mu.Unlock()
		if rec.enc != nil {
			_ = rec.enc.Encode(jobEvent(j))
		}
	})
}

// settleBackground runs after the final turn. Unless the run failed or
// waiting was switched off, it waits for the jobs still running; whatever
// is left afterwards (cancel, wall time, no-wait, a failed run) is killed
// here, while the stream is still open, so every job gets its end line
// before the result. Context cancellation is the user's Ctrl+C and is
// reported as such; the wall-time limit only stops the wait, because the
// turns themselves finished in time.
func settleBackground(ctx context.Context, opts Options, err error) error {
	if opts.Agent == nil || opts.Agent.Jobs == nil {
		return err
	}
	jobs := opts.Agent.Jobs
	running := jobs.Running()
	if len(running) == 0 {
		return err
	}
	if err == nil && !opts.NoWaitBackground && ctx.Err() == nil {
		var names []string
		for _, j := range running {
			names = append(names, fmt.Sprintf("%d (%s)", j.ID, textfmt.ClipBytes(j.Command, 60)))
		}
		if opts.Stderr != nil {
			fmt.Fprintf(opts.Stderr, "[background] waiting for %d job(s): %s (Ctrl+C stops them)\n", len(running), strings.Join(names, ", "))
		}
		for _, j := range running {
			if _, werr := jobs.Wait(ctx, j.ID); werr != nil {
				break
			}
		}
		if ctx.Err() != nil && !errors.Is(context.Cause(ctx), ErrWallTime) {
			err = ctx.Err()
		}
	}
	jobs.KillAll()
	// Kill signals; the end line comes from the reaper, so give it a
	// moment to land before the result is written.
	grace, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	for _, j := range running {
		_, _ = jobs.Wait(grace, j.ID)
	}
	return err
}
