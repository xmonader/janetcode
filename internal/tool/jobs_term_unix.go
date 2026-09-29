//go:build unix

package tool

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"syscall"
	"time"

	"github.com/creack/pty"
)

var errNoTerminal = errors.New("no pseudo-terminal on this system")

// startTerminal starts a job on a pseudo-terminal: the command's stdin,
// stdout and stderr are the terminal, it is the terminal's session leader
// (so /dev/tty is this terminal, and a password prompt reaches the job
// tool instead of failing), and what it shows is copied into the log.
func (j *Jobs) startTerminal(id int, command string, cmd *exec.Cmd, out *os.File) (*Job, error) {
	// Programs look at TERM to decide how much terminal to use. dumb
	// keeps them to plain lines, which is what a log can hold.
	env := cmd.Env
	if env == nil {
		env = os.Environ()
	}
	cmd.Env = append(append([]string(nil), env...), "TERM=dumb", "COLUMNS=120", "LINES=40")
	attrs := &syscall.SysProcAttr{}
	if cmd.SysProcAttr != nil {
		*attrs = *cmd.SysProcAttr
	}
	attrs.Setpgid = false
	attrs.Setsid = true
	attrs.Setctty = true
	attrs.Ctty = 0 // the child's stdin, which is the terminal
	ptmx, err := pty.StartWithAttrs(cmd, &pty.Winsize{Rows: 40, Cols: 120}, attrs)
	if err != nil {
		out.Close()
		os.Remove(out.Name())
		if errors.Is(err, pty.ErrUnsupported) {
			return nil, errNoTerminal
		}
		return nil, fmt.Errorf("start on a terminal: %w", err)
	}
	copied := make(chan struct{})
	go func() {
		// Ends with EIO once every process holding the terminal has
		// closed it.
		_, _ = io.Copy(out, ptmx)
		close(copied)
	}()
	job := &Job{ID: id, Command: command, Pid: cmd.Process.Pid, Started: time.Now(), OutPath: out.Name(), cmd: cmd, done: make(chan struct{}), stdin: ptmx, Terminal: true}
	j.mu.Lock()
	j.jobs = append(j.jobs, job)
	j.mu.Unlock()
	go j.reap(job, cmd.Wait, func() {
		// What the command printed last may still be in the terminal's
		// buffer when it exits. Wait for the copy to drain it, but not
		// for ever: a process it left behind can hold the terminal open.
		select {
		case <-copied:
		case <-time.After(2 * time.Second):
		}
		out.Close()
	})
	return job, nil
}
