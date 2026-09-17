package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

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
	stderr  *ringBuffer
	done    chan struct{}
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

func (t *stdioTransport) send(_ context.Context, msg rpcMessage) error {
	data, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	t.writeMu.Lock()
	defer t.writeMu.Unlock()
	_, err = t.stdin.Write(append(data, '\n'))
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
