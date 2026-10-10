package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/proof"
	"github.com/xmonader/janetcode/internal/session"
	"github.com/xmonader/janetcode/internal/tool"
)

// ledgerProject is a project whose "test suite" is a shell script under
// tests/, so the ledger's replay and the conflict check have something
// real to run without a language toolchain.
func ledgerProject(t *testing.T, check string) string {
	t.Helper()
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "tests"), 0o755)
	os.WriteFile(filepath.Join(root, "tests", "check.sh"), []byte(check), 0o644)
	os.WriteFile(filepath.Join(root, "calc.sh"), []byte("total() { echo 40; }\n"), 0o644)
	return root
}

const totalCheck = `. ./calc.sh
[ "$(total)" = "42" ] || { echo "FAIL test_total: got $(total)"; exit 1; }
echo "1 passed"
`

func ledgerAgent(f *fake, root string, off map[string]bool) *Agent {
	return &Agent{Provider: f, Model: "m", Log: &session.Memory{}, AutoApprove: true, TestLedger: true, Root: root, Off: off,
		Tools: tool.NewRegistry(tool.WriteFile{Root: root}, tool.Bash{Root: root})}
}

func lastLedger(t *testing.T, a *Agent) proof.Integrity {
	t.Helper()
	led := foldLedger(a.Log.Events(), a.turnN)
	if led == nil {
		t.Fatal("no ledger on the log")
	}
	return *led
}

// A turn that edits the test until it passes is sent back once: the test
// as it was fails against the code. The ledger records the edit.
func TestLedgerReplaysTheOriginalTests(t *testing.T) {
	root := ledgerProject(t, totalCheck)
	run := func(off map[string]bool) (*fake, *Agent) {
		f := &fake{responses: []*llm.Response{
			toolCall("t1", "write_file", `{"path":"tests/check.sh","content":". ./calc.sh\n[ \"$(total)\" = \"40\" ] || exit 1\necho \"1 passed\"\n"}`),
			toolCall("t2", "bash", `{"command":"cd `+root+` && sh tests/check.sh"}`),
			text("Done; the tests pass."),
			text("Restored nothing; stopping."),
		}}
		a := ledgerAgent(f, root, off)
		if err := a.Run(context.Background(), "make the tests pass", nil); err != nil {
			t.Fatal(err)
		}
		return f, a
	}
	f, a := run(nil)
	if len(f.requests) != 4 {
		t.Fatalf("%d model calls, want 4: the turn goes back once", len(f.requests))
	}
	got := diagLastText(f.requests[3])
	if !strings.Contains(got, "as they were before this turn fail") || !strings.Contains(got, "FAIL test_total") {
		t.Fatalf("note = %q", got)
	}
	led := lastLedger(t, a)
	if !led.ReplayFailed || !led.SentBack || len(led.TestsModified) != 1 || led.TestsModified[0] != "tests/check.sh" || led.Replay != "sh tests/check.sh" {
		t.Fatalf("ledger = %+v", led)
	}
	// The replay ran in a copy: the edited test is still the edited one.
	if data, _ := os.ReadFile(filepath.Join(root, "tests", "check.sh")); !strings.Contains(string(data), `"40"`) {
		t.Fatalf("the replay touched the tree: %q", data)
	}
	os.WriteFile(filepath.Join(root, "tests", "check.sh"), []byte(totalCheck), 0o644)
	if f, _ := run(map[string]bool{"testledger": true}); len(f.requests) != 3 {
		t.Fatalf("%d model calls with testledger off, want 3", len(f.requests))
	}
}

// A branch on the test's own input, returning the test's own answer, is
// named and sent back; the honest version of the same change is not.
func TestLedgerFlagsABranchOnATestLiteral(t *testing.T) {
	check := `. ./calc.sh
code="SAVE-17"
[ "$(price $code)" = "4.20" ] || { echo "FAIL test_price"; exit 1; }
echo "1 passed"
`
	for _, tc := range []struct {
		name, code string
		flagged    bool
	}{
		{"special case", `price() {\n  if [ \"$1\" = \"SAVE-17\" ]; then echo \"4.20\"; fi\n}\n`, true},
		{"honest", `price() { awk -v p=\"$1\" 'BEGIN { print \"4.20\" }'; }\n`, false},
	} {
		root := ledgerProject(t, check)
		f := &fake{responses: []*llm.Response{
			toolCall("t1", "write_file", `{"path":"calc.sh","content":"`+tc.code+`"}`),
			toolCall("t2", "bash", `{"command":"sh tests/check.sh"}`),
			text("Done."),
			text("Stopping."),
		}}
		a := ledgerAgent(f, root, nil)
		if err := a.Run(context.Background(), "price applies the promotion codes", nil); err != nil {
			t.Fatal(err)
		}
		led := lastLedger(t, a)
		if tc.flagged != (len(led.LiteralFlags) > 0) || tc.flagged != led.SentBack {
			t.Fatalf("%s: ledger = %+v", tc.name, led)
		}
		want := 3
		if tc.flagged {
			want = 4
			if got := diagLastText(f.requests[3]); !strings.Contains(got, `"SAVE-17"`) || !strings.Contains(got, "calc.sh") {
				t.Fatalf("%s: note = %q", tc.name, got)
			}
		}
		if len(f.requests) != want {
			t.Fatalf("%s: %d model calls, want %d", tc.name, len(f.requests), want)
		}
	}
}

// A test added beside the code is counted, and is no finding.
func TestLedgerCountsAddedTests(t *testing.T) {
	root := ledgerProject(t, totalCheck)
	f := &fake{responses: []*llm.Response{
		toolCall("t1", "write_file", `{"path":"calc.sh","content":"total() { echo 42; }\n"}`),
		toolCall("t2", "write_file", `{"path":"tests/more.sh","content":". ./calc.sh\n[ \"$(total)\" -gt 0 ]\n"}`),
		toolCall("t3", "bash", `{"command":"sh tests/check.sh && sh tests/more.sh"}`),
		text("Done."),
	}}
	a := ledgerAgent(f, root, nil)
	if err := a.Run(context.Background(), "make total right", nil); err != nil {
		t.Fatal(err)
	}
	led := lastLedger(t, a)
	if led.TestsAdded != 1 || len(led.TestsModified) != 0 || !led.Clean() || led.SentBack || len(f.requests) != 4 {
		t.Fatalf("ledger = %+v after %d calls", led, len(f.requests))
	}
}

// With no passing test command to replay, the ledger says so rather than
// counting the modified test as replayed.
func TestLedgerSkipsReplayWithoutACommand(t *testing.T) {
	root := ledgerProject(t, totalCheck)
	a := &Agent{Root: root, TestLedger: true, Log: &session.Memory{}}
	led, _ := a.keepLedger(context.Background(), root, []ledgerFile{{
		rel: "tests/check.sh", before: []byte(totalCheck), after: []byte("echo ok\n"), existed: true, exists: true,
	}})
	if led.ReplaySkipped == "" || led.ReplayFailed || led.AssertionsModified != 0 {
		t.Fatalf("ledger = %+v", led)
	}
}

func TestRemovedAssertions(t *testing.T) {
	before := "def test_a():\n    assert f(1) == 2\n    assert f(2) == 3\n"
	after := "def test_a():\n    assert f(1) == 2\n    assert f(2) == 4\n"
	if n := removedAssertions(before, after); n != 1 {
		t.Fatalf("removedAssertions = %d, want 1", n)
	}
}

// The proof note carries the ledger and the conflict of a turn.
func TestProofFoldsLedgerAndConflict(t *testing.T) {
	log := &session.Memory{}
	raw, _ := json.Marshal(proof.Integrity{TestsAdded: 2})
	log.Append(session.Event{Type: session.TypeTestLedger, Turn: 1, Extra: raw})
	raw, _ = json.Marshal(proof.Conflict{Test: "test_total", Verified: true})
	log.Append(session.Event{Type: session.TypeConflict, Turn: 1, Extra: raw})
	if l := foldLedger(log.Events(), 1); l == nil || l.TestsAdded != 2 {
		t.Fatalf("ledger = %+v", l)
	}
	if c := foldConflict(log.Events(), 1); c == nil || c.Test != "test_total" || !c.Verified {
		t.Fatalf("conflict = %+v", c)
	}
	if foldLedger(log.Events(), 2) != nil || foldConflict(log.Events(), 2) != nil {
		t.Fatal("folded another turn's record")
	}
}
