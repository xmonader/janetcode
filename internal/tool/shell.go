package tool

import (
	"context"
	"encoding/base64"
	"os/exec"
	"runtime"
	"sync"
	"unicode/utf16"
)

// Shell is the interpreter commands run through: bash (or sh) on Unix,
// PowerShell on Windows.
type Shell struct {
	// Name is shown to the model and the user ("bash", "PowerShell").
	Name string
	// Path is the executable; Args come before the command.
	Path string
	Args []string
	// encoded means the command is passed base64 UTF-16LE encoded
	// (PowerShell's -EncodedCommand), which sidesteps quoting rules.
	encoded bool
}

// Command builds the exec.Cmd for one command line.
func (s Shell) Command(ctx context.Context, command string) *exec.Cmd {
	args := append([]string(nil), s.Args...)
	if s.encoded {
		args = append(args, encodePowerShell(command))
	} else {
		args = append(args, command)
	}
	return exec.CommandContext(ctx, s.Path, args...)
}

var (
	shellOnce sync.Once
	shell     Shell
)

// DefaultShell picks the shell for this machine once.
func DefaultShell() Shell {
	shellOnce.Do(func() { shell = shellFor(runtime.GOOS, exec.LookPath) })
	return shell
}

// ShellCommand is DefaultShell().Command.
func ShellCommand(ctx context.Context, command string) *exec.Cmd {
	return DefaultShell().Command(ctx, command)
}

// shellFor chooses by OS and what is installed: pwsh (PowerShell 7) over
// Windows PowerShell 5, bash over sh.
func shellFor(goos string, lookPath func(string) (string, error)) Shell {
	if goos == "windows" {
		path := "powershell"
		if p, err := lookPath("pwsh"); err == nil {
			path = p
		} else if p, err := lookPath("powershell"); err == nil {
			path = p
		}
		return Shell{Name: "PowerShell", Path: path, Args: []string{"-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-EncodedCommand"}, encoded: true}
	}
	if p, err := lookPath("bash"); err == nil {
		return Shell{Name: "bash", Path: p, Args: []string{"-c"}}
	}
	return Shell{Name: "sh", Path: "sh", Args: []string{"-c"}}
}

// encodePowerShell wraps the command so its exit status reaches us the
// way a shell's would (native commands set $LASTEXITCODE, cmdlets only
// $?), then encodes it as PowerShell expects for -EncodedCommand.
func encodePowerShell(command string) string {
	script := "$ErrorActionPreference = 'Continue'\n" + command + "\nif ($LASTEXITCODE -ne $null) { exit $LASTEXITCODE } elseif (-not $?) { exit 1 } else { exit 0 }"
	u := utf16.Encode([]rune(script))
	b := make([]byte, 0, len(u)*2)
	for _, c := range u {
		b = append(b, byte(c), byte(c>>8))
	}
	return base64.StdEncoding.EncodeToString(b)
}
