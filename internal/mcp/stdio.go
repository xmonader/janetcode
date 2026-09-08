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
	cmd.Env = os.Environ()
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

func (t *stdioTransport) readLoop(r io.Reader) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 1<<16), 64<<20)
	for sc.Scan() {
		t.c.dispatchRaw(sc.Bytes())
	}
}

func (t *stdioTransport) close() {
	t.stdin.Close()
	select {
	case <-t.done:
	case <-time.After(3 * time.Second):
		if t.cmd.Process != nil {
			t.cmd.Process.Kill()
		}
		<-t.done
	}
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
