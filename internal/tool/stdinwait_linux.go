//go:build linux

package tool

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"syscall"
)

// stdinWatchable reports whether a read of a command's stdin can be
// seen on this system: Linux, through /proc.
func stdinWatchable() bool {
	_, err := os.Stat("/proc/self/wchan")
	return err == nil
}

// pipeInode is the inode of a pipe end, which is what /proc/<pid>/fd/0
// names as pipe:[inode] for every process reading that pipe.
func pipeInode(f *os.File) uint64 {
	st, err := f.Stat()
	if err != nil {
		return 0
	}
	if s, ok := st.Sys().(*syscall.Stat_t); ok {
		return s.Ino
	}
	return 0
}

// pollWchans are the kernel functions a process sleeps in while it waits
// on several descriptors at once.
var pollWchans = map[string]bool{
	"ep_poll": true, "do_epoll_wait": true, "do_select": true, "core_sys_select": true,
	"do_sys_poll": true, "poll_schedule_timeout": true, "do_poll": true,
}

// probeStdin looks at every process in the command's session (the
// command is a session leader, see procgroup.NoTerminal, so its pid is
// the session id, and a child that makes a process group of its own is
// still found) and reports what those holding the pipe as stdin are
// doing.
func probeStdin(sid int, ino uint64) inputState {
	var st inputState
	if ino == 0 {
		return st
	}
	want := fmt.Sprintf("pipe:[%d]", ino)
	ents, err := os.ReadDir("/proc")
	if err != nil {
		return st
	}
	running := false
	for _, e := range ents {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		stat, err := os.ReadFile("/proc/" + e.Name() + "/stat")
		if err != nil {
			continue
		}
		// The command name is in parentheses and may hold spaces; the
		// fields after the last ')' are state, ppid, pgrp, session.
		s := string(stat)
		f := strings.Fields(s[strings.LastIndexByte(s, ')')+1:])
		if len(f) < 4 {
			continue
		}
		if sess, _ := strconv.Atoi(f[3]); sess != sid {
			continue
		}
		if f[0] == "R" {
			running = true
		}
		if fd0, _ := os.Readlink(fmt.Sprintf("/proc/%d/fd/0", pid)); fd0 != want {
			continue
		}
		wchan, _ := os.ReadFile(fmt.Sprintf("/proc/%d/wchan", pid))
		switch w := strings.TrimSpace(string(wchan)); {
		case w == "pipe_read" || w == "pipe_wait" || w == "pipe_wait_readable":
			st.reading = true
		case pollWchans[w]:
			st.polling = true
		case (w == "" || w == "0") && f[0] != "R":
			// A kernel that hides wchan (or a process between two
			// states) says nothing about what it waits on. Giving it EOF
			// is what every command used to get; holding the pipe open
			// on a guess could leave it waiting until the timeout.
			st.opaque = true
		}
	}
	if running {
		st.polling = false
	}
	return st
}
