package evidence

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/xmonader/janetcode/internal/session"
)

var t0 = time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)

func editEv(turn int, path string) session.Event {
	return session.Event{Type: session.TypeFileChange, Turn: turn, Name: path, CallID: "c-" + path}
}

func gate(turn int, src, cmd string, extra GateExtra, isErr bool, out string, at time.Duration) session.Event {
	raw, _ := json.Marshal(extra)
	return session.Event{Type: session.TypeGate, Turn: turn, Source: src, Name: cmd, Extra: raw, IsError: isErr, Output: out,
		Time: t0.Add(at), DurationMs: 2000}
}

func bash(turn int, id, cmd, out string, isErr bool) session.Event {
	args, _ := json.Marshal(map[string]string{"command": cmd})
	return session.Event{Type: session.TypeToolResult, Turn: turn, Name: "bash", CallID: id, Arguments: string(args), Output: out, IsError: isErr, Time: t0}
}

func TestClassify(t *testing.T) {
	cases := []struct {
		name   string
		exit   int
		out    string
		cancel bool
		want   Status
	}{
		{"green with tests", 0, "ok  \tpkg\t0.1s", false, Passed},
		{"green with no output", 0, "", false, Passed},
		{"red", 1, "--- FAIL: TestX", false, Failed},
		{"empty suite is no evidence", 0, "no tests found", false, Inconclusive},
		{"timeout", 124, "[timed out after 10m0s]", false, Inconclusive},
		{"tool missing", 127, "sh: 1: nosuch: not found", false, Environmental},
		{"network down", 1, "dial tcp: lookup proxy.golang.org: no such host", false, Environmental},
		{"disk full", 2, "write: no space left on device", false, Environmental},
		{"interrupted", -1, "", true, Cancelled},
		{"cancel beats green exit", 0, "ok pkg", true, Cancelled},
	}
	for _, c := range cases {
		if got := Classify(c.exit, c.out, c.cancel); got != c.want {
			t.Errorf("%s: Classify(%d,%q,%v) = %s, want %s", c.name, c.exit, c.out, c.cancel, got, c.want)
		}
	}
	for _, s := range All {
		if !s.Valid() {
			t.Errorf("%s not valid", s)
		}
	}
	if Parse("bogus") != Inconclusive || Parse("") != Inconclusive {
		t.Error("unknown status must read as inconclusive, never passed")
	}
}

func TestSummarizePrefersTheLineThatSaysHowItWent(t *testing.T) {
	out := "=== RUN TestA\n--- FAIL: TestA (0.00s)\n    a_test.go:9: boom\nFAIL\texample/pkg\t0.01s\n\n[exit code 1]\n"
	if got := Summarize(out); !strings.Contains(got, "FAIL") {
		t.Errorf("summary %q should name the failure", got)
	}
	if Summarize("   \n") != "no output" {
		t.Error("empty output")
	}
	if got := Summarize(strings.Repeat("x", 500)); len([]rune(got)) > maxSummary {
		t.Errorf("summary not bounded: %d", len([]rune(got)))
	}
}

func TestPassedAfterTheLastEditBacksSuccess(t *testing.T) {
	ev := []session.Event{
		editEv(1, "/w/a.go"),
		gate(1, session.GateVerify, "make test", GateExtra{Exit: 0}, false, "", time.Minute),
	}
	r := FromEvents(ev, Options{Root: "/w"})
	if r.Status != Passed || r.Blocks || !r.Required || len(r.Checks) != 1 {
		t.Fatalf("report = %+v", r)
	}
	c := r.Checks[0]
	if !c.Fresh || c.Exit != 0 || c.Command != "make test" || c.Source != session.GateVerify {
		t.Errorf("check = %+v", c)
	}
	if got := c.Ended.Sub(c.Started); got != 2*time.Second {
		t.Errorf("started/ended should bracket the run, got %v", got)
	}
	if len(c.Files) != 1 || c.Files[0] != "a.go" {
		t.Errorf("affected files = %v", c.Files)
	}
}

func TestACheckBeforeTheLatestEditCannotSatisfyTheGate(t *testing.T) {
	ev := []session.Event{
		editEv(1, "/w/a.go"),
		gate(1, session.GateVerify, "make test", GateExtra{Exit: 0}, false, "", time.Minute),
		editEv(1, "/w/b.go"),
	}
	r := FromEvents(ev, Options{Root: "/w"})
	if r.Status != NotRun || !r.Blocks || !r.Stale {
		t.Fatalf("a check older than the last edit must not back success: %+v", r)
	}
	c := r.Checks[0]
	if c.Fresh || c.StaleBy != "b.go" || c.Status != Passed {
		t.Errorf("the check keeps its own status but is stale: %+v", c)
	}
	if !strings.Contains(r.Why, "b.go") {
		t.Errorf("why should name the file: %q", r.Why)
	}
	// Re-running after the edit restores it.
	ev = append(ev, gate(1, session.GateVerify, "make test", GateExtra{Exit: 0}, false, "", 2*time.Minute))
	if r := FromEvents(ev, Options{Root: "/w"}); r.Status != Passed || r.Blocks {
		t.Errorf("rerun after the edit should pass: %+v", r)
	}
}

func TestStaleAcrossTurnsButNotForAnEarlierTurnsOwnReport(t *testing.T) {
	ev := []session.Event{
		editEv(1, "/w/a.go"),
		gate(1, session.GateVerify, "make test", GateExtra{}, false, "", time.Minute),
		editEv(2, "/w/a.go"),
	}
	if r := FromEvents(ev, Options{}); r.Status != NotRun || !r.Blocks {
		t.Errorf("session-wide: turn 2's edit makes turn 1's check stale: %+v", r)
	}
	if r := FromEvents(ev, Options{Turn: 1}); r.Status != Passed || r.Blocks {
		t.Errorf("turn 1's own report must not be spoiled by turn 2: %+v", r)
	}
}

func TestDocsOnlyEditsNeitherRequireNorStaleEvidence(t *testing.T) {
	exempt := func(p string) bool { return strings.HasSuffix(p, ".md") }
	ev := []session.Event{editEv(1, "/w/README.md")}
	r := FromEvents(ev, Options{Exempt: exempt})
	if r.Required || r.Blocks {
		t.Errorf("docs-only edit needs no evidence: %+v", r)
	}
	ev = []session.Event{
		editEv(1, "/w/a.go"),
		gate(1, session.GateVerify, "make test", GateExtra{}, false, "", time.Minute),
		editEv(1, "/w/README.md"),
	}
	if r := FromEvents(ev, Options{Exempt: exempt}); r.Status != Passed || r.Blocks {
		t.Errorf("a README edit after the check must not stale it: %+v", r)
	}
}

func TestEditedCodeWithNoCheckIsNotRun(t *testing.T) {
	r := FromEvents([]session.Event{editEv(1, "/w/a.go")}, Options{})
	if r.Status != NotRun || !r.Blocks || !strings.Contains(r.Why, "no check ran") {
		t.Errorf("%+v", r)
	}
	if r := FromEvents(nil, Options{}); r.Blocks || r.Line() != "" {
		t.Errorf("an empty session has nothing to say: %+v", r)
	}
}

func TestFailureDominatesAndTheExitCodeIsKept(t *testing.T) {
	ev := []session.Event{
		editEv(1, "/w/a.go"),
		gate(1, session.GateVerify, "go vet ./...", GateExtra{}, false, "", time.Minute),
		gate(1, session.GateVerify, "go test ./...", GateExtra{Exit: 3}, true, "--- FAIL: TestA\nFAIL", time.Minute),
		gate(1, session.GateVerify, "make lint", GateExtra{Status: string(NotRun)}, false, "", time.Minute),
	}
	r := FromEvents(ev, Options{})
	if r.Status != Failed || !r.Blocks {
		t.Fatalf("%+v", r)
	}
	got := map[string]Status{}
	for _, c := range r.Checks {
		got[c.Command] = c.Status
	}
	if got["go vet ./..."] != Passed || got["go test ./..."] != Failed || got["make lint"] != NotRun {
		t.Errorf("statuses = %v", got)
	}
	for _, c := range r.Checks {
		if c.Command == "go test ./..." && c.Exit != 3 {
			t.Errorf("exit lost: %d", c.Exit)
		}
	}
}

func TestEnvironmentalFailureIsNotAFailureOfTheCode(t *testing.T) {
	ev := []session.Event{
		editEv(1, "/w/a.go"),
		bash(1, "b1", "go test ./...", "dial tcp: lookup proxy.golang.org: no such host\nFAIL\n[exit code 1]", false),
	}
	r := FromEvents(ev, Options{})
	if r.Status != Environmental || !r.Blocks {
		t.Errorf("environmental must be its own status and still block the claim: %+v", r)
	}
}

func TestRetriesOnAnUnchangedTreeAreFlaky(t *testing.T) {
	ev := []session.Event{
		editEv(1, "/w/a.go"),
		bash(1, "b1", "go test ./...", "--- FAIL: TestRace\nFAIL\n[exit code 1]", false),
		bash(1, "b2", "go test ./...", "ok  \tpkg\t0.2s\n", false),
	}
	r := FromEvents(ev, Options{})
	if r.Status != Flaky || !r.Blocks || r.Checks[0].Attempts != 2 {
		t.Fatalf("pass after fail with no edit between is flaky, not passed: %+v", r)
	}
	// A fix between the two runs is a repair, not flakiness.
	fixed := []session.Event{
		editEv(1, "/w/a.go"),
		bash(1, "b1", "go test ./...", "--- FAIL: TestX\nFAIL\n[exit code 1]", false),
		editEv(1, "/w/a.go"),
		bash(1, "b2", "go test ./...", "ok  \tpkg\t0.2s\n", false),
	}
	if r := FromEvents(fixed, Options{}); r.Status != Passed || r.Checks[0].Attempts != 1 {
		t.Errorf("edit between runs makes the retry a fix: %+v", r)
	}
	// Two consistent passes are just a passed check run twice.
	twice := []session.Event{
		editEv(1, "/w/a.go"),
		bash(1, "b1", "go test ./...", "ok  \tpkg\t0.2s\n", false),
		bash(1, "b2", "go test ./...", "ok  \tpkg\t0.2s\n", false),
	}
	if r := FromEvents(twice, Options{}); r.Status != Passed || r.Checks[0].Attempts != 2 {
		t.Errorf("%+v", r)
	}
}

func TestCancelledAndRefusedChecks(t *testing.T) {
	opts := Options{Refusals: []string{"The user denied"}, Interrupted: []string{"Cancelled by the user"}}
	ev := []session.Event{
		editEv(1, "/w/a.go"),
		bash(1, "b1", "go test ./...", "Cancelled by the user", true),
	}
	if r := FromEvents(ev, opts); r.Status != Cancelled || !r.Blocks {
		t.Errorf("cancelled: %+v", r)
	}
	ev = []session.Event{
		editEv(1, "/w/a.go"),
		bash(1, "b1", "go test ./...", "The user denied permission for this tool call.", true),
	}
	if r := FromEvents(ev, opts); len(r.Checks) != 0 || r.Status != NotRun {
		t.Errorf("a refused call never ran: %+v", r)
	}
	gateCancel := []session.Event{
		editEv(1, "/w/a.go"),
		gate(1, session.GateVerify, "make test", GateExtra{Exit: -1, Status: string(Cancelled)}, false, "", time.Minute),
	}
	if r := FromEvents(gateCancel, Options{}); r.Status != Cancelled {
		t.Errorf("gate cancelled: %+v", r)
	}
}

func TestBackgroundedCheckIsInconclusive(t *testing.T) {
	ev := []session.Event{editEv(1, "/w/a.go"), bash(1, "b1", "go test ./...", "Started background job 3", false)}
	if r := FromEvents(ev, Options{}); r.Status != Inconclusive {
		t.Errorf("%+v", r)
	}
}

func TestHiddenAcceptanceGateIsWithheldAndCounted(t *testing.T) {
	ev := []session.Event{
		editEv(1, "/w/a.go"),
		gate(1, session.GateAccept, "acceptance tests", GateExtra{Exit: 1}, true, "FAIL hidden", time.Minute),
	}
	r := FromEvents(ev, Options{})
	if r.Status != Failed || len(r.Checks) != 1 || !r.Checks[0].Withheld || r.Checks[0].Command != "acceptance tests" {
		t.Errorf("%+v", r)
	}
}

func TestJevAdvisoryIsRecordedBesideTheCommandNeverInIt(t *testing.T) {
	advisory := func(extra map[string]any) session.Event {
		raw, _ := json.Marshal(extra)
		return session.Event{Type: session.TypeDecision, Turn: 1, Name: "verify_triage", Extra: raw}
	}
	base := []session.Event{
		editEv(1, "/w/a.go"),
		gate(1, session.GateVerify, "make test", GateExtra{Exit: 2}, true, "FAIL", time.Minute),
	}
	// Jev says environmental with high confidence: the command still failed.
	ev := append(slicesClone(base), advisory(map[string]any{
		"consumer": "verify_triage", "class": "environmental", "confidence": 0.9, "caused_by_change": 0.1, "skip": true}))
	r := FromEvents(ev, Options{})
	c := r.Checks[0]
	if c.Status != Failed || c.Exit != 2 || r.Status != Failed {
		t.Fatalf("advice must not change command truth: %+v", r)
	}
	if c.Advisory == nil || c.Advisory.Class != "environmental" || !c.Advisory.Acted || c.Advisory.Source != "jev" {
		t.Errorf("advisory = %+v", c.Advisory)
	}
	// Jev errors: recorded as an advisory with no class; status untouched.
	ev = append(slicesClone(base), advisory(map[string]any{"consumer": "verify_triage", "status": "timeout", "acted_on": false, "error": "deadline"}))
	r = FromEvents(ev, Options{})
	a := r.Checks[0].Advisory
	if r.Checks[0].Status != Failed || a == nil || a.Error != "timeout" || a.Class != "" || a.Acted {
		t.Errorf("jev error: %+v / %+v", r.Checks[0], a)
	}
	if !strings.Contains(r.Text(), "no answer (timeout); status unchanged") {
		t.Errorf("text: %s", r.Text())
	}
	// A passing check never gets an advisory attached.
	ev = []session.Event{editEv(1, "/w/a.go"), gate(1, session.GateVerify, "make test", GateExtra{}, false, "", time.Minute),
		advisory(map[string]any{"class": "flaky", "confidence": 0.9})}
	if r := FromEvents(ev, Options{}); r.Checks[0].Advisory != nil {
		t.Errorf("advisory on a passing check: %+v", r.Checks[0].Advisory)
	}
}

func slicesClone(ev []session.Event) []session.Event { return append([]session.Event(nil), ev...) }

func TestWithoutRetiresAScratchCheck(t *testing.T) {
	ev := []session.Event{
		editEv(1, "/w/a.go"),
		bash(1, "b1", "go test ./scratch", "FAIL\n[exit code 1]", false),
		bash(1, "b2", "go test ./...", "ok  \tpkg\t0.2s\n", false),
	}
	r := FromEvents(ev, Options{})
	if r.Status != Failed {
		t.Fatalf("%+v", r)
	}
	if got := r.Without(map[string]bool{"go test ./scratch": true}); got.Status != Passed || got.Blocks || len(got.Checks) != 1 {
		t.Errorf("%+v", got)
	}
}

func TestLineAndTextRenderTheSameModel(t *testing.T) {
	ev := []session.Event{
		editEv(1, "/w/a.go"),
		gate(1, session.GateVerify, "make test", GateExtra{}, false, "ok  \tpkg\t0.1s", time.Minute),
		editEv(1, "/w/b.go"),
	}
	r := FromEvents(ev, Options{Root: "/w"})
	if got := r.Line(); !strings.Contains(got, "not_run") || !strings.Contains(got, "(stale)") || !strings.Contains(got, "not backed") {
		t.Errorf("line = %q", got)
	}
	txt := r.Text()
	for _, want := range []string{"stale (edited after: b.go)", "make test", "success claims blocked"} {
		if !strings.Contains(txt, want) {
			t.Errorf("text missing %q:\n%s", want, txt)
		}
	}
}
