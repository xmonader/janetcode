package agent

import (
	"sync"
	"testing"

	"github.com/xmonader/janetcode/internal/tool"
)

// /accept creates its mode on first use while a turn may be reading it.
// Under -race the lazy creation used to be an unsynchronised write.
func TestAcceptModeIsCreatedOnceUnderConcurrency(t *testing.T) {
	a := &Agent{Tools: tool.NewRegistry(), Model: "m", Root: t.TempDir()}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(2)
		go func() { defer wg.Done(); _ = a.AcceptStatus() }()
		go func() { defer wg.Done(); _, _ = a.acceptPhase() }()
	}
	wg.Wait()
	if m := a.acceptMode(); m != a.acceptMode() {
		t.Fatal("two accept modes")
	}
}

// /permissions check reads the root while /cd may be moving it.
func TestPermissionCheckReadsRootUnderTheLock(t *testing.T) {
	a := &Agent{Tools: tool.NewRegistry(), Model: "m", Root: t.TempDir()}
	other := t.TempDir()
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); a.SetRoot(other) }()
	go func() { defer wg.Done(); _ = a.PermissionCheck([]string{"ls"}) }()
	wg.Wait()
}
