package main

import (
	"bufio"
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/config"
	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/session"
)

// The helper process: another janetcode holding a session's lease.
func TestLeaseHolderHelper(t *testing.T) {
	dir, id := os.Getenv("JANETCODE_LEASE_DIR"), os.Getenv("JANETCODE_LEASE_ID")
	if dir == "" {
		t.Skip("helper for TestResumeOfALeasedSession")
	}
	s, _, err := session.Open(dir, id)
	if err != nil {
		os.Exit(1)
	}
	session.SetHolder("the terminal interface")
	if err := s.Acquire(); err != nil {
		os.Exit(1)
	}
	os.Stdout.WriteString("held\n")
	_, _ = bufio.NewReader(os.Stdin).ReadString('\n')
	os.Exit(0)
}

// A --resume of a session another process is running is refused with who
// has it and the two ways on; --fork continues in a copy, and --show
// reads it without taking it.
func TestResumeOfALeasedSession(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "data"))
	work := t.TempDir()
	dir, err := session.Dir()
	if err != nil {
		t.Fatal(err)
	}
	s, err := session.New(dir, work, "p", "m")
	if err != nil {
		t.Fatal(err)
	}
	msg := llm.TextMessage(llm.RoleUser, "the conversation so far")
	if _, err := s.Append(session.Event{Type: session.TypeUserMessage, Surface: true, Message: &msg}); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command(os.Args[0], "-test.run=^TestLeaseHolderHelper$")
	cmd.Env = append(os.Environ(), "JANETCODE_LEASE_DIR="+dir, "JANETCODE_LEASE_ID="+s.Meta.ID)
	stdin, _ := cmd.StdinPipe()
	out, _ := cmd.StdoutPipe()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { stdin.Close(); _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	if line, _ := bufio.NewReader(out).ReadString('\n'); strings.TrimSpace(line) != "held" {
		t.Fatalf("helper said %q", line)
	}

	cfg := &config.Config{Provider: "p", Model: "m"}
	_, _, err = openSession(&options{resume: s.Meta.ID}, cfg, dir, work)
	var le *session.LeasedError
	if !errors.As(err, &le) {
		t.Fatalf("resume of a held session = %v, want a lease refusal", err)
	}
	for _, want := range []string{"the terminal interface", "--resume " + s.Meta.ID + " --fork", "--show " + s.Meta.ID} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal %q does not mention %q", err, want)
		}
	}

	fork, hist, err := openSession(&options{resume: s.Meta.ID, fork: true}, cfg, dir, work)
	if err != nil {
		t.Fatalf("--fork: %v", err)
	}
	defer fork.Release()
	if fork.Meta.ID == s.Meta.ID || fork.Meta.ForkOf != s.Meta.ID || len(hist) != 1 {
		t.Fatalf("--fork opened %s (fork of %q) with %d messages", fork.Meta.ID, fork.Meta.ForkOf, len(hist))
	}

	var buf bytes.Buffer
	if err := showSession(&buf, s.Meta.ID); err != nil {
		t.Fatalf("--show of a held session: %v", err)
	}
	if !strings.Contains(buf.String(), "the conversation so far") {
		t.Errorf("--show printed %q", buf.String())
	}
}
