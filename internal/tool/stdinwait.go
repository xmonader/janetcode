package tool

import (
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"
)

// inputPipe is the write end of a foreground command's stdin, held open
// so the command's read of it can be seen instead of answered with EOF at
// once. Exactly one of two things happens to it: it is closed (the
// command gets its EOF, as a command always used to) or it is handed to a
// background job that will write to it. The two race — a watcher closing
// it while the command is promoted — so both go through the lock.
type inputPipe struct {
	mu     sync.Mutex
	w      *os.File
	closed bool
	handed bool
	// readEOF records that the command read its stdin and was given EOF
	// by the watcher, so the result can say so.
	readEOF bool
}

// eof closes the pipe unless it was handed on. It reports whether it
// closed it now.
func (p *inputPipe) eof() bool {
	if p == nil {
		return false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed || p.handed {
		return false
	}
	p.closed = true
	_ = p.w.Close()
	return true
}

// hand gives the pipe to a job; nil if it is already closed.
func (p *inputPipe) hand() *os.File {
	if p == nil {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed || p.handed {
		return nil
	}
	p.handed = true
	return p.w
}

func (p *inputPipe) sawRead() bool {
	if p == nil {
		return false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.readEOF
}

// Timing of the watch. A command has to have printed nothing for quiet
// before it is looked at, and a program that polls its input rather than
// blocking in read (node, anything on an event loop) must have been
// quiet for pollQuiet as well, since that alone says less.
const (
	stdinTick      = 100 * time.Millisecond
	stdinQuiet     = 300 * time.Millisecond
	stdinPollQuiet = 1500 * time.Millisecond
)

// inputState is what the process tree of a command is doing with its
// stdin.
type inputState struct {
	// reading: a process is blocked in read(2) on the pipe.
	reading bool
	// polling: a process holding the pipe as stdin sleeps in poll,
	// select or epoll — it may be waiting on it — and none is running.
	polling bool
	// opaque: a process holding the pipe sleeps somewhere the kernel
	// will not say.
	opaque bool
}

// watchStdin watches a running command for a read of its stdin. When the
// command is blocked reading and the end of its output looks like a
// question, waiting receives that output and the watch ends: the caller
// decides what to do with a command that wants an answer. When it reads
// without having asked anything (cat, a filter, a test reading an empty
// stdin), it is given EOF, as it always was, and the watch ends. canWait
// false means nobody can answer, so every read gets EOF.
func watchStdin(cmd *exec.Cmd, pipe *inputPipe, ino uint64, out *swapBuffer, canWait bool, waiting chan<- string, stop <-chan struct{}) {
	t := time.NewTicker(stdinTick)
	defer t.Stop()
	lastN, lastChange := int64(-1), time.Now()
	var lastProbe time.Time
	for {
		select {
		case <-stop:
			return
		case <-t.C:
		}
		n, tail := out.progress()
		if n != lastN {
			lastN, lastChange = n, time.Now()
			continue
		}
		quiet := time.Since(lastChange)
		if quiet < stdinQuiet {
			continue
		}
		// A long quiet stretch is usually a build or a test run thinking,
		// not a question; look at /proc once a second then, not ten times.
		if quiet > 5*time.Second && time.Since(lastProbe) < time.Second {
			continue
		}
		lastProbe = time.Now()
		st := probeStdin(cmd.Process.Pid, ino)
		switch {
		case st.reading && canWait && looksLikePrompt(tail, true):
			waiting <- tail
			return
		case st.reading:
			if pipe.eof() {
				pipe.mu.Lock()
				pipe.readEOF = true
				pipe.mu.Unlock()
			}
			return
		case st.polling && canWait && quiet >= stdinPollQuiet && looksLikePrompt(tail, false):
			waiting <- tail
			return
		case st.opaque:
			pipe.eof()
			return
		}
	}
}

// promptEnd is how a question ends: ? : > ] ) or a y/n choice.
var promptEnd = regexp.MustCompile(`(?i)([?:>\])]|\[y/n\]|\(y/n\)|y/n)$`)

// looksLikePrompt reports whether output ends the way a program asking
// for input ends. A line left unfinished (no newline) is how nearly every
// prompt is printed; a finished line ending in ? or : counts too when the
// program is known to be blocked reading (strong), since some print the
// question and then read on the next line.
func looksLikePrompt(output string, strong bool) bool {
	output = stripANSI(output)
	i := strings.LastIndexByte(output, '\n')
	unfinished := strings.TrimSpace(output[i+1:])
	if unfinished != "" {
		return strong || promptEnd.MatchString(unfinished)
	}
	if !strong {
		return false
	}
	last := strings.TrimSpace(output)
	if j := strings.LastIndexByte(last, '\n'); j >= 0 {
		last = last[j+1:]
	}
	return strings.HasSuffix(last, "?") || strings.HasSuffix(last, ":")
}

// promptTail is the last few lines of the output, for the result that
// tells the model what the command asked.
func promptTail(output string) string {
	output = strings.TrimRight(stripANSI(strings.ReplaceAll(output, "\r\n", "\n")), " \n")
	lines := strings.Split(output, "\n")
	if len(lines) > 12 {
		lines = lines[len(lines)-12:]
	}
	return strings.Join(lines, "\n")
}
