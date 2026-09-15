package shell

import (
	"context"
	"runtime"
	"strings"
	"testing"
)

func TestSandboxOffByDefault(t *testing.T) {
	var s Sandbox
	if s.Enabled() {
		t.Fatal("sandbox must start off")
	}
	if Status := s.Status(); !strings.Contains(Status, "off") {
		t.Fatalf("status = %q", Status)
	}
	if (*Sandbox)(nil).Enabled() {
		t.Fatal("nil sandbox is on")
	}
}

func TestSandboxCommandDocker(t *testing.T) {
	if runtime.GOOS == "windows" || runtime.GOOS == "darwin" {
		t.Skip("docker wrap is the Linux path")
	}
	s := &Sandbox{look: func(name string) (string, error) { return "/usr/bin/" + name, nil }}
	if err := s.Enable(); err != nil {
		t.Fatal(err)
	}
	s.SetImage("alpine:test")
	cmd, err := s.Command(context.Background(), "/work", "echo hi")
	if err != nil {
		t.Fatal(err)
	}
	args := strings.Join(cmd.Args, " ")
	for _, want := range []string{"run", "--rm", "-v /work:/work", "-w /work", "alpine:test", "echo hi"} {
		if !strings.Contains(args, want) {
			t.Errorf("args %q missing %q", args, want)
		}
	}
	s.Disable()
	if s.Enabled() {
		t.Fatal("still on")
	}
}

func TestSetSandboxNilReportsNoSandbox(t *testing.T) {
	if _, err := SetSandbox(nil, ""); err == nil {
		t.Fatal("want error for a nil sandbox")
	}
}

func TestSetSandboxOnOff(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sandbox is not supported on windows")
	}
	s := &Sandbox{look: func(name string) (string, error) { return "/usr/bin/" + name, nil }}
	status, err := SetSandbox(s, "on")
	if err != nil || !strings.Contains(status, "on") {
		t.Fatalf("status=%q err=%v", status, err)
	}
	status, err = SetSandbox(s, "off")
	if err != nil || !strings.Contains(status, "off") {
		t.Fatalf("status=%q err=%v", status, err)
	}
}

func TestSetSandboxUnknownArgIsAnImage(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sandbox is not supported on windows")
	}
	s := &Sandbox{look: func(name string) (string, error) { return "/usr/bin/" + name, nil }}
	status, err := SetSandbox(s, "myimage:latest")
	if err != nil || s.Image() != "myimage:latest" || !s.Enabled() {
		t.Fatalf("status=%q image=%q enabled=%v err=%v", status, s.Image(), s.Enabled(), err)
	}
}
