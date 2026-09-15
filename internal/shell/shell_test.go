package shell

import (
	"context"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
	"unicode/utf16"
)

func TestShellForPicksByOS(t *testing.T) {
	only := func(names ...string) func(string) (string, error) {
		return func(n string) (string, error) {
			for _, x := range names {
				if x == n {
					return "/bin/" + n, nil
				}
			}
			return "", errors.New("not found")
		}
	}
	if s := shellFor("linux", only("bash", "sh")); s.Name != "bash" || s.Path != "/bin/bash" || strings.Join(s.Args, " ") != "-c" {
		t.Fatalf("linux: %+v", s)
	}
	if s := shellFor("linux", only("sh")); s.Name != "sh" || s.Path != "sh" {
		t.Fatalf("linux without bash: %+v", s)
	}
	if s := shellFor("windows", only("powershell")); s.Name != "PowerShell" || s.Path != "/bin/powershell" || !s.encoded {
		t.Fatalf("windows: %+v", s)
	}
	if s := shellFor("windows", only("pwsh", "powershell")); s.Path != "/bin/pwsh" {
		t.Fatalf("windows with pwsh: %+v", s)
	}
	if s := shellFor("windows", only()); s.Path != "powershell" {
		t.Fatalf("windows with nothing on PATH: %+v", s)
	}
}

func TestPowerShellCommandIsEncodedWithExitStatus(t *testing.T) {
	s := shellFor("windows", func(string) (string, error) { return "", errors.New("nope") })
	cmd := s.Command(context.Background(), `go test ./... "quoted arg" $env:HOME`)
	args := cmd.Args
	if args[0] != "powershell" || args[len(args)-2] != "-EncodedCommand" {
		t.Fatalf("args = %v", args)
	}
	raw, err := base64.StdEncoding.DecodeString(args[len(args)-1])
	if err != nil {
		t.Fatal(err)
	}
	u := make([]uint16, len(raw)/2)
	for i := range u {
		u[i] = uint16(raw[2*i]) | uint16(raw[2*i+1])<<8
	}
	script := string(utf16.Decode(u))
	if !strings.Contains(script, `go test ./... "quoted arg" $env:HOME`) || !strings.Contains(script, "exit $LASTEXITCODE") {
		t.Fatalf("script:\n%s", script)
	}
}

func TestShellCommandRunsHere(t *testing.T) {
	out, err := Command(context.Background(), "echo shell-ok").CombinedOutput()
	if err != nil || !strings.Contains(string(out), "shell-ok") {
		t.Fatalf("out=%q err=%v", out, err)
	}
	if n := Default().Name; n != "bash" && n != "sh" && n != "PowerShell" {
		t.Fatalf("shell name %q", n)
	}
}
