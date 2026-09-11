package tool

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"sync"
	"time"
)

// A command started in the foreground and then found to be long — a build,
// a test suite, a server someone forgot to mark background — used to leave
// two choices: wait for it, or cancel it and lose the work it had done.
// Promotion is the third: the command keeps running, its output keeps
// being collected, and it becomes a job like any other, so the turn can
// carry on and come back to it.
//
// Running is the registry of foreground commands that can be promoted.
// A nil *Running disables promotion; nothing else changes.
type Running struct {
	mu   sync.Mutex
	next int
	live map[int]*foreground
}

type foreground struct {
	id      int
	command string
	started time.Time
	out     *swapBuffer
	cmd     *exec.Cmd
	promote chan struct{}
	// handed carries the job id the command became, or 0 when the
	// handover did not happen, so a caller that asked for the promotion
	// can name the job it got.
	handed   chan int
	promoted bool
}

// hand reports the outcome of a promotion without ever blocking the
// command's own goroutine on a caller that stopped listening.
func (f *foreground) hand(job int) {
	if f == nil {
		return
	}
	select {
	case f.handed <- job:
	default:
	}
}

// Foreground is one running command, for a caller deciding what to promote.
type Foreground struct {
	ID      int
	Command string
	Started time.Time
	Pid     int
}

// List returns the foreground commands that could be promoted, oldest first.
func (r *Running) List() []Foreground {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Foreground, 0, len(r.live))
	for _, f := range r.live {
		pid := 0
		if f.cmd.Process != nil {
			pid = f.cmd.Process.Pid
		}
		out = append(out, Foreground{ID: f.id, Command: f.command, Started: f.started, Pid: pid})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// promoteWait bounds how long Promote waits for the tool call to hand the
// command over. The handover is a channel send and an Adopt; a second is
// already generous, and a caller must not be left hanging because the
// command exited on its own in the same instant.
const promoteWait = time.Second

// Promote asks the command to detach into the jobs registry. It returns
// the job id once the tool call has handed it over, so the caller can say
// which job the command became. An id that is not running, or one already
// promoted, is an error.
func (r *Running) Promote(id int) (int, error) {
	if r == nil {
		return 0, errors.New("promotion is not available in this session")
	}
	r.mu.Lock()
	f := r.live[id]
	if f == nil {
		r.mu.Unlock()
		return 0, fmt.Errorf("no foreground command %d", id)
	}
	if f.promoted {
		r.mu.Unlock()
		return 0, fmt.Errorf("command %d is already being promoted", id)
	}
	f.promoted = true
	r.mu.Unlock()
	close(f.promote)
	select {
	case job := <-f.handed:
		if job == 0 {
			return 0, fmt.Errorf("command %d could not be moved to the background", id)
		}
		return job, nil
	case <-time.After(promoteWait):
		return 0, fmt.Errorf("command %d did not hand over", id)
	}
}

// PromoteLatest promotes the most recently started foreground command,
// which is what a key press means when there is one.
func (r *Running) PromoteLatest() (int, error) {
	list := r.List()
	if len(list) == 0 {
		return 0, errors.New("no command is running in the foreground")
	}
	return r.Promote(list[len(list)-1].ID)
}

func (r *Running) add(command string, cmd *exec.Cmd, out *swapBuffer) *foreground {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.next++
	if r.live == nil {
		r.live = map[int]*foreground{}
	}
	f := &foreground{id: r.next, command: command, started: time.Now(), out: out, cmd: cmd, promote: make(chan struct{}), handed: make(chan int, 1)}
	r.live[f.id] = f
	return f
}

func (r *Running) remove(f *foreground) {
	if r == nil || f == nil {
		return
	}
	r.mu.Lock()
	delete(r.live, f.id)
	r.mu.Unlock()
}

// promoteChan is f's promotion signal, or nil when there is no registry —
// a nil channel blocks forever in a select, which is what "never" means.
func promoteChan(f *foreground) <-chan struct{} {
	if f == nil {
		return nil
	}
	return f.promote
}

// swapBuffer collects a command's output in memory and can start writing
// it to a file as well, from the beginning, without the command noticing.
// That is what makes a running command promotable: its output does not
// have to be redirected before anyone knows the command will be long.
type swapBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
	f   *os.File
}

func (s *swapBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.f != nil {
		if _, err := s.f.Write(p); err != nil {
			return 0, err
		}
		return len(p), nil
	}
	return s.buf.Write(p)
}

func (s *swapBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.String()
}

// attach starts writing to f, beginning with everything collected so far.
func (s *swapBuffer) attach(f *os.File) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := f.Write(s.buf.Bytes()); err != nil {
		return err
	}
	s.f = f
	return nil
}
