//go:build unix

package tool

import (
	"context"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/shell"
)

// A job is reaped by its waiting goroutine before that goroutine records
// it as done, and in that window the pid — which is also the process
// group id — has already gone back to the kernel to be handed out again.
// Killing on the Done flag alone therefore sent SIGKILL to whatever tree
// happened to be given the number next.
func TestKillDoesNotSignalAJobAlreadyReaped(t *testing.T) {
	cmd := shell.Command(context.Background(), "exit 0")
	cmd.Dir = t.TempDir()
	shell.SetProcessGroup(cmd)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	// Reap it by hand and leave Done unset, which is exactly the state
	// the waiting goroutine passes through.
	_ = cmd.Wait()
	j := &Jobs{next: 1}
	j.jobs = append(j.jobs, &Job{ID: 1, Command: "exit 0", Pid: cmd.Process.Pid, cmd: cmd, done: make(chan struct{})})

	err := j.Kill(1)
	if err == nil {
		t.Fatal("a reaped job was signalled; its process group id may belong to something else by now")
	}
	if !strings.Contains(err.Error(), "already finished") {
		t.Fatalf("kill went to the operating system instead of being refused: %v", err)
	}
}
