package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/xmonader/janetcode/internal/session"
)

// stringList is a repeatable flag value.
type stringList []string

func (s *stringList) String() string { return strings.Join(*s, ",") }
func (s *stringList) Set(v string) error {
	v = strings.TrimSpace(v)
	if v == "" {
		return fmt.Errorf("empty path")
	}
	*s = append(*s, v)
	return nil
}

// launchBackground starts a detached child that runs the prompt against
// sess and returns immediately. The child is `janetcode --resume ID -p
// prompt --yolo` so unattended writes can land; attach is --resume of
// the same id.
func launchBackground(sess *session.Session, prompt string, extra []string) error {
	if err := sess.EnsureWritten(); err != nil {
		return err
	}
	exe, err := os.Executable()
	if err != nil {
		exe = os.Args[0]
	}
	args := backgroundChildArgs(sess.Meta.ID, prompt, extra)
	cmd := exec.Command(exe, args...)
	if dir := sess.Meta.Cwd; dir != "" {
		cmd.Dir = dir
	}
	logPath := strings.TrimSuffix(sess.Path(), ".jsonl") + ".bg.log"
	log, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	cmd.Stdout = log
	cmd.Stderr = log
	cmd.Stdin = nil
	cmd.SysProcAttr = bgProcAttr()
	if err := cmd.Start(); err != nil {
		log.Close()
		return fmt.Errorf("background: %w", err)
	}
	pid := cmd.Process.Pid
	_ = sess.WritePID(pid)
	_ = cmd.Process.Release()
	_ = log.Close()
	fmt.Println(sess.Meta.ID)
	fmt.Fprintf(os.Stderr, "background session %s (pid %d)\nreopen with: janetcode --resume %s\n", sess.Meta.ID, pid, sess.Meta.ID)
	return nil
}

func backgroundChildArgs(id, prompt string, extra []string) []string {
	args := []string{"--resume", id, "-p", prompt, "--yolo"}
	for _, d := range extra {
		args = append(args, "--add-dir", d)
	}
	return args
}
