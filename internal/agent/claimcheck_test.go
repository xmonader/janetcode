package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/hook"
	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/tool"
)

// call is one answered tool call as a pair of messages.
func call(id, name, input, result string, isErr bool) []llm.Message {
	return []llm.Message{
		{Role: llm.RoleAssistant, Blocks: []llm.Block{{Type: llm.BlockToolUse, ID: id, Name: name, Input: json.RawMessage(input)}}},
		llm.ToolResultMessage(llm.ToolResult(id, result, isErr)),
	}
}

func calls(groups ...[]llm.Message) []llm.Message {
	var out []llm.Message
	for _, g := range groups {
		out = append(out, g...)
	}
	return out
}

func checkerFor(root, where string, msgs ...[]llm.Message) *claimChecker {
	return &claimChecker{proofs: gatherProofs(calls(msgs...), where), root: root}
}

func TestClaimCheckJudgesStateClaimsByTheLastCheck(t *testing.T) {
	red := call("c1", "bash", `{"command":"go test ./..."}`, "--- FAIL: TestParse\n[exit code 1]", false)
	green := call("c2", "bash", `{"command":"go test ./..."}`, "ok  \tex/a\t0.01s\n[exit code 0]", false)
	edit := call("e1", "edit_file", `{"path":"a.go","old_string":"x","new_string":"y"}`, "edited", false)
	for _, tc := range []struct {
		name  string
		c     *claimChecker
		claim string
		want  claimKind
		ref   string
	}{
		{"green after red", checkerFor("", "ctx-4", red, green), "All 11 tests currently pass", claimBacked, "ctx-4, call c2"},
		{"red last", checkerFor("", "ctx-4", green, red), "the tests pass now", claimContradicted, "ctx-4, call c1"},
		{"edited since", checkerFor("", "", green, edit), "tests pass", claimUnbacked, ""},
		{"never ran", checkerFor("", "", edit), "build succeeds", claimUnbacked, ""},
		{"ran claim", checkerFor("", "", green), "I ran the tests after the change", claimBacked, "call c2"},
		// Past tense is history, not a claim about the tree now.
		{"history", checkerFor("", "", red), "tests passed before the refactor", claimUnchecked, ""},
		// A command that never started is no check either way.
		{"not found", checkerFor("", "", call("n", "bash", `{"command":"pytest -q"}`, "pytest: command not found\n[exit code 127]", false)), "tests pass", claimUnbacked, ""},
	} {
		got, ref := tc.c.check(tc.claim)
		if got != tc.want || ref != tc.ref {
			t.Errorf("%s: check(%q) = %v %q, want %v %q", tc.name, tc.claim, got, ref, tc.want, tc.ref)
		}
	}
}

func TestClaimCheckCommandsMustHaveRunAndAgree(t *testing.T) {
	c := checkerFor("", "ctx-2",
		call("c1", "bash", `{"command":"cd /w && go test -run TestParse ./ledger"}`, "--- FAIL: TestParse\n[exit code 1]", false),
		call("c2", "bash", `{"command":"make lint"}`, "[exit code 0]", false),
	)
	for _, tc := range []struct {
		claim string
		want  claimKind
	}{
		{"reproduced with `go test -run TestParse ./ledger`", claimBacked},
		{"`go test -run TestParse ./ledger` fails on the empty row", claimBacked},
		{"`make lint` fails with two warnings", claimContradicted},
		{"`make lint` passes; checks pass", claimBacked},
		{"`go test -race ./...` is clean", claimUnbacked},
		// A backticked span with no space is a name, not a command; one
		// that is not a check and never ran says nothing either way.
		{"`parse_row` reads the tags column", claimUnchecked},
		{"see `git log --oneline` for the history", claimUnchecked},
	} {
		if got, _ := c.check(tc.claim); got != tc.want {
			t.Errorf("check(%q) = %v, want %v", tc.claim, got, tc.want)
		}
	}
}

func TestClaimCheckDefinitionsAgainstTheFileOnDisk(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "ledger"), 0o755)
	os.WriteFile(filepath.Join(root, "ledger", "parse.py"), []byte("REQUIRED = ('date',)\n\ndef read_ledger(path):\n    pass\n"), 0o644)
	read := call("r1", "read_file", `{"path":"ledger/parse.py"}`, "def load_transactions(path):", false)
	c := checkerFor(root, "ctx-9", read)
	for _, tc := range []struct {
		claim string
		want  claimKind
		ref   string
	}{
		{"read_ledger is defined in ledger/parse.py", claimBacked, "ctx-9, call r1"},
		{"`Ledger.read_ledger` is implemented in parse.py", claimBacked, "ctx-9, call r1"},
		{"parse.py defines REQUIRED", claimBacked, "ctx-9, call r1"},
		// Renamed since the read: the file on disk is what counts.
		{"load_transactions is defined in ledger/parse.py", claimContradicted, ""},
		// A path no call opened may be one the harness cannot resolve:
		// not found is unverified, not contradicted.
		{"parse_amount is defined in ledger/money.py", claimUnbacked, ""},
		{"fmt_row is defined in util.py", claimUnbacked, ""},
		// English, not a name: nothing to look for in the file, so the
		// file the claim names is all there is to back it.
		{"The REQUIRED tuple is defined in ledger/parse.py", claimBacked, "ctx-9, call r1"},
		{"This is defined in ledger/parse.py", claimBacked, "ctx-9, call r1"},
		{"Everything is defined in ledger/parse.py", claimBacked, "ctx-9, call r1"},
		{"The tuple is defined in ledger/other.py", claimUnbacked, ""},
	} {
		got, ref := c.check(tc.claim)
		if got != tc.want || ref != tc.ref {
			t.Errorf("check(%q) = %v %q, want %v %q", tc.claim, got, ref, tc.want, tc.ref)
		}
	}
	// A file no call read is checked on disk and says so.
	os.WriteFile(filepath.Join(root, "ledger", "cli.py"), []byte("def main(): pass\n"), 0o644)
	if got, ref := c.check("main is defined in ledger/cli.py"); got != claimBacked || ref != "checked in ledger/cli.py" {
		t.Errorf("unread file: %v %q", got, ref)
	}
	// A file a call opened and that is gone now is a contradiction.
	gone := checkerFor(root, "", call("r2", "read_file", `{"path":"ledger/old.py"}`, "def legacy(): pass", false))
	if got, _ := gone.check("legacy is defined in ledger/old.py"); got != claimContradicted {
		t.Errorf("deleted file: %v", got)
	}
	// Offline there is no disk: the outputs of the calls are the evidence.
	off := checkerFor("", "", read)
	if got, _ := off.check("load_transactions is defined in ledger/parse.py"); got != claimBacked {
		t.Errorf("offline, from the read: %v", got)
	}
	if got, _ := off.check("read_ledger is defined in ledger/parse.py"); got != claimUnbacked {
		t.Errorf("offline, not in any output: %v", got)
	}
}

func TestClaimCheckPathsAndWhatItLeavesAlone(t *testing.T) {
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "a.go"), []byte("package a\n"), 0o644)
	os.WriteFile(filepath.Join(root, "b.go"), []byte("package a\n"), 0o644)
	big := call("r1", "read_file", `{"path":"a.go"}`, "[output is 40 KB (sha256 abc); about 16 KB of the middle is omitted here. The full output is saved as ctx-7: expand_output ...]\npackage a", false)
	denied := call("w1", "write_file", `{"path":"gone.go","content":"x"}`, DeniedMessage, true)
	c := checkerFor(root, "ctx-3", big, denied)
	for _, tc := range []struct {
		claim string
		want  claimKind
		ref   string
	}{
		// The read's own archive entry is the evidence: no call to grep for.
		{"a.go holds the entry point", claimBacked, "ctx-7"},
		{"b.go has the helpers", claimUnchecked, ""},
		{"the config lives in conf/app.yaml", claimUnbacked, ""},
		// A refused write never happened.
		{"gone.go holds the old parser", claimUnbacked, ""},
		// Judgements, plans and dotted names are not checkable claims.
		{"keep it in package a", claimUnchecked, ""},
		{"output goes through fmt.Println", claimUnchecked, ""},
		{"pin version 1.2.3 of the client", claimUnchecked, ""},
		{"the parser is verbose", claimUnchecked, ""},
	} {
		got, ref := c.check(tc.claim)
		if got != tc.want || ref != tc.ref {
			t.Errorf("check(%q) = %v %q, want %v %q", tc.claim, got, ref, tc.want, tc.ref)
		}
	}
}

// A record goes through many compactions: the tags of one check are
// replaced by the next, never stacked, and a claim backed once is judged
// again against what happened since.
func TestClaimCheckRetagsInsteadOfStacking(t *testing.T) {
	green := call("c1", "bash", `{"command":"go test ./..."}`, "ok\n[exit code 0]", false)
	c := checkerFor("", "ctx-1", green)
	var tally claimTally
	once := c.apply([]string{"tests pass", "keep it small", "`go vet ./...` is clean"}, &tally)
	twice := c.apply(once, &tally)
	if strings.Join(once, "|") != strings.Join(twice, "|") {
		t.Fatalf("not stable:\n%q\n%q", once, twice)
	}
	want := []string{"tests pass (evidence: ctx-1, call c1)", "keep it small", "unverified: `go vet ./...` is clean"}
	if strings.Join(once, "|") != strings.Join(want, "|") {
		t.Fatalf("apply = %q", once)
	}
	red := checkerFor("", "ctx-2", green, call("c2", "bash", `{"command":"go test ./..."}`, "FAIL\n[exit code 1]", false))
	var t2 claimTally
	if got := red.apply(once, &t2); len(got) != 2 || t2.Dropped != 1 || !strings.Contains(t2.String(), "dropped 1 contradicted (tests pass)") {
		t.Fatalf("re-check after a red run = %q, %+v", got, t2)
	}
}

// At compaction the facts in the record are checked, the dropped one is
// gone from the note, the pointers resolve with expand_output, and the
// switch leaves the record as the model wrote it.
func TestCompactionChecksWorkingMemoryFacts(t *testing.T) {
	run := func(off map[string]bool) (*Agent, string, []string) {
		root := t.TempDir()
		os.WriteFile(filepath.Join(root, "a.go"), []byte("package a\n\nfunc Parse() {}\n"), 0o644)
		f := &fake{responses: []*llm.Response{text(`{"goal":"fix parse","decisions":["keep it in package a"],"ruled_out":[],` +
			`"facts":["Parse is defined in a.go","Lex is defined in a.go","all tests pass","the lexer is hand-written"],"open":[],"next":["fix it"]}`)}}
		a := &Agent{Provider: f, Tools: tool.NewRegistry(), Model: "m", Off: off, Archive: NewArchive(t.TempDir()), Hooks: &hook.Runner{Cwd: root}}
		a.Messages = calls(
			[]llm.Message{llm.TextMessage(llm.RoleUser, "fix parse")},
			call("t1", "read_file", `{"path":"a.go"}`, "package a\n\nfunc Parse() {}\n", false),
			call("t2", "bash", `{"command":"go test ./..."}`, strings.Repeat("log line\n", 300)+"--- FAIL: TestParse\n[exit code 1]", false),
			[]llm.Message{llm.TextMessage(llm.RoleAssistant, "found it")},
			chatter(3),
			[]llm.Message{llm.TextMessage(llm.RoleUser, "go on")},
		)
		var notices []string
		if err := a.ForceCompact(context.Background(), func(e Event) {
			if e.Kind == EvNotice {
				notices = append(notices, e.Text)
			}
		}); err != nil {
			t.Fatal(err)
		}
		return a, a.Messages[0].Text(), notices
	}

	a, note, notices := run(nil)
	for _, want := range []string{
		"- Parse is defined in a.go (evidence: ctx-1, call t1)",
		"- the lexer is hand-written\n",
		claimLegend,
	} {
		if !strings.Contains(note, want) {
			t.Errorf("note missing %q:\n%s", want, note)
		}
	}
	if strings.Contains(note, "Lex is defined") || strings.Contains(note, "all tests pass") {
		t.Errorf("contradicted claims kept:\n%s", note)
	}
	if len(notices) != 1 || !strings.Contains(notices[0], "dropped 2 contradicted") {
		t.Errorf("notices = %q", notices)
	}
	// The pointer resolves: the call is in the archived head.
	out, err := (&ExpandOutput{Archive: a.Archive}).Run(context.Background(), json.RawMessage(`{"id":"ctx-1","grep":"t1"}`))
	if err != nil || !strings.Contains(out, "read_file t1") {
		t.Errorf("pointer did not resolve: %v %q", err, out)
	}
	// The evidence is carried, without the outputs, to the next check.
	if len(a.working.proofs) != 2 || a.working.proofs[0].out != "" {
		t.Errorf("carried proofs = %+v", a.working.proofs)
	}

	_, note, notices = run(map[string]bool{"claimcheck": true})
	if !strings.Contains(note, "- Lex is defined in a.go\n") || strings.Contains(note, "evidence:") || strings.Contains(note, claimLegend) || len(notices) != 0 {
		t.Errorf("switched off, the record changed:\n%s\n%q", note, notices)
	}
}

func TestSubagentReportIsCheckedAgainstItsOwnCalls(t *testing.T) {
	parent := &Agent{Archive: NewArchive(t.TempDir())}
	child := &Agent{Messages: calls(
		[]llm.Message{llm.TextMessage(llm.RoleUser, "find the bug")},
		call("k1", "bash", `{"command":"pytest -q tests/test_parse.py"}`, "1 failed, 4 passed\n[exit code 1]", false),
		[]llm.Message{llm.TextMessage(llm.RoleAssistant, "report")},
	)}
	report := "Findings:\n- reproduced with `pytest -q tests/test_parse.py`\n- all tests pass after my fix\n- `ruff check .` is clean\n\n```\ntests pass\n```"
	got := parent.checkSubagentReport(report, "task-1", child)
	for _, want := range []string{
		"Findings:",
		"- reproduced with `pytest -q tests/test_parse.py` (evidence: ctx-1, call k1)",
		"- unverified: `ruff check .` is clean",
		"```\ntests pass\n```", // code is not a claim
		"removed 1 line(s)",
		claimLegend,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("report missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "after my fix") {
		t.Errorf("contradicted line kept:\n%s", got)
	}
	// Nothing checkable: the report is untouched and nothing is archived.
	plain := &Agent{Archive: NewArchive(t.TempDir())}
	if got := plain.checkSubagentReport("The parser is a hand-written recursive descent.", "task-2", child); got != "The parser is a hand-written recursive descent." || len(plain.Archive.List()) != 0 {
		t.Errorf("plain report = %q, archive %d", got, len(plain.Archive.List()))
	}
	off := &Agent{Off: map[string]bool{"claimcheck": true}}
	if got := off.checkSubagentReport(report, "task-3", child); got != report {
		t.Errorf("switched off: %q", got)
	}
}

func TestReviewSummaryClaimsAreReportedNotRewritten(t *testing.T) {
	a := &Agent{Messages: call("c1", "bash", `{"command":"npm test"}`, "Tests: 1 failed, 3 passed\n[exit code 1]", false)}
	var notices []string
	emit := func(e Event) { notices = append(notices, e.Text) }
	a.noteReviewClaims("Every requirement holds and all tests pass.", emit)
	if len(notices) != 1 || !strings.Contains(notices[0], "dropped 1 contradicted") {
		t.Fatalf("notices = %q", notices)
	}
	notices = nil
	a.noteReviewClaims("Every requirement holds; I fixed the boundary check.", emit)
	if len(notices) != 0 {
		t.Fatalf("a summary with nothing checkable was flagged: %q", notices)
	}
}
