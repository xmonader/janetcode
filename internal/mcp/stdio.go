package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/xmonader/janetcode/internal/pipewrite"
	"github.com/xmonader/janetcode/internal/procgroup"
	"github.com/xmonader/janetcode/internal/shell"
)

// stdioTransport runs the server as a child process and exchanges
// newline-delimited JSON over its stdin and stdout.
type stdioTransport struct {
	c       *Client
	cmd     *exec.Cmd
	stdin   io.WriteCloser
	writeMu sync.Mutex
	// broken is set when an abandoned write left part of a frame on the
	// wire. The server would read the next frame as the rest of that one,
	// so the connection is over and every later call says so.
	broken error
	stderr *ringBuffer
	done   chan struct{}
}

func newStdioTransport(c *Client, cfg ServerConfig, root string) (*stdioTransport, error) {
	cmd := exec.Command(cfg.Command, cfg.Args...)
	cmd.Dir = cfg.Cwd
	if cmd.Dir == "" {
		cmd.Dir = root
	}
	cmd.Env = shell.ScrubEnv(os.Environ())
	for k, v := range cfg.Env {
		cmd.Env = append(cmd.Env, k+"="+os.ExpandEnv(v))
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	t := &stdioTransport{c: c, cmd: cmd, stdin: stdin, stderr: &ringBuffer{max: 4096}, done: make(chan struct{})}
	cmd.Stderr = t.stderr
	// Servers are usually launched through a wrapper (npx, uvx, a shell
	// one-liner), so the process janetcode starts is rarely the server.
	// Its own group is what makes close() able to reach the real one.
	procgroup.Set(cmd)
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start %q: %w", cfg.Command, err)
	}
	go t.readLoop(stdout)
	go func() {
		err := cmd.Wait()
		close(t.done)
		c.exited(err)
	}()
	return t, nil
}

// send writes one frame, bounded by ctx. A pipe write has no deadline of
// its own, and without a bound one server that stopped reading (a full
// 64 KB pipe) hangs every caller past cancellation — and because such a
// call rides the parallel read-only batch, the whole turn with it.
//
// The bound must not merely stop waiting. Writing on a goroutine and
// returning on ctx.Done left the frame parked: it completed when the
// server next drained stdin, so a tools/call the model had already been
// told had failed was delivered and run — a deploy that happened two
// seconds after the harness reported it had not. pipewrite pushes the
// pipe's write deadline instead, so an abandoned frame either never
// reached the server at all (the call did not happen, and the connection
// is still good) or reached it in part, which desyncs the protocol and
// ends the connection.
func (t *stdioTransport) send(ctx context.Context, msg rpcMessage) error {
	data, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	t.writeMu.Lock()
	defer t.writeMu.Unlock()
	if t.broken != nil {
		return t.broken
	}
	_, err = pipewrite.Write(ctx, t.stdin, data)
	var partial *pipewrite.PartialError
	if errors.As(err, &partial) {
		// The server holds the front of a frame and would read the next
		// one as its tail. Close stdin so it sees EOF and stops, rather
		// than acting on a spliced request.
		t.broken = fmt.Errorf("mcp: connection unusable: %w", partial)
		_ = t.stdin.Close()
		return t.broken
	}
	return err
}

// readLoop reads frames until the stream ends. A scanner error — a read
// that failed, or a frame past maxFrameBytes — ends it just as quietly as
// EOF does, and the process may well still be running, so nothing else
// would ever notice: every call in flight and every call after it would
// wait out its own timeout against a connection that can no longer
// answer. It is reported as the disconnection it is.
func (t *stdioTransport) readLoop(r io.Reader) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 1<<16), maxFrameBytes)
	for sc.Scan() {
		t.c.dispatchRaw(sc.Bytes())
	}
	if err := sc.Err(); err != nil {
		t.c.fail(fmt.Errorf("%w: reading from the server: %v", ErrDisconnected, err))
	}
}

// close ends the server and everything it started.
//
// Closing stdin is the protocol's way of saying goodbye, but it only
// reaches the wrapper: an `npx server` that exits on EOF leaves the server
// it forked running, reparented to init, for the rest of the machine's
// uptime. So the group is signalled as well, and while the wrapper is
// still alive — that is what makes the group id certainly ours.
func (t *stdioTransport) close() {
	t.stdin.Close()
	procgroup.Stop(t.cmd, t.done, 3*time.Second)
	<-t.done
}

func (t *stdioTransport) hint() string {
	if s := strings.TrimSpace(t.stderr.String()); s != "" {
		return "\nserver stderr:\n" + s
	}
	return ""
}

// ringBuffer keeps the tail of a stream for error messages.
type ringBuffer struct {
	mu  sync.Mutex
	buf []byte
	max int
}

func (r *ringBuffer) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.buf = append(r.buf, p...)
	if len(r.buf) > r.max {
		r.buf = r.buf[len(r.buf)-r.max:]
	}
	return len(p), nil
}

func (r *ringBuffer) String() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return string(r.buf)
}
