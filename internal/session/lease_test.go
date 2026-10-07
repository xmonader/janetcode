package session

import (
	"bufio"
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"
)

// The helper process: holds the lease of the session named by the
// environment, says so on stdout, and keeps it until killed or its stdin
// closes.
func TestLeaseHelperProcess(t *testing.T) {
	dir, id := os.Getenv("JANETCODE_LEASE_DIR"), os.Getenv("JANETCODE_LEASE_ID")
	if dir == "" {
		t.Skip("helper for TestLease*")
	}
	s, _, err := Open(dir, id)
	if err != nil {
		os.Stdout.WriteString("error " + err.Error() + "\n")
		os.Exit(1)
	}
	SetHolder("the helper")
	if err := s.Acquire(); err != nil {
		os.Stdout.WriteString("error " + err.Error() + "\n")
		os.Exit(1)
	}
	os.Stdout.WriteString("held\n")
	_, _ = bufio.NewReader(os.Stdin).ReadString('\n')
	os.Exit(0)
}

func leasedSession(t *testing.T) (*Session, string) {
	t.Helper()
	dir := t.TempDir()
	s, err := New(dir, "/work", "p", "m")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.EnsureWritten(); err != nil {
		t.Fatal(err)
	}
	return s, dir
}

func startHolder(t *testing.T, dir, id string) *exec.Cmd {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestLeaseHelperProcess$")
	cmd.Env = append(os.Environ(), "JANETCODE_LEASE_DIR="+dir, "JANETCODE_LEASE_ID="+id)
	stdin, _ := cmd.StdinPipe()
	out, _ := cmd.StdoutPipe()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { stdin.Close(); _ = cmd.Process.Kill(); _ = cmd.Wait() })
	line, _ := bufio.NewReader(out).ReadString('\n')
	if strings.TrimSpace(line) != "held" {
		t.Fatalf("helper: %q", line)
	}
	return cmd
}

// A second process gets a refusal naming the holder, and a holder that
// dies — SIGKILL, no chance to clean up — leaves no lock behind.
func TestLeaseRefusesSecondProcessAndSurvivesCrash(t *testing.T) {
	s, dir := leasedSession(t)
	cmd := startHolder(t, dir, s.Meta.ID)

	err := s.Acquire()
	var le *LeasedError
	if !errors.As(err, &le) {
		t.Fatalf("Acquire while another process holds it = %v, want *LeasedError", err)
	}
	if le.Info.PID != cmd.Process.Pid || le.Info.Holder != "the helper" {
		t.Errorf("holder = %+v, want pid %d, the helper", le.Info, cmd.Process.Pid)
	}
	if !strings.Contains(err.Error(), "the helper") || !strings.Contains(err.Error(), s.Meta.ID) {
		t.Errorf("message %q should name the session and the holder", err)
	}
	if in, ok := Holder(s.Path()); !ok || in.PID != cmd.Process.Pid {
		t.Errorf("Holder = %+v, %v", in, ok)
	}

	_ = cmd.Process.Kill()
	_ = cmd.Wait()
	if err := s.Acquire(); err != nil {
		t.Fatalf("Acquire after the holder was killed: %v", err)
	}
	s.Release()
	if _, ok := Holder(s.Path()); ok {
		t.Error("Holder reports a lease after Release")
	}
}

// Inside one process the lease is shared, and only the last release gives
// it up.
func TestLeaseSharedInProcess(t *testing.T) {
	s, dir := leasedSession(t)
	s2, _, err := Open(dir, s.Meta.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Acquire(); err != nil {
		t.Fatal(err)
	}
	if err := s2.Acquire(); err != nil {
		t.Fatalf("second Session of the same process: %v", err)
	}
	s.Release()
	if !s2.Leased() {
		t.Fatal("s2 lost its lease")
	}
	if _, ok := Holder(s.Path()); !ok {
		t.Error("released while another Session of the process still holds it")
	}
	s2.Release()
	if _, ok := Holder(s.Path()); ok {
		t.Error("still held after both released")
	}
}

// The lock file names the holder, so a refusal can say who it is.
func TestLeaseInfoDescribe(t *testing.T) {
	in := LeaseInfo{PID: 12, Host: "elsewhere", Holder: "the web interface"}
	if got := in.describe(); !strings.Contains(got, "the web interface") || !strings.Contains(got, "pid 12 on elsewhere") {
		t.Errorf("describe = %q", got)
	}
	if !in.alive() {
		t.Error("a holder on another host must count as alive")
	}
	if (LeaseInfo{PID: 0}).alive() {
		t.Error("no pid is not alive")
	}
}
