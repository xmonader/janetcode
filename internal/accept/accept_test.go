package accept

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/session"
)

// fakeTool is a tool with a declared write target, which is all this
// package looks at: the lock is decided from Name and Touches, never from
// running anything.
type fakeTool struct {
	name    string
	touches []string
}

func (f fakeTool) Name() string            { return f.name }
func (f fakeTool) Description() string     { return "fake" }
func (f fakeTool) Schema() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }
func (f fakeTool) Mutating() bool          { return true }
func (f fakeTool) Run(context.Context, json.RawMessage) (string, error) {
	return "", nil
}
func (f fakeTool) Touches(json.RawMessage) ([]string, error) { return f.touches, nil }

// bashTool has no declared targets: the lock reads its command instead.
type bashTool struct{}

func (bashTool) Name() string                                         { return "bash" }
func (bashTool) Description() string                                  { return "bash" }
func (bashTool) Schema() json.RawMessage                              { return json.RawMessage(`{"type":"object"}`) }
func (bashTool) Mutating() bool                                       { return true }
func (bashTool) Run(context.Context, json.RawMessage) (string, error) { return "", nil }

func bashInput(cmd string) json.RawMessage {
	b, err := json.Marshal(struct {
		Command string `json:"command"`
	}{cmd})
	if err != nil {
		panic(err)
	}
	return b
}

func TestParseArgs(t *testing.T) {
	cases := []struct {
		rest string
		want Args
	}{
		{"", Args{Status: true}},
		{"  ", Args{Status: true}},
		{"approve", Args{Approve: true}},
		{"lock", Args{Lock: true}},
		{"unlock", Args{Unlock: true}},
		{"off", Args{Off: true}},
		{"cancel", Args{Off: true}},
		{"exit", Args{Off: true}},
		{"command go test ./...", Args{SetCommand: true, Command: "go test ./..."}},
		{"add a foo endpoint", Args{Request: "add a foo endpoint"}},
		// A word that only looks like a keyword, with more after it, is
		// the request: /accept is free text unless it is exactly one of
		// the fixed words.
		{"lock the door", Args{Request: "lock the door"}},
	}
	for _, c := range cases {
		if got := ParseArgs(c.rest); got != c.want {
			t.Errorf("ParseArgs(%q) = %+v, want %+v", c.rest, got, c.want)
		}
	}
}

// writeFile is a test file on disk plus the tool call that claims to
// write it.
func writeFile(t *testing.T, dir, name, body string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// newMode returns a Mode whose persisted copies are collected, so a test
// can check that a change reached the session record as well as the
// in-memory state.
func newMode(t *testing.T) (*Mode, *[]*session.AcceptMeta) {
	t.Helper()
	var saved []*session.AcceptMeta
	m := New(nil, func(meta *session.AcceptMeta) { saved = append(saved, meta) })
	return m, &saved
}

// TestPhaseTransitions walks the whole mode the way a session does:
// /accept <request>, the model writing a test file, the ACCEPT-VERIFY
// line, /accept approve, /accept unlock and /accept lock, checking the
// phase and what gets persisted at each step.
func TestPhaseTransitions(t *testing.T) {
	dir := t.TempDir()
	m, saved := newMode(t)

	if m.On() {
		t.Fatal("a fresh mode is on")
	}
	if _, ok := m.Phase(); ok {
		t.Fatal("a fresh mode has a phase")
	}

	m.Start("add a foo endpoint", dir)
	if p, ok := m.Phase(); !ok || p != session.AcceptWriting {
		t.Fatalf("after Start: phase=%v ok=%v", p, ok)
	}
	if len(*saved) != 1 || (*saved)[0].Request != "add a foo endpoint" {
		t.Fatalf("Start did not persist the request: %+v", *saved)
	}

	// The model writes a test file; only test files are tracked.
	testPath := writeFile(t, dir, "foo_test.go", "package foo\n\nfunc TestFoo(t *testing.T) {}\n")
	writeFile(t, dir, "foo.go", "package foo\n")
	m.TrackEdit(fakeTool{name: "write_file", touches: []string{testPath}}, nil)
	m.TrackEdit(fakeTool{name: "write_file", touches: []string{filepath.Join(dir, "foo.go")}}, nil)
	if files := m.Meta().Files; len(files) != 1 || files[0] != testPath {
		t.Fatalf("TrackEdit tracked the wrong files: %v", files)
	}

	// The turn ends with the structured line, which proposes the command.
	nudged := false
	notice, harness := m.Capture("done.\n\nACCEPT-VERIFY: go test ./...\n", &nudged, nil)
	if harness != "" {
		t.Fatalf("a stated command should end the turn, got harness %q", harness)
	}
	if !strings.Contains(notice, "go test ./...") {
		t.Fatalf("notice does not name the command: %q", notice)
	}
	if p, _ := m.Phase(); p != session.AcceptProposed {
		t.Fatalf("after Capture: phase=%v", p)
	}

	// Nothing gates the session until the user approves.
	if _, _, ok := m.GateTarget(); ok {
		t.Fatal("a proposed command already gates the session")
	}

	cmd, err := m.Approve(nil)
	if err != nil {
		t.Fatal(err)
	}
	if cmd != "go test ./..." {
		t.Fatalf("Approve returned %q", cmd)
	}
	meta := m.Meta()
	if meta.Phase != session.AcceptLocked {
		t.Fatalf("after Approve: phase=%v", meta.Phase)
	}
	if meta.Hashes[testPath] == "" {
		t.Fatal("Approve recorded no hash for the locked file")
	}
	if string(meta.Contents[testPath]) == "" {
		t.Fatal("Approve captured no content for the locked file")
	}
	if meta.Proposed != "" {
		t.Fatalf("Approve left the proposal behind: %q", meta.Proposed)
	}
	gotCmd, gotRoot, ok := m.GateTarget()
	if !ok || gotCmd != "go test ./..." || gotRoot != dir {
		t.Fatalf("GateTarget = %q %q %v", gotCmd, gotRoot, ok)
	}

	// Approving twice reports the command already in effect rather than
	// re-locking.
	if _, err := m.Approve(nil); err == nil {
		t.Fatal("a second Approve succeeded")
	} else if !strings.Contains(err.Error(), "already locked") {
		t.Fatalf("second Approve: %v", err)
	}

	// Unlock lets a wrong test be fixed; an edit while unlocked moves the
	// recorded hash with it, so the next check does not read as tampering.
	if err := m.Unlock(); err != nil {
		t.Fatal(err)
	}
	if p, _ := m.Phase(); p != session.AcceptUnlocked {
		t.Fatalf("after Unlock: phase=%v", p)
	}
	writeFile(t, dir, "foo_test.go", "package foo\n\nfunc TestFoo(t *testing.T) { _ = 1 }\n")
	m.TrackEdit(fakeTool{name: "edit_file", touches: []string{testPath}}, nil)
	if bad := m.IntegrityCheck(); len(bad) != 0 {
		t.Fatalf("an edit made while unlocked reads as tampering: %v", bad)
	}
	if err := m.Lock(); err != nil {
		t.Fatal(err)
	}
	if p, _ := m.Phase(); p != session.AcceptLocked {
		t.Fatalf("after Lock: phase=%v", p)
	}

	m.Off()
	if m.On() {
		t.Fatal("Off left the mode on")
	}
	if last := (*saved)[len(*saved)-1]; last != nil {
		t.Fatalf("Off persisted %+v, want nil", last)
	}
}

// TestCaptureNudgesThenInfers covers the writing-phase turn end: it asks
// once for what is missing and, on the second turn, settles for an
// inferred command rather than looping forever.
func TestCaptureNudgesThenInfers(t *testing.T) {
	t.Run("no test files", func(t *testing.T) {
		m, _ := newMode(t)
		m.Start("req", t.TempDir())
		nudged := false
		notice, harness := m.Capture("here you go", &nudged, nil)
		if harness == "" || !strings.Contains(notice, "no test files") {
			t.Fatalf("first turn: notice=%q harness=%q", notice, harness)
		}
		if !nudged {
			t.Fatal("the nudge flag was not set")
		}
		notice, harness = m.Capture("here you go", &nudged, nil)
		if notice != "" || harness != "" {
			t.Fatalf("second turn should say nothing: notice=%q harness=%q", notice, harness)
		}
		if p, _ := m.Phase(); p != session.AcceptWriting {
			t.Fatalf("phase moved without any tests: %v", p)
		}
	})

	t.Run("no command stated", func(t *testing.T) {
		dir := t.TempDir()
		m, _ := newMode(t)
		m.Start("req", dir)
		p := writeFile(t, dir, "foo_test.go", "package foo\n")
		m.TrackEdit(fakeTool{name: "write_file", touches: []string{p}}, nil)
		nudged := false
		_, harness := m.Capture("done", &nudged, nil)
		if harness == "" {
			t.Fatal("a missing command was not asked for")
		}
		notice, harness := m.Capture("done", &nudged, []string{"go test ./..."})
		if harness != "" {
			t.Fatalf("the second turn nudged again: %q", harness)
		}
		if !strings.Contains(notice, "inferred") {
			t.Fatalf("notice does not say the command was inferred: %q", notice)
		}
		if got := m.Meta(); got.Phase != session.AcceptProposed || got.Proposed == "" {
			t.Fatalf("no inferred proposal: %+v", got)
		}
	})

	t.Run("unusable command", func(t *testing.T) {
		dir := t.TempDir()
		m, _ := newMode(t)
		m.Start("req", dir)
		p := writeFile(t, dir, "foo_test.go", "package foo\n")
		m.TrackEdit(fakeTool{name: "write_file", touches: []string{p}}, nil)
		nudged := false
		notice, harness := m.Capture("ACCEPT-VERIFY: ``", &nudged, nil)
		if harness == "" || !strings.Contains(notice, "not usable") {
			t.Fatalf("an unusable command was accepted: notice=%q harness=%q", notice, harness)
		}
		if p, _ := m.Phase(); p != session.AcceptWriting {
			t.Fatalf("phase moved on an unusable command: %v", p)
		}
	})
}

// TestDenialRefusesEveryRouteToALockedFile checks the pre-execution half
// of the lock: the file tools' declared targets and a bash command's
// write targets both have to be refused, and anything else let through.
func TestDenialRefusesEveryRouteToALockedFile(t *testing.T) {
	dir := t.TempDir()
	m, _ := newMode(t)
	locked, other := lockOneTest(t, m, dir)

	if got := m.Denial(fakeTool{name: "edit_file", touches: []string{locked}}, nil); got == "" {
		t.Fatal("edit_file on the locked file was allowed")
	} else if !strings.Contains(got, "locked acceptance test") {
		t.Fatalf("denial does not say why: %q", got)
	}
	if got := m.Denial(fakeTool{name: "write_file", touches: []string{other}}, nil); got != "" {
		t.Fatalf("a write to an unlocked file was refused: %q", got)
	}
	if got := m.Denial(bashTool{}, bashInput("echo hi > "+locked)); got == "" {
		t.Fatal("a shell redirect onto the locked file was allowed")
	}
	if got := m.Denial(bashTool{}, bashInput("go test ./...")); got != "" {
		t.Fatalf("a command that writes nothing was refused: %q", got)
	}

	// Off means off: nothing is locked any more.
	m.Off()
	if got := m.Denial(fakeTool{name: "edit_file", touches: []string{locked}}, nil); got != "" {
		t.Fatalf("still denying after /accept off: %q", got)
	}
}

// TestWritingDenialAllowsOnlyTestsAndReadOnlyCommands covers the other
// direction: while the tests are being written (and while they wait for
// approval) the model may write tests and look around, nothing else.
func TestWritingDenialAllowsOnlyTestsAndReadOnlyCommands(t *testing.T) {
	dir := t.TempDir()
	m, _ := newMode(t)
	m.Start("add a foo endpoint", dir)
	testPath := filepath.Join(dir, "foo_test.go")
	srcPath := filepath.Join(dir, "foo.go")

	if got := m.WritingDenial(fakeTool{name: "write_file", touches: []string{testPath}}, nil); got != "" {
		t.Fatalf("writing a test file was refused: %q", got)
	}
	if got := m.WritingDenial(fakeTool{name: "write_file", touches: []string{srcPath}}, nil); got == "" {
		t.Fatal("writing the implementation was allowed during the writing phase")
	}
	if got := m.WritingDenial(bashTool{}, bashInput("cat foo.go")); got != "" {
		t.Fatalf("a read-only command was refused: %q", got)
	}
	if got := m.WritingDenial(bashTool{}, bashInput("rm -rf "+dir)); got == "" {
		t.Fatal("a destructive command was allowed during the writing phase")
	}

	// The proposed phase keeps the same restriction: a QA run found the
	// model starting the implementation the moment its turn ended.
	if err := os.WriteFile(testPath, []byte("package foo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	m.TrackEdit(fakeTool{name: "write_file", touches: []string{testPath}}, nil)
	nudged := false
	if _, harness := m.Capture("ACCEPT-VERIFY: go test ./...", &nudged, nil); harness != "" {
		t.Fatalf("capture did not settle: %q", harness)
	}
	if p, _ := m.Phase(); p != session.AcceptProposed {
		t.Fatalf("phase=%v", p)
	}
	got := m.WritingDenial(fakeTool{name: "write_file", touches: []string{srcPath}}, nil)
	if got == "" {
		t.Fatal("the implementation was allowed while waiting for /accept approve")
	}
	if !strings.Contains(got, "/accept approve") {
		t.Fatalf("the refusal does not say what is being waited for: %q", got)
	}
}

// lockOneTest drives a mode to the locked phase over one test file, and
// returns that file and a second, unlocked one.
func lockOneTest(t *testing.T, m *Mode, dir string) (locked, other string) {
	t.Helper()
	m.Start("add a foo endpoint", dir)
	locked = writeFile(t, dir, "foo_test.go", "package foo\n\nfunc TestFoo(t *testing.T) {}\n")
	other = writeFile(t, dir, "foo.go", "package foo\n")
	m.TrackEdit(fakeTool{name: "write_file", touches: []string{locked}}, nil)
	nudged := false
	if _, harness := m.Capture("ACCEPT-VERIFY: go test ./...", &nudged, nil); harness != "" {
		t.Fatalf("capture did not settle: %q", harness)
	}
	if _, err := m.Approve(nil); err != nil {
		t.Fatal(err)
	}
	return locked, other
}

// TestRestoreTamperedPutsTheApprovedContentBack is the backstop: whatever
// got past the pre-execution checks, the approved bytes go back on disk
// and the tool call that did it is told so.
func TestRestoreTamperedPutsTheApprovedContentBack(t *testing.T) {
	dir := t.TempDir()
	m, _ := newMode(t)
	locked, _ := lockOneTest(t, m, dir)
	approved, err := os.ReadFile(locked)
	if err != nil {
		t.Fatal(err)
	}

	// Nothing changed: nothing to say.
	if got := m.RestoreTampered("bash"); got != "" {
		t.Fatalf("an untouched lock reported %q", got)
	}

	if err := os.WriteFile(locked, []byte("package foo\n// weakened\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if bad := m.IntegrityCheck(); len(bad) != 1 || !strings.Contains(bad[0], "changed outside a tool call") {
		t.Fatalf("the integrity check missed the change: %v", bad)
	}
	msg := m.RestoreTampered("bash")
	if !strings.Contains(msg, "foo_test.go") || !strings.Contains(msg, "bash") {
		t.Fatalf("the restore message does not name the file and the tool: %q", msg)
	}
	back, err := os.ReadFile(locked)
	if err != nil {
		t.Fatal(err)
	}
	if string(back) != string(approved) {
		t.Fatalf("the file was not restored: %q", back)
	}
	if bad := m.IntegrityCheck(); len(bad) != 0 {
		t.Fatalf("still tampered after a restore: %v", bad)
	}

	// A change made outside any tool call is reported differently, since
	// no tool call is to blame for it.
	if err := os.WriteFile(locked, []byte("package foo\n// again\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if msg := m.RestoreTampered(""); !strings.Contains(msg, "outside the session") {
		t.Fatalf("an external change was reported as a tool call's: %q", msg)
	}
}

// TestCheckIntegrityRecordsTheTamper is what the verification gate reads:
// the reason is put on the state (and persisted) rather than only
// returned.
func TestCheckIntegrityRecordsTheTamper(t *testing.T) {
	dir := t.TempDir()
	m, saved := newMode(t)
	locked, _ := lockOneTest(t, m, dir)
	if got := m.CheckIntegrity(); got != "" {
		t.Fatalf("an intact lock reported %q", got)
	}
	if err := os.Remove(locked); err != nil {
		t.Fatal(err)
	}
	reason := m.CheckIntegrity()
	if !strings.Contains(reason, "missing or unreadable") {
		t.Fatalf("CheckIntegrity = %q", reason)
	}
	if m.Meta().Tamper != reason {
		t.Fatalf("the reason was not recorded: %q", m.Meta().Tamper)
	}
	if last := (*saved)[len(*saved)-1]; last == nil || last.Tamper != reason {
		t.Fatalf("the tamper was not persisted: %+v", last)
	}
}

// TestSetCommandChangesOnlyTheCommand covers /accept command <cmd>: the
// locked files and their hashes are not touched by it.
func TestSetCommandChangesOnlyTheCommand(t *testing.T) {
	dir := t.TempDir()
	m, _ := newMode(t)
	locked, _ := lockOneTest(t, m, dir)
	before := m.Meta().Hashes[locked]

	got, err := m.SetCommand("  go test ./internal/foo/...  ")
	if err != nil {
		t.Fatal(err)
	}
	if got != "go test ./internal/foo/..." {
		t.Fatalf("SetCommand = %q", got)
	}
	if cmd, _, _ := m.GateTarget(); cmd != got {
		t.Fatalf("the gate still runs %q", cmd)
	}
	if after := m.Meta().Hashes[locked]; after != before {
		t.Fatal("SetCommand rehashed the locked files")
	}
	if _, err := m.SetCommand("   "); err == nil {
		t.Fatal("an empty command was accepted")
	}
}

// TestSystemTextTracksThePhase: the model is told the rules of the phase
// it is actually in, and nothing at all once the mode is off.
func TestSystemTextTracksThePhase(t *testing.T) {
	dir := t.TempDir()
	m, _ := newMode(t)
	if m.SystemText() != "" {
		t.Fatal("a mode that is off contributes system text")
	}
	m.Start("add a foo endpoint", dir)
	if !strings.Contains(m.SystemText(), "ACCEPT-VERIFY") {
		t.Fatalf("the writing phase does not state the line to end with: %q", m.SystemText())
	}
	lockOneTest(t, m, dir)
	txt := m.SystemText()
	if !strings.Contains(txt, "locked") || !strings.Contains(txt, "foo_test.go") {
		t.Fatalf("the locked phase does not name what is locked: %q", txt)
	}
	m.Off()
	if m.SystemText() != "" {
		t.Fatalf("still contributing system text after /accept off: %q", m.SystemText())
	}
}

// TestNilModeIsOff: a session that never used /accept holds a nil *Mode,
// and every call on it has to be a no-op rather than a panic.
func TestNilModeIsOff(t *testing.T) {
	var m *Mode
	if m.On() || m.Meta() != nil || m.SystemText() != "" {
		t.Fatal("a nil mode is not off")
	}
	if _, ok := m.Phase(); ok {
		t.Fatal("a nil mode has a phase")
	}
	if _, _, ok := m.GateTarget(); ok {
		t.Fatal("a nil mode gates the session")
	}
	nudged := false
	if n, h := m.Capture("ACCEPT-VERIFY: x", &nudged, nil); n != "" || h != "" {
		t.Fatalf("a nil mode captured %q %q", n, h)
	}
	if m.Denial(fakeTool{name: "edit_file"}, nil) != "" || m.WritingDenial(fakeTool{name: "edit_file"}, nil) != "" {
		t.Fatal("a nil mode denies calls")
	}
	if m.RestoreTampered("bash") != "" || len(m.IntegrityCheck()) != 0 || m.CheckIntegrity() != "" {
		t.Fatal("a nil mode reports tampering")
	}
	m.TrackEdit(fakeTool{name: "write_file"}, nil)
	m.RecordGate(&session.AcceptMeta{}, true, "")
}

// TestRecordGateDropsAStaleResult: the gate runs the tests outside the
// lock, so /accept off or a new request may have replaced the state by
// the time the result comes back. It must not land on the new state.
func TestRecordGateDropsAStaleResult(t *testing.T) {
	dir := t.TempDir()
	m, _ := newMode(t)
	lockOneTest(t, m, dir)
	inFlight := m.Meta()

	m.RecordGate(inFlight, true, "ok")
	if g := m.Meta().Gate; g == nil || !g.Passed || g.Summary != "ok" {
		t.Fatalf("the gate result was not recorded: %+v", g)
	}

	// A new request while the tests were running.
	m.Start("something else", dir)
	m.RecordGate(inFlight, false, "failed")
	if g := m.Meta().Gate; g != nil {
		t.Fatalf("a stale gate result landed on the new state: %+v", g)
	}
}

// TestCommandDispatch is the front-end seam: every spelling of /accept
// resolves through one call, so the terminal and the web UI agree.
func TestCommandDispatch(t *testing.T) {
	dir := t.TempDir()
	m, _ := newMode(t)

	res, err := m.Command(ParseArgs(""), dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Sheet, "accept mode is off") {
		t.Fatalf("a bare /accept on an off mode: %q", res.Sheet)
	}

	// A request hands the text over as the next turn's prompt.
	res, err = m.Command(ParseArgs("add a foo endpoint"), dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Handover != "add a foo endpoint" {
		t.Fatalf("the request was not handed over: %+v", res)
	}
	if p, _ := m.Phase(); p != session.AcceptWriting {
		t.Fatalf("phase=%v", p)
	}

	// Approving before anything is proposed is an error, not a lock.
	if _, err := m.Command(ParseArgs("approve"), dir, nil); err == nil {
		t.Fatal("approve succeeded with nothing proposed")
	}

	locked, _ := lockOneTest(t, m, dir)
	_ = locked
	res, err = m.Command(ParseArgs("unlock"), dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Notes) != 1 || !strings.Contains(res.Notes[0], "unlocked") {
		t.Fatalf("unlock said %+v", res.Notes)
	}
	if _, err := m.Command(ParseArgs("lock"), dir, nil); err != nil {
		t.Fatal(err)
	}
	if p, _ := m.Phase(); p != session.AcceptLocked {
		t.Fatalf("phase=%v", p)
	}

	// Already locked reports the command in effect rather than failing.
	res, err = m.Command(ParseArgs("approve"), dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Sheet, "already locked") {
		t.Fatalf("a second approve said %q", res.Sheet)
	}

	if _, err := m.Command(ParseArgs("off"), dir, nil); err != nil {
		t.Fatal(err)
	}
	if m.On() {
		t.Fatal("off left the mode on")
	}
}
