package purity

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// A trace in the shape strace -f writes, from a real run of
// `ls -l /tmp | head -2; cat /etc/hostname > /dev/null; date`.
const pureTrace = `39468 execve("/usr/bin/bash", ["bash", "-c", "ls -l /tmp | head -2; cat /etc/h"...], 0x7fff48602398 /* 101 vars */) = 0
39468 openat(AT_FDCWD, "/etc/ld.so.cache", O_RDONLY|O_CLOEXEC) = 3
39468 openat(AT_FDCWD, "/dev/tty", O_RDWR|O_NONBLOCK) = -1 ENXIO (No such device or address)
39469 execve("/home/driver/.local/bin/ls", ["ls", "-l", "/tmp"], 0x633079f0e410 /* 101 vars */) = -1 ENOENT (No such file or directory)
39469 execve("/usr/bin/ls", ["ls", "-l", "/tmp"], 0x633079f0e410 /* 101 vars */ <unfinished ...>
39470 execve("/usr/bin/head", ["head", "-2"], 0x633079f0e410 /* 101 vars */ <unfinished ...>
39469 <... execve resumed>)             = 0
39469 connect(3, {sa_family=AF_UNIX, sun_path="/var/run/nscd/socket"}, 110) = -1 ENOENT (No such file or directory)
39880 openat(AT_FDCWD, "/dev/null", O_WRONLY|O_CREAT|O_TRUNC, 0666) = 3
--- SIGCHLD {si_signo=SIGCHLD, si_code=CLD_EXITED, si_pid=39469, si_uid=1000, si_status=0} ---
39471 kill(39469, 0) = 0
39469 +++ exited with 0 +++
39470 +++ exited with 0 +++
39880 +++ exited with 0 +++
39471 +++ exited with 0 +++
39468 +++ exited with 0 +++
`

func TestPurityJudgeReadsACleanRun(t *testing.T) {
	v := Judge(strings.NewReader(pureTrace))
	if !v.Pure || v.Blind {
		t.Fatalf("a run that only read was judged %s", v)
	}
	want := []string{"/usr/bin/bash", "/usr/bin/ls", "/usr/bin/head"}
	if strings.Join(v.Exec, " ") != strings.Join(want, " ") {
		t.Errorf("exec = %v, want %v (a path search's miss ran nothing)", v.Exec, want)
	}
}

func TestPurityJudgeSeesEachKindOfChange(t *testing.T) {
	head := `1 execve("/usr/bin/bash", ["bash"], 0x0) = 0` + "\n"
	for line, want := range map[string]string{
		`2 openat(AT_FDCWD, "x.txt", O_WRONLY|O_CREAT|O_TRUNC, 0666) = 3`:                               "opened x.txt for writing",
		`2 openat(AT_FDCWD, "log", O_RDONLY|O_APPEND <unfinished ...>`:                                  "opened log for writing",
		`2 open("/tmp/a", O_RDWR) = -1 EACCES (Permission denied)`:                                      "opened /tmp/a for writing",
		`2 unlinkat(AT_FDCWD, "go.sum", 0) = 0`:                                                         "unlinkat go.sum",
		`2 renameat2(AT_FDCWD, "a", AT_FDCWD, "b", RENAME_NOREPLACE) = 0`:                               "renameat2 a",
		`2 fchmod(3, 0755) = 0`:                                                                         "called fchmod",
		`2 connect(3, {sa_family=AF_INET, sin_port=htons(443), sin_addr=inet_addr("1.2.3.4")}, 16) = 0`: "opened a network connection",
		`2 connect(3, {sa_family=AF_UNIX, sun_path="/var/run/docker.sock"}, 110) = 0`:                   "connected to the local socket /var/run/docker.sock",
		`2 sendto(3, "x", 1, 0, {sa_family=AF_INET6, sin6_port=htons(53)}, 28) = 1`:                     "sent a datagram",
		`2 kill(1234, SIGTERM) = 0`:                                                                     "signalled another process",
		`2 io_uring_setup(8, 0x7ff) = 3`:                                                                "called io_uring_setup",
	} {
		v := Judge(strings.NewReader(head + line + "\n"))
		if v.Pure || v.Blind || v.Why != want {
			t.Errorf("%s\n  judged %+v, want not pure: %s", line, v, want)
		}
	}
}

func TestPurityJudgeIgnoresTheHarmlessWrites(t *testing.T) {
	head := `1 execve("/usr/bin/bash", ["bash"], 0x0) = 0` + "\n"
	for _, line := range []string{
		`2 openat(AT_FDCWD, "/dev/pts/3", O_RDWR) = 3`,
		`2 openat(AT_FDCWD, "/proc/self/fd/1", O_WRONLY) = 3`,
		// A path that merely spells a flag is not one.
		`2 openat(AT_FDCWD, "O_WRONLY.txt", O_RDONLY) = 3`,
		`2 sendto(3, "q", 1, MSG_NOSIGNAL, NULL, 0) = 1`,
		`2 tgkill(5, 6, 0) = 0`,
	} {
		exits := "1 +++ exited with 0 +++\n2 +++ exited with 0 +++\n"
		if v := Judge(strings.NewReader(head + line + "\n" + exits)); !v.Pure {
			t.Errorf("%s\n  judged %s", line, v)
		}
	}
}

func TestPurityJudgeFailsClosed(t *testing.T) {
	for name, trace := range map[string]string{
		"empty":       "",
		"no execve":   `2 openat(AT_FDCWD, "a", O_RDONLY) = 3` + "\n",
		"unreadable":  `1 execve("/usr/bin/bash", ["bash"], 0x0) = 0` + "\nstrace: Process 7 detached oddly\n",
		"only signal": "--- SIGCHLD {} ---\n",
		// Killed at its timeout: what it would have done next is unseen.
		"killed": "1 execve(\"/usr/bin/bash\", [\"bash\"], 0x0) = 0\n1 +++ killed by SIGKILL +++\n",
		// A process that outlived the command: the same.
		"left behind": "1 execve(\"/usr/bin/bash\", [\"bash\"], 0x0) = 0\n2 execve(\"/usr/bin/sleep\", [\"sleep\"], 0x0) = 0\n1 +++ exited with 0 +++\n",
	} {
		v := Judge(strings.NewReader(trace))
		if v.Pure || !v.Blind {
			t.Errorf("%s: judged %+v, want blind", name, v)
		}
	}
	if v := ReadTrace(filepath.Join(t.TempDir(), "missing")); !v.Blind {
		t.Errorf("a missing trace judged %+v", v)
	}
}

func TestPurityPromotableBarsCodeRunners(t *testing.T) {
	root := "/repo"
	pure := func(exec ...string) Verdict {
		return Verdict{Pure: true, Exec: append([]string{"/usr/bin/bash"}, exec...)}
	}
	for _, c := range []struct {
		v    Verdict
		ok   bool
		want string
	}{
		{pure("/usr/bin/grep", "/usr/bin/wc"), true, ""},
		{pure(), true, ""}, // a builtin: the shell alone
		{pure("/usr/bin/python3.12"), false, "python3.12"},
		{pure("/usr/bin/bash"), false, "bash"},
		{pure("/usr/local/go/bin/go"), false, "go"},
		{pure("/repo/bin/check"), false, "inside the repository"},
		{pure("./check"), false, "relative path"},
		{Verdict{Why: "opened x for writing"}, false, "opened x"},
	} {
		ok, why := Promotable(c.v, root)
		if ok != c.ok || !strings.Contains(why, c.want) {
			t.Errorf("Promotable(%v) = %v %q, want %v containing %q", c.v.Exec, ok, why, c.ok, c.want)
		}
	}
}

func TestPurityStorePromotesAfterCleanRunsAndDemotesForGood(t *testing.T) {
	dir := t.TempDir()
	s := Open(true, dir)
	clean := Verdict{Pure: true, Exec: []string{"/usr/bin/bash", "/usr/bin/cat"}}
	for i := 1; i < PromoteAfter; i++ {
		if _, after := s.Observe("/r", "cat a ", clean); after.Promoted() {
			t.Fatalf("promoted after %d runs", i)
		}
	}
	s.Observe("/r", "cat a", clean)
	if !s.Promoted("/r", "  cat a") {
		t.Fatal("not promoted after PromoteAfter clean runs")
	}
	// Another repository, another command: neither is promoted.
	if s.Promoted("/other", "cat a") || s.Promoted("/r", "cat b") {
		t.Fatal("a promotion leaked to another repository or command")
	}
	// A blind run changes nothing.
	s.Observe("/r", "cat a", Verdict{Blind: true, Why: "the trace is empty"})
	if !s.Promoted("/r", "cat a") {
		t.Fatal("a blind run demoted the command")
	}
	// It persists, and one write demotes it for good.
	s2 := Open(true, dir)
	if !s2.Promoted("/r", "cat a") {
		t.Fatal("the promotion did not persist")
	}
	s2.Observe("/r", "cat a", Verdict{Why: "opened a for writing", Exec: clean.Exec})
	for i := 0; i < 5; i++ {
		s2.Observe("/r", "cat a", clean)
	}
	if r := s2.Get("/r", "cat a"); r.Promoted() || r.Impure == "" {
		t.Fatalf("after a write the record is %+v", r)
	}
	// A barred command never counts.
	for i := 0; i < 5; i++ {
		s2.Observe("/r", "make lint", Verdict{Pure: true, Exec: []string{"/usr/bin/bash", "/usr/bin/make"}})
	}
	if r := s2.Get("/r", "make lint"); r.Promoted() || r.Barred == "" {
		t.Fatalf("make lint record %+v", r)
	}
	var off *Store
	off.Observe("/r", "x", clean)
	if off.Promoted("/r", "x") || Open(false, dir) != nil {
		t.Fatal("a nil or disabled store promoted something")
	}
}

// TestPurityMonitorLive runs real commands under strace when this
// machine has it, so the parser is held to what strace really writes.
func TestPurityMonitorLive(t *testing.T) {
	m := Detect()
	if m == nil {
		t.Skip("no usable strace here")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run := func(script string) Verdict {
		t.Helper()
		log := filepath.Join(t.TempDir(), "trace")
		cmd := exec.CommandContext(context.Background(), "/bin/sh", "-c", script)
		cmd.Dir = dir
		m.Wrap(cmd, log)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%s: %v\n%s", script, err, out)
		}
		return ReadTrace(log)
	}
	if v := run("cat a.txt | wc -l >/dev/null; ls -l >/dev/null"); !v.Pure {
		t.Errorf("a read-only pipeline judged %s", v)
	} else if ok, why := Promotable(v, dir); !ok {
		t.Errorf("a read-only pipeline barred: %s", why)
	}
	if _, err := exec.LookPath("rg"); err == nil {
		// Threads exit under the trace like processes do.
		if v := run("rg -j4 x . >/dev/null"); !v.Pure {
			t.Errorf("a threaded search judged %s", v)
		}
	}
	if v := run("echo y > b.txt"); v.Pure || !strings.Contains(v.Why, "b.txt") {
		t.Errorf("a redirect judged %s", v)
	}
	if v := run("rm a.txt"); v.Pure {
		t.Errorf("rm judged %s", v)
	}
}
