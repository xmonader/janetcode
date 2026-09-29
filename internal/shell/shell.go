// Package shell runs command lines through the platform shell and judges
// them: which interpreter to use, how to supervise the process tree, what
// environment a model-run command sees, whether a command line is
// read-only, escapes the workspace or is catastrophic, and the optional
// docker/sandbox-exec sandbox around it. It knows nothing about tools.
package shell

import (
	"context"
	"encoding/base64"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"
	"unicode/utf16"

	"github.com/xmonader/janetcode/internal/procgroup"
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
	shellOnce    sync.Once
	defaultShell Shell
)

// Default picks the shell for this machine once. JANETCODE_SHELL, when
// set, names it instead (see Override).
func Default() Shell {
	shellOnce.Do(func() {
		if s, ok := Override(os.Getenv("JANETCODE_SHELL")); ok {
			defaultShell = s
			return
		}
		defaultShell = shellFor(runtime.GOOS, exec.LookPath)
	})
	return defaultShell
}

// Override is the shell JANETCODE_SHELL names: a POSIX shell given the
// command after -c. On Windows it is how Git Bash (C:\Program
// Files\Git\bin\bash.exe) or busybox-w32 replaces PowerShell, for people
// whose commands and habits are bash's. busybox.exe is run as
// `busybox.exe sh -c`, since it is a multi-call binary. Empty means no
// override.
func Override(path string) (Shell, bool) {
	path = strings.TrimSpace(path)
	if path == "" {
		return Shell{}, false
	}
	// Either separator, whatever the OS: the name is only for display
	// and the busybox check.
	base := path[strings.LastIndexAny(path, `/\`)+1:]
	name := strings.TrimSuffix(strings.ToLower(base), ".exe")
	if strings.HasPrefix(name, "busybox") {
		return Shell{Name: "sh (busybox)", Path: path, Args: []string{"sh", "-c"}}, true
	}
	return Shell{Name: name, Path: path, Args: []string{"-c"}}, true
}

// ShellCommand is Default().Command.
func Command(ctx context.Context, command string) *exec.Cmd {
	return Default().Command(ctx, command)
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

// Supervise puts cmd in its own process group so cancellation kills the
// whole tree, and bounds how long Wait blocks on pipes held open by
// orphaned grandchildren. Use it for any command a context may cancel.
func Supervise(cmd *exec.Cmd) {
	SetProcessGroup(cmd)
	cmd.WaitDelay = 5 * time.Second
}

// SetProcessGroup puts a command in its own process group, so cancelling
// it kills everything it started and not just the process itself.
func SetProcessGroup(cmd *exec.Cmd) { procgroup.KillOnCancel(cmd) }
