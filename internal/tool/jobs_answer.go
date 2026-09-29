package tool

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
)

// isNilFile reports a typed nil *os.File inside an interface, which is
// not == nil and would be written to.
func isNilFile(w io.Writer) bool {
	f, ok := w.(*os.File)
	return ok && f == nil
}

// logSize is how much a job has logged so far.
func (j *Jobs) logSize(id int) int64 {
	job := j.find(id)
	if job == nil {
		return 0
	}
	st, err := os.Stat(job.OutPath)
	if err != nil {
		return 0
	}
	return st.Size()
}

// settleQuiet is how long a job's log must stop growing before what it
// printed counts as its whole answer.
const settleQuiet = 400 * time.Millisecond

// Settle waits for what a job prints after offset from — the next prompt,
// after an answer — and returns it as text. It returns once the log has
// grown and then been quiet for settleQuiet, once the job has ended, or
// after max, whichever is first; "" means it printed nothing in that time.
func (j *Jobs) Settle(ctx context.Context, id int, from int64, max time.Duration) string {
	job := j.find(id)
	if job == nil {
		return ""
	}
	deadline := time.Now().Add(max)
	last, lastChange := from, time.Now()
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return j.since(job, from)
		case <-job.done:
			return j.since(job, from)
		case <-time.After(50 * time.Millisecond):
		}
		n := j.logSize(id)
		if n != last {
			last, lastChange = n, time.Now()
			continue
		}
		if n > from && time.Since(lastChange) >= settleQuiet {
			break
		}
	}
	return j.since(job, from)
}

// since reads a job's log from offset from, at most maxOutputTail bytes,
// as text.
func (j *Jobs) since(job *Job, from int64) string {
	f, err := os.Open(job.OutPath)
	if err != nil {
		return ""
	}
	defer f.Close()
	if st, err := f.Stat(); err == nil && st.Size()-from > maxOutputTail {
		from = st.Size() - maxOutputTail
	}
	data, _ := io.ReadAll(io.NewSectionReader(f, from, maxOutputTail))
	s := string(data)
	if job.Terminal {
		s = termText(s)
	}
	return strings.TrimRight(s, "\n")
}

// termText turns what a terminal was sent into the text it would show:
// escapes removed, CRLF as a newline, and a line rewritten with a bare
// carriage return (a progress bar, a spinner) kept only as it ended.
func termText(s string) string {
	s = stripANSI(strings.ReplaceAll(s, "\r\n", "\n"))
	if !strings.Contains(s, "\r") {
		return s
	}
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		if k := strings.LastIndexByte(strings.TrimRight(l, "\r"), '\r'); k >= 0 {
			lines[i] = l[k+1:]
		}
		lines[i] = strings.TrimRight(lines[i], "\r")
	}
	return strings.Join(lines, "\n")
}

// CloseInput ends a running job's input. A pipe is closed, so the job
// reads end-of-file; a terminal is sent Ctrl-D, which is how a terminal
// says the same thing (and the terminal stays open for the job's output).
func (j *Jobs) CloseInput(ctx context.Context, id int) error {
	job, ok := j.Get(id)
	if !ok {
		return fmt.Errorf("no job %d", id)
	}
	if job.Terminal {
		return j.WriteRaw(ctx, id, "\x04")
	}
	p := j.find(id)
	j.mu.Lock()
	defer j.mu.Unlock()
	if p.Done {
		return fmt.Errorf("job %d already finished", id)
	}
	if p.stdin == nil {
		return fmt.Errorf("job %d does not accept stdin", id)
	}
	_ = p.stdin.Close()
	p.stdin = nil
	p.stdinErr = fmt.Errorf("job %d's input was ended (eof: true); it reads nothing more", id)
	return nil
}
