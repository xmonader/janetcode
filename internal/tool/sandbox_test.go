package tool

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

func TestLocalShellSkipsSandboxWhenOff(t *testing.T) {
	cmd, err := localShell(context.Background(), nil, nil, t.TempDir(), "echo hi")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.Join(cmd.Args, " "), "docker") {
		t.Fatalf("unwrapped command used docker: %v", cmd.Args)
	}
}
