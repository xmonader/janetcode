package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/session"
	"github.com/xmonader/janetcode/internal/tool"
)

// What a long session holds on to, measured rather than guessed.
//
// Thirty turns are run against a fake provider, each one reading a file
// and answering, and the resident set is read at the start and the end.
// The transcript itself has to grow — it is the conversation — so the
// number to watch is how much more than the transcript is held: the
// session's own copy of every event, the output archive's index, the
// cache ledger, anything that accumulates per call and is never dropped.

func vmRSS(t *testing.T) int {
	t.Helper()
	data, err := os.ReadFile("/proc/self/status")
	if err != nil {
		t.Skip("no /proc/self/status")
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "VmRSS:") {
			f := strings.Fields(line)
			if len(f) >= 2 {
				kb, _ := strconv.Atoi(f[1])
				return kb * 1024
			}
		}
	}
	t.Skip("no VmRSS")
	return 0
}

func TestThirtyTurnsHoldOnlyTheTranscript(t *testing.T) {
	root := t.TempDir()
	// A file big enough that a tool result is worth measuring: 32 KB a
	// turn, a million bytes over thirty turns.
	body := strings.Repeat("func handler(w http.ResponseWriter, r *http.Request) { fmt.Fprintln(w, \"ok\") }\n", 400)
	if err := os.WriteFile(root+"/big.go", []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	sess, err := session.New(t.TempDir(), root, "fake", "m")
	if err != nil {
		t.Fatal(err)
	}
	const turns = 30
	var responses []*llm.Response
	for i := 0; i < turns; i++ {
		responses = append(responses,
			&llm.Response{Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{
				{Type: llm.BlockToolUse, ID: fmt.Sprintf("t%d", i), Name: "read_file",
					Input: json.RawMessage(`{"path":"big.go"}`)},
			}}, StopReason: llm.StopToolUse},
			text("read it"))
	}
	f := &fake{responses: responses}
	a := &Agent{
		Provider:    f,
		Tools:       tool.NewRegistry(tool.ReadFile{Root: root}),
		System:      "sys",
		Model:       "m",
		Log:         sess,
		AutoApprove: true,
	}
	a.CompactAt = 1 << 30 // never compact: this measures what is held, not what is dropped

	runtime.GC()
	var before runtime.MemStats
	runtime.ReadMemStats(&before)
	rssBefore := vmRSS(t)

	transcript := 0
	for i := 0; i < turns; i++ {
		if err := a.Run(context.Background(), fmt.Sprintf("turn %d: read big.go", i), nil); err != nil {
			t.Fatal(err)
		}
	}
	for _, m := range a.Messages {
		for _, b := range m.Blocks {
			transcript += len(b.Text) + len(b.Input)
		}
	}

	runtime.GC()
	var after runtime.MemStats
	runtime.ReadMemStats(&after)
	rssAfter := vmRSS(t)

	heap := int(after.HeapAlloc) - int(before.HeapAlloc)
	t.Logf("%d turns · transcript %d bytes · live heap %d → %d (+%d, %.1f× the transcript) · VmRSS %d → %d (+%d) · %d session events",
		turns, transcript, before.HeapAlloc, after.HeapAlloc, heap,
		float64(heap)/float64(transcript), rssBefore, rssAfter, rssAfter-rssBefore, len(sess.Events()))

	// The agent's history, the session's copy of the same events and the
	// archive's index are three holders of the same conversation; four
	// times the transcript leaves room for all of them and their slack,
	// and fails on anything that accumulates per call without bound.
	if limit := 4 * transcript; heap > limit {
		t.Fatalf("thirty turns hold %d bytes for a %d-byte transcript (%.1f×), over the %.0f× ceiling",
			heap, transcript, float64(heap)/float64(transcript), 4.0)
	}
}
