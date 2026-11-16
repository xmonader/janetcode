package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xmonader/janetcode/internal/headless"
	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/session"
	"github.com/xmonader/janetcode/internal/sessionproto"
)

type lockedBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuf) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

func (l *lockedBuf) waitFor(t *testing.T, sub string) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if strings.Contains(l.String(), sub) {
			return
		}
	}
	t.Fatalf("output never contained %q:\n%s", sub, l.String())
}

func say(t *testing.T, s *session.Session, role llm.Role, text string) {
	t.Helper()
	m := llm.TextMessage(role, text)
	typ := session.TypeUserMessage
	if role == llm.RoleAssistant {
		typ = session.TypeAssistant
	}
	if _, err := s.Append(session.Event{Type: typ, Surface: true, Message: &m}); err != nil {
		t.Fatal(err)
	}
}

// startOwner is another janetcode holding the session's lease: the
// helper of lease_test.go.
func startOwner(t *testing.T, dir, id string) *exec.Cmd {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestLeaseHolderHelper$")
	cmd.Env = append(os.Environ(), "JANETCODE_LEASE_DIR="+dir, "JANETCODE_LEASE_ID="+id)
	stdin, _ := cmd.StdinPipe()
	out, _ := cmd.StdoutPipe()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { stdin.Close(); _ = cmd.Process.Kill(); _ = cmd.Wait() })
	if line, _ := bufio.NewReader(out).ReadString('\n'); strings.TrimSpace(line) != "held" {
		t.Fatalf("helper: %q", line)
	}
	return cmd
}

func followEnv(t *testing.T) (string, *session.Session) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "data"))
	dir, err := session.Dir()
	if err != nil {
		t.Fatal(err)
	}
	s, err := session.New(dir, t.TempDir(), "p", "m")
	if err != nil {
		t.Fatal(err)
	}
	say(t, s, llm.RoleUser, "refactor the parser")
	return dir, s
}

// Another process owns the session. An observer sees its history and what
// it writes next, takes nothing, and learns when the owner is killed.
func TestFollowWatchesALiveOwnerAndSeesItDie(t *testing.T) {
	dir, s := followEnv(t)
	owner := startOwner(t, dir, s.Meta.ID)

	out := &lockedBuf{}
	done := make(chan error, 1)
	go func() {
		done <- followMode(context.Background(), out, dir, s.Meta.ID, headless.FormatText, 20*time.Millisecond)
	}()
	out.waitFor(t, "following "+s.Meta.ID+" read-only; the terminal interface")
	out.waitFor(t, "you: refactor the parser")

	say(t, s, llm.RoleAssistant, "done, parser split in two")
	out.waitFor(t, "assistant: done, parser split in two")

	// The watcher holds nothing: the owner still has the lease, and a
	// second opener is still refused by it.
	if in, ok := session.Holder(s.Path()); !ok || in.PID != owner.Process.Pid {
		t.Fatalf("holder while watched = %+v %v, want the owner's pid %d", in, ok, owner.Process.Pid)
	}

	_ = owner.Process.Kill()
	_ = owner.Wait()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("--follow kept running after its owner was killed")
	}
	out.waitFor(t, "the session's owner is gone")
	if err := s.Acquire(); err != nil {
		t.Fatalf("lease after the owner's death: %v", err)
	}
	s.Release()
}

func TestFollowStreamJSONSpeaksTheProtocol(t *testing.T) {
	dir, s := followEnv(t)
	owner := startOwner(t, dir, s.Meta.ID)
	out := &lockedBuf{}
	done := make(chan error, 1)
	go func() {
		done <- followMode(context.Background(), out, dir, s.Meta.ID, headless.FormatStreamJSON, 20*time.Millisecond)
	}()
	out.waitFor(t, `"kind":"event"`)
	_ = owner.Process.Kill()
	_ = owner.Wait()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	var kinds []string
	for _, line := range strings.Split(strings.TrimSpace(out.String()), "\n") {
		var m struct {
			Protocol int
			Kind     string
			Held     bool
			Role     string
			Reason   string
			Event    session.Event
		}
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("not JSON: %q", line)
		}
		if m.Protocol != sessionproto.Version {
			t.Fatalf("line %q lacks protocol %d", line, sessionproto.Version)
		}
		kinds = append(kinds, m.Kind)
		switch m.Kind {
		case "lease":
			if !m.Held || m.Role != "observer" {
				t.Fatalf("lease line: %q", line)
			}
		case "event":
			if m.Event.Message == nil || m.Event.Message.Text() != "refactor the parser" {
				t.Fatalf("event line: %q", line)
			}
		case "end":
			if m.Reason != "owner_gone" {
				t.Fatalf("end line: %q", line)
			}
		}
	}
	if len(kinds) != 3 || kinds[0] != "lease" || kinds[1] != "event" || kinds[2] != "end" {
		t.Fatalf("kinds = %v, want lease, event, end", kinds)
	}
}

func TestFollowOfAnIdleSessionPrintsItAndEnds(t *testing.T) {
	dir, s := followEnv(t)
	out := &lockedBuf{}
	if err := followMode(context.Background(), out, dir, s.Meta.ID, headless.FormatText, 10*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"is not held by any janetcode", "you: refactor the parser", "nothing is running this session"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("output lacks %q:\n%s", want, out.String())
		}
	}
}

func TestFollowRefusesWhatIsNotASession(t *testing.T) {
	dir, _ := followEnv(t)
	for _, id := range []string{"no-such-session", "../etc/passwd", ""} {
		if err := followMode(context.Background(), &lockedBuf{}, dir, id, headless.FormatText, 10*time.Millisecond); err == nil {
			t.Fatalf("--follow %q was accepted", id)
		}
	}
}

// A background run's questions show up for the watcher too, from the
// run's status file.
func TestFollowReportsABackgroundRunsStatus(t *testing.T) {
	dir, s := followEnv(t)
	owner := startOwner(t, dir, s.Meta.ID)
	out := &lockedBuf{}
	done := make(chan error, 1)
	go func() {
		done <- followMode(context.Background(), out, dir, s.Meta.ID, headless.FormatText, 20*time.Millisecond)
	}()
	out.waitFor(t, "following")
	st := session.BgStatus{State: session.BgWaiting, PID: owner.Process.Pid, Started: time.Now(), Ask: &session.BgAsk{ID: "a1", Tool: "bash", Summary: "go test ./..."}}
	if err := session.WriteBgStatus(s.Path(), st); err != nil {
		t.Fatal(err)
	}
	out.waitFor(t, "-- background run:")
	_ = owner.Process.Kill()
	_ = owner.Wait()
	<-done
}
