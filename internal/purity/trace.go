// Package purity watches what a shell command actually does, and lets a
// command that has been seen to change nothing, run after run, stop
// asking for approval in the repository it was seen in.
//
// The classifier in package shell guesses from a command's text whether
// it only reads. A guess is the wrong basis for skipping a person's yes:
// `make lint` reads the same as `make install`, and a script's name says
// nothing about its body. This package does not guess. Each run is put
// under a side-effect monitor (strace, on Linux), and the trace is read
// for anything that changes state: a file opened for writing, created,
// renamed, removed or re-moded, a connection to anything but the local
// name-lookup sockets, a signal to another process. A command whose every
// observed run was clean, PromoteAfter times in one repository, is
// promoted: the next run is approved without asking, and is watched like
// every other run, so a promoted command that writes is demoted for good
// the moment it does.
//
// Every failure fails closed. No strace, a ptrace the kernel refuses, a
// trace line that cannot be read, a run that was killed or timed out:
// each is a run that proves nothing, and a command is never promoted on
// runs that proved nothing. A command that runs code it reads — an
// interpreter, a build tool, a program inside the repository — is never
// promoted however clean it looked, since the code it runs can change
// between runs while its text does not.
package purity

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

// traced is every system call the trace asks for: the ones that can
// change a file, the network or another process, and execve so the
// programs a command ran can be named. Each carries strace's "?" so a
// name this architecture lacks (open on arm64) is skipped rather than an
// error. A call that can change state and is not on this list is a hole
// in the monitor, so the list errs long.
var traced = []string{
	"open", "openat", "openat2", "creat", "open_by_handle_at",
	"unlink", "unlinkat", "rename", "renameat", "renameat2",
	"mkdir", "mkdirat", "rmdir", "link", "linkat", "symlink", "symlinkat",
	"chmod", "fchmod", "fchmodat", "fchmodat2", "chown", "fchown", "fchownat", "lchown",
	"truncate", "ftruncate", "fallocate", "utime", "utimes", "utimensat", "futimesat",
	"mknod", "mknodat", "setxattr", "lsetxattr", "fsetxattr", "removexattr", "lremovexattr", "fremovexattr",
	"mount", "umount2", "pivot_root", "chroot", "swapon", "swapoff", "acct",
	"mount_setattr", "move_mount", "fsopen", "fsconfig", "fsmount", "open_tree",
	"io_uring_setup", "process_vm_writev", "ptrace", "bpf",
	"init_module", "finit_module", "delete_module", "reboot", "kexec_load",
	"sethostname", "setdomainname", "settimeofday", "clock_settime", "adjtimex",
	"mq_open", "msgsnd", "shmget", "semop",
	"execve", "execveat",
	"connect", "bind", "listen", "sendto", "sendmsg", "sendmmsg",
	"kill", "tkill", "tgkill", "rt_sigqueueinfo", "rt_tgsigqueueinfo", "pidfd_send_signal",
}

// Monitor runs commands under strace.
type Monitor struct {
	strace string
}

var detected struct {
	once sync.Once
	m    *Monitor
}

// Detect returns the monitor this machine has, or nil. strace has to be
// installed and the kernel has to let it trace its own child (a
// container without CAP_SYS_PTRACE, a Yama setting of 3, a seccomp
// profile refusing ptrace): one traced run of `true` decides, once per
// process. nil means there is nothing to watch with, and nothing is ever
// promoted.
func Detect() *Monitor {
	detected.once.Do(func() {
		p, err := exec.LookPath("strace")
		if err != nil {
			return
		}
		tr, err := exec.LookPath("true")
		if err != nil {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if exec.CommandContext(ctx, p, "-f", "-qq", "-o", os.DevNull, "-e", "trace=execve", "--", tr).Run() != nil {
			return
		}
		detected.m = &Monitor{strace: p}
	})
	return detected.m
}

// Wrap makes cmd run under the monitor, writing its trace to logPath.
// Everything else about cmd — its directory, environment, process group,
// standard streams — is left as it was: strace runs the same program
// with the same arguments, and only adds itself in front.
func (m *Monitor) Wrap(cmd *exec.Cmd, logPath string) {
	if m == nil || cmd == nil || cmd.Path == "" {
		return
	}
	names := make([]string, len(traced))
	for i, n := range traced {
		names[i] = "?" + n
	}
	args := []string{"strace", "-f", "-q", "-s", "4096", "-o", logPath, "-e", "trace=" + strings.Join(names, ","), "--", cmd.Path}
	if len(cmd.Args) > 1 {
		args = append(args, cmd.Args[1:]...)
	}
	cmd.Path, cmd.Args = m.strace, args
}

// Verdict is what one traced run showed.
type Verdict struct {
	// Pure is true when the run changed nothing the monitor can see.
	Pure bool
	// Blind is true when the trace proves nothing either way: none was
	// written, it was too large, or a line of it could not be read.
	Blind bool
	// Why names the first change seen, or why the trace proves nothing,
	// when the run was not pure.
	Why string
	// Exec lists the programs the run executed, first first; the first
	// is the shell that ran the command line.
	Exec []string
}

// maxTrace is the largest trace that is read. A run that traced more is
// not judged: proving nothing is cheaper than reading a gigabyte.
const maxTrace = 64 << 20

// ReadTrace judges the trace a wrapped run wrote to path.
func ReadTrace(path string) Verdict {
	f, err := os.Open(path)
	if err != nil {
		return Verdict{Blind: true, Why: "no trace was written"}
	}
	defer f.Close()
	if st, err := f.Stat(); err == nil && st.Size() > maxTrace {
		return Verdict{Blind: true, Why: "the trace was too large to read"}
	}
	return Judge(f)
}

// Judge reads a strace -f -q log. A clean verdict needs the whole run:
// every process the trace saw must have exited under it, so a run that
// was killed at its timeout, handed to the jobs registry, or that left a
// process behind proves nothing. A change seen in part of a run is still
// a change. An empty log proves nothing either: even `true` leaves its
// execve, so a log with none means the trace never ran.
func Judge(r io.Reader) Verdict {
	v := Verdict{Pure: true}
	seen, exited := map[string]bool{}, map[string]bool{}
	killed := ""
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		pid, name, args, ok := splitLine(line)
		if !ok {
			return Verdict{Blind: true, Why: "a trace line could not be read: " + clip(line, 120), Exec: v.Exec}
		}
		if pid != "" {
			seen[pid] = true
		}
		switch {
		case strings.HasPrefix(args, "+++ exited with"):
			exited[pid] = true
			continue
		case strings.HasPrefix(args, "+++ killed by"):
			exited[pid] = true
			if killed == "" {
				killed = strings.Trim(args, "+ ")
			}
			continue
		case name == "":
			continue // a signal, a resumed call
		}
		if name == "execve" || name == "execveat" {
			if strings.Contains(args, ") = -1 ") {
				continue // a path search's miss ran nothing
			}
			if p := execPath(name, args); p != "" {
				v.Exec = append(v.Exec, p)
			}
			continue
		}
		if why := effect(name, args); why != "" && v.Pure {
			v.Pure, v.Why = false, why
		}
	}
	if sc.Err() != nil {
		return Verdict{Blind: true, Why: "the trace could not be read", Exec: v.Exec}
	}
	if !v.Pure {
		return v
	}
	if len(v.Exec) == 0 {
		return Verdict{Blind: true, Why: "the trace is empty", Exec: v.Exec}
	}
	if killed != "" {
		return Verdict{Blind: true, Why: "a process was " + killed, Exec: v.Exec}
	}
	if len(exited) == 0 {
		return Verdict{Blind: true, Why: "no process was seen to exit", Exec: v.Exec}
	}
	for pid := range seen {
		if !exited[pid] {
			return Verdict{Blind: true, Why: "a process was still running when the command returned", Exec: v.Exec}
		}
	}
	return v
}

// lineRe is one strace call: its name and its arguments to the end of
// the line.
var lineRe = regexp.MustCompile(`^([a-z_0-9]+)\((.*)$`)

// splitLine takes a trace line apart into the process it is about and
// the call. A line that is not a call — a signal (---), the second half
// of a call another process interrupted (<... resumed>) — returns an
// empty name and ok; an exit (+++) returns an empty name with the line
// as args.
func splitLine(line string) (pid, name, args string, ok bool) {
	rest := line
	if i := strings.IndexByte(rest, ' '); i > 0 && allDigits(rest[:i]) {
		pid, rest = rest[:i], strings.TrimSpace(rest[i+1:])
	}
	switch {
	case strings.HasPrefix(rest, "+++"):
		return pid, "", rest, true
	case strings.HasPrefix(rest, "---"), strings.HasPrefix(rest, "<..."):
		return pid, "", "", true
	}
	m := lineRe.FindStringSubmatch(rest)
	if m == nil {
		return pid, "", "", false
	}
	return pid, m[1], m[2], true
}

func allDigits(s string) bool {
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return s != ""
}

// quotedRe is a strace string argument, escapes and all.
var quotedRe = regexp.MustCompile(`"(?:[^"\\]|\\.)*"`)

// firstString is the first string argument, unquoted, or "".
func firstString(args string) string {
	q := quotedRe.FindString(args)
	if len(q) < 2 {
		return ""
	}
	return strings.ReplaceAll(q[1:len(q)-1], `\"`, `"`)
}

// execPath is the program an execve ran. execveat's path is relative to
// a descriptor this trace cannot resolve; it is kept as written.
func execPath(name, args string) string {
	if strings.Contains(args, "<unfinished") && !strings.Contains(args, "\"") {
		return ""
	}
	return firstString(args)
}

// writeFlagRe matches an open flag that can change a file.
var writeFlagRe = regexp.MustCompile(`\bO_(WRONLY|RDWR|CREAT|TRUNC|APPEND|TMPFILE)\b`)

// harmless are the paths a command may open for writing without changing
// anything: the null and terminal devices and its own standard streams.
var harmless = map[string]bool{
	"/dev/null": true, "/dev/tty": true, "/dev/zero": true, "/dev/full": true,
	"/dev/stdout": true, "/dev/stderr": true, "/dev/stdin": true,
	"/dev/random": true, "/dev/urandom": true,
}

var harmlessRe = regexp.MustCompile(`^(/dev/pts/\d+|/dev/fd/[0-2]|/proc/self/fd/[0-2])$`)

// lookupSockets are the local services a C library asks to resolve a
// user or group name; `ls -l` connects to one. Connecting asks, it does
// not change anything.
var lookupSockets = []string{"/var/run/nscd/socket", "/run/nscd/socket", "/run/systemd/userdb/"}

// effect is the change one traced call makes, "" for none.
func effect(name, args string) string {
	switch name {
	case "open", "openat", "openat2", "open_by_handle_at":
		path := firstString(args)
		rest := quotedRe.ReplaceAllString(args, `""`)
		if !writeFlagRe.MatchString(rest) || harmless[path] || harmlessRe.MatchString(path) {
			return ""
		}
		return "opened " + describe(path) + " for writing"
	case "connect":
		if strings.Contains(args, "AF_UNIX") {
			p := sunPath(args)
			for _, s := range lookupSockets {
				if p == s || (strings.HasSuffix(s, "/") && strings.HasPrefix(p, s)) {
					return ""
				}
			}
			return "connected to the local socket " + describe(p)
		}
		return "opened a network connection"
	case "sendto", "sendmsg", "sendmmsg":
		// A send on a connected socket was judged at its connect; one
		// that names an address of its own is a datagram leaving.
		if strings.Contains(args, "sa_family=") && !strings.Contains(args, "sa_family=AF_UNIX") {
			return "sent a datagram"
		}
		if p := sunPath(args); p != "" {
			return "sent to the local socket " + describe(p)
		}
		return ""
	case "kill", "tkill", "tgkill":
		if signalZero(args) {
			return "" // kill(pid, 0) asks whether a process exists
		}
		return "signalled another process"
	case "ptrace":
		return "traced another process"
	}
	path := firstString(args)
	if path != "" {
		return name + " " + describe(path)
	}
	return "called " + name
}

var sunPathRe = regexp.MustCompile(`sun_path=(@?"(?:[^"\\]|\\.)*")`)

func sunPath(args string) string {
	m := sunPathRe.FindStringSubmatch(args)
	if m == nil {
		return ""
	}
	return strings.Trim(strings.TrimPrefix(m[1], "@"), `"`)
}

// signalZero reports a kill-family call whose signal argument is 0.
func signalZero(args string) bool {
	parts := strings.Split(strings.SplitN(args, ")", 2)[0], ",")
	sig := strings.TrimSpace(parts[len(parts)-1])
	return sig == "0" || sig == "SIG_0"
}

func describe(path string) string {
	if path == "" {
		return "(unnamed)"
	}
	return clip(path, 120)
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}

// codeRunners are the programs that run code they read from a file or a
// network: the code can change between two runs of the same command
// line, so how the command behaved before says nothing about the next
// run. Matched on the program's base name, with any version suffix
// (python3.12) dropped.
var codeRunners = map[string]bool{
	"sh": true, "bash": true, "dash": true, "zsh": true, "ksh": true, "ash": true, "fish": true, "mksh": true, "busybox": true,
	"python": true, "python2": true, "python3": true, "pypy": true, "pypy3": true, "node": true, "nodejs": true,
	"deno": true, "bun": true, "ruby": true, "perl": true, "php": true, "lua": true, "luajit": true,
	"tclsh": true, "rscript": true, "julia": true, "java": true, "osascript": true, "pwsh": true,
	"make": true, "gmake": true, "cmake": true, "ninja": true, "go": true, "cargo": true, "rustc": true,
	"npm": true, "npx": true, "yarn": true, "pnpm": true, "pip": true, "pip3": true, "uv": true,
	"gradle": true, "gradlew": true, "mvn": true, "dotnet": true, "mix": true, "rake": true, "bundle": true,
}

var versionSuffix = regexp.MustCompile(`[0-9.]+$`)

// Promotable reports whether a pure run's command may count towards
// promotion, and if not, why. The first program is the shell that ran
// the command line and is not held against it; every later one must be
// a program outside the repository that does not run code it reads.
func Promotable(v Verdict, root string) (bool, string) {
	if !v.Pure {
		return false, v.Why
	}
	for i, p := range v.Exec {
		if i == 0 {
			continue
		}
		if root != "" && insideDir(p, root) {
			return false, "it runs " + describe(p) + ", a program inside the repository"
		}
		if !filepath.IsAbs(p) {
			return false, "it runs " + describe(p) + " by a relative path"
		}
		base := strings.ToLower(filepath.Base(p))
		if codeRunners[base] || codeRunners[versionSuffix.ReplaceAllString(base, "")] {
			return false, "it runs " + filepath.Base(p) + ", which runs code it reads"
		}
	}
	return true, ""
}

func insideDir(p, dir string) bool {
	if !filepath.IsAbs(p) {
		return false
	}
	rel, err := filepath.Rel(dir, p)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// String is the verdict as a line for a notice or a test failure.
func (v Verdict) String() string {
	if v.Pure {
		return fmt.Sprintf("pure (%d programs)", len(v.Exec))
	}
	return "not pure: " + v.Why
}
