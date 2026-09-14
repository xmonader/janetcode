package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/xmonader/janetcode/internal/session"
	"github.com/xmonader/janetcode/internal/tool"
	"github.com/xmonader/janetcode/internal/verify"
)

// "Acceptance tests you approve first, that the model can't weaken."
// /accept <request> puts the session in a test-first mode: the model's
// only job is to write acceptance tests for the request — read-only
// otherwise — then stop. The user reviews the tests (as a diff, with the
// front-end's existing /diff) and runs /accept approve, which locks the
// test files: from then on the harness refuses any tool call that would
// change them, whatever the model tries, and their run command becomes
// an extra verification gate for this session. /accept unlock lifts the
// lock when a test itself turns out wrong; /accept lock reapplies it;
// /accept off ends the mode.
//
// State lives on session.AcceptMeta (Agent.Accept), persisted through
// OnAcceptChange the same way Cron persists armed loops (see cron.go's
// OnChange): the front end that built the agent wires the callback to
// session.Session.SetAccept, so this package never needs its own
// *session.Session reference.
//
// Accept is shared between goroutines: a front-end changes it (/accept
// approve, lock, off) while a turn may be running tools that consult it.
// acceptMu guards the pointer and everything under it. It is only ever
// held for bookkeeping and file hashing — never across emit, a model call,
// or the gate's test run — so it cannot stall an approval prompt.

// acceptVerifyLine is the structured final line a test-writing turn ends
// with to propose the command that runs the tests it wrote, e.g.
// "ACCEPT-VERIFY: go test ./internal/foo/...". This was chosen over a
// dedicated tool: the model already ends every turn with a prose answer,
// so one line in a fixed shape gets the value back with no new schema,
// no extra round trip, and nothing for a front-end to render specially.
var acceptVerifyLine = regexp.MustCompile(`(?m)^\s*ACCEPT-VERIFY:\s*(.+?)\s*$`)

// cleanVerifyCommand takes the raw text after "ACCEPT-VERIFY:" (or a
// user's /accept command argument) down to the one literal shell command
// it names, or "" if it cannot be made into one. It exists because a
// model likes to wrap the command in backticks or quotes — and a stated
// command wrapped in backticks, like `cd pkg && go test -run TestSlug`
// run literally does not run go test at all: the shell performs command substitution first,
// runs the test suite to build a new command line out of its stdout,
// then tries to execute that output's first line as a command — which is
// how a QA run's gate failed with exit 127 and "===: command not found"
// on a command that worked fine typed by hand. Only the text on the line
// itself, trimmed of that wrapping, is ever stored; anything left
// spanning more than one line, or empty once unwrapped, is rejected
// rather than run.
func cleanVerifyCommand(s string) string {
	s = strings.TrimSpace(s)
	for {
		trimmed := stripMatchingWrap(s)
		if trimmed == s {
			break
		}
		s = strings.TrimSpace(trimmed)
	}
	if s == "" || strings.ContainsAny(s, "\n\r") {
		return ""
	}
	return s
}

// stripMatchingWrap removes one layer of wrapping that spans the whole
// string: a triple or single backtick pair, or a matching pair of
// double or single quotes.
func stripMatchingWrap(s string) string {
	for _, wrap := range []string{"```", "`", `"`, "'"} {
		if len(s) >= 2*len(wrap) && strings.HasPrefix(s, wrap) && strings.HasSuffix(s, wrap) {
			return s[len(wrap) : len(s)-len(wrap)]
		}
	}
	return s
}

// AcceptArgs is what "/accept ..." asks for, parsed once in
// ParseAcceptArgs (the same shape as undo_intent.go's ParseUndoArgs) so
// the terminal and the web UI agree on what each spelling means.
type AcceptArgs struct {
	// Status is a bare /accept: report where the mode stands.
	Status bool
	// Request starts the mode: write acceptance tests for this text.
	Request                    string
	Approve, Lock, Unlock, Off bool
	// SetCommand is /accept command <cmd>: replace the verify command
	// directly, when the model's stated or inferred one is wrong (see
	// Agent.AcceptSetCommand). Command holds the text after "command ",
	// still raw — AcceptSetCommand is what cleans it.
	SetCommand bool
	Command    string
}

// ParseAcceptArgs parses the text after "/accept". Anything that is not
// one of the fixed words is a request, the same way /plan's argument is
// free text unless it names approve/off.
func ParseAcceptArgs(rest string) AcceptArgs {
	rest = strings.TrimSpace(rest)
	switch rest {
	case "":
		return AcceptArgs{Status: true}
	case "approve":
		return AcceptArgs{Approve: true}
	case "lock":
		return AcceptArgs{Lock: true}
	case "unlock":
		return AcceptArgs{Unlock: true}
	case "off", "cancel", "exit":
		return AcceptArgs{Off: true}
	}
	if arg, ok := strings.CutPrefix(rest, "command"); ok && (arg == "" || arg[0] == ' ') {
		return AcceptArgs{SetCommand: true, Command: strings.TrimSpace(arg)}
	}
	return AcceptArgs{Request: rest}
}

// AcceptStart puts the session in test-writing mode for request: until
// the model proposes a command and the user approves it, every mutating
// tool call is refused except a write_file/edit_file on a test file (see
// acceptWritingDenial).
func (a *Agent) AcceptStart(request, root string) {
	a.acceptMu.Lock()
	defer a.acceptMu.Unlock()
	a.Accept = &session.AcceptMeta{Phase: session.AcceptWriting, Request: strings.TrimSpace(request), Root: root}
	a.persistAccept()
}

// AcceptOff ends accept mode outright: whatever was locked is unlocked
// and the gate stops running.
func (a *Agent) AcceptOff() {
	a.acceptMu.Lock()
	defer a.acceptMu.Unlock()
	a.Accept = nil
	a.persistAccept()
}

// AcceptApprove locks in what the writing phase produced: it requires at
// least one test file and a verification command (the model's own, or
// one inferred as a fallback — see InferTestCommand), records every
// file's hash, and arms the gate. It returns the command now in effect.
func (a *Agent) AcceptApprove() (string, error) {
	a.acceptMu.Lock()
	defer a.acceptMu.Unlock()
	if a.Accept == nil || a.Accept.Phase != session.AcceptProposed {
		return "", fmt.Errorf("nothing proposed yet: /accept <request> writes tests first")
	}
	if len(a.Accept.Files) == 0 {
		return "", fmt.Errorf("no test files were written")
	}
	cmd := cleanVerifyCommand(a.Accept.Proposed)
	if cmd == "" {
		cmd = InferTestCommand(a.Accept.Files, a.Accept.Root, a.verifyCommands())
	}
	if cmd == "" {
		return "", fmt.Errorf("no verification command: the model did not state one and none could be inferred; write one yourself with /accept lock after setting it, or /accept off and try again")
	}
	a.Accept.Command = cmd
	if err := a.rehashAccept(); err != nil {
		return "", err
	}
	a.captureAcceptContents()
	a.Accept.Phase = session.AcceptLocked
	a.Accept.Proposed = ""
	a.Accept.Tamper = ""
	a.persistAccept()
	return cmd, nil
}

// AcceptLock re-locks the files an earlier approval named, hashing their
// current content, after /accept unlock let the model fix a wrong test.
func (a *Agent) AcceptLock() error {
	a.acceptMu.Lock()
	defer a.acceptMu.Unlock()
	if a.Accept == nil || (a.Accept.Phase != session.AcceptUnlocked && a.Accept.Phase != session.AcceptLocked) {
		return fmt.Errorf("accept: nothing approved yet to lock")
	}
	if err := a.rehashAccept(); err != nil {
		return err
	}
	a.captureAcceptContents()
	a.Accept.Phase = session.AcceptLocked
	a.Accept.Tamper = ""
	a.persistAccept()
	return nil
}

// AcceptSetCommand replaces the verification command directly — for
// /accept command <cmd>, when the model's stated or inferred command
// turns out wrong (a bad ACCEPT-VERIFY line, or a gate that failed
// because the command itself would not run at all — see
// acceptVerifyGate's exit-126/127 handling). It does not touch the
// locked files or their hashes/content, only which command gates the
// session and gets approved.
func (a *Agent) AcceptSetCommand(raw string) (string, error) {
	a.acceptMu.Lock()
	defer a.acceptMu.Unlock()
	if a.Accept == nil {
		return "", fmt.Errorf("accept: nothing started yet")
	}
	cmd := cleanVerifyCommand(raw)
	if cmd == "" {
		return "", fmt.Errorf("accept: command is empty, or is not a single line")
	}
	switch a.Accept.Phase {
	case session.AcceptProposed:
		a.Accept.Proposed = cmd
	case session.AcceptLocked, session.AcceptUnlocked:
		a.Accept.Command = cmd
	default:
		return "", fmt.Errorf("accept: nothing proposed or locked yet to set a command for")
	}
	a.persistAccept()
	return cmd, nil
}

// AcceptUnlock lifts the lock so the model may edit the test files — a
// test turned out wrong, and fixing it is a legitimate edit the user is
// asking for by name, not one the model talked its way into.
func (a *Agent) AcceptUnlock() error {
	a.acceptMu.Lock()
	defer a.acceptMu.Unlock()
	if a.Accept == nil || a.Accept.Phase != session.AcceptLocked {
		return fmt.Errorf("accept: tests are not locked")
	}
	a.Accept.Phase = session.AcceptUnlocked
	a.persistAccept()
	return nil
}

// AcceptStatus is the report for a bare /accept: the request, the locked
// files, whether their hashes still match, and the last gate result.
func (a *Agent) AcceptStatus() string {
	a.acceptMu.Lock()
	defer a.acceptMu.Unlock()
	if a.Accept == nil {
		return "accept mode is off: /accept <request> writes acceptance tests for it first"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "accept: %s\nrequest: %s\n", a.Accept.Phase, a.Accept.Request)
	switch a.Accept.Phase {
	case session.AcceptWriting:
		b.WriteString("writing acceptance tests — only test files may be written; the model ends with an ACCEPT-VERIFY line when done\n")
	case session.AcceptProposed:
		fmt.Fprintf(&b, "proposed command: %q\nreview the files below (a front-end /diff shows each one), then /accept approve — or /accept command <cmd> to correct it first\n", a.Accept.Proposed)
	}
	if len(a.Accept.Files) > 0 {
		b.WriteString("files:\n")
		for _, f := range a.Accept.Files {
			b.WriteString("  " + relOrBase(f, a.Accept.Root) + "\n")
		}
	}
	if a.Accept.Command != "" {
		// %q rather than %s: the exact command as it will be run, quotes
		// and any stray wrapping visible rather than swallowed, so a bad
		// one (see cleanVerifyCommand) is obvious before it burns a gate
		// round.
		fmt.Fprintf(&b, "command: %q\n", a.Accept.Command)
	}
	if a.Accept.Phase == session.AcceptLocked || a.Accept.Phase == session.AcceptUnlocked {
		if bad := a.acceptIntegrityCheck(); len(bad) > 0 {
			b.WriteString("hashes: TAMPERED — " + strings.Join(bad, "; ") + "\n")
		} else {
			b.WriteString("hashes: intact\n")
		}
	}
	if a.Accept.Gate != nil {
		state := "failed"
		if a.Accept.Gate.Passed {
			state = "passed"
		}
		fmt.Fprintf(&b, "last gate: %s at %s — %s\n", state, a.Accept.Gate.At.Format(time.Kitchen), a.Accept.Gate.Summary)
	}
	return strings.TrimRight(b.String(), "\n")
}

// persistAccept tells the front end accept state changed, so it can keep
// the session record in step (see OnAcceptChange). Called with acceptMu
// held; the callback gets a copy, since the session keeps what it is
// given and this agent goes on changing its own.
func (a *Agent) persistAccept() {
	if a.OnAcceptChange != nil {
		a.OnAcceptChange(a.Accept.Clone())
	}
}

// acceptPhase is the current phase, and false when accept mode is off.
func (a *Agent) acceptPhase() (session.AcceptPhase, bool) {
	a.acceptMu.Lock()
	defer a.acceptMu.Unlock()
	if a.Accept == nil {
		return "", false
	}
	return a.Accept.Phase, true
}

func (a *Agent) verifyCommands() []string {
	if a.Verify == nil {
		return nil
	}
	return a.Verify.Config.Commands
}

func (a *Agent) rehashAccept() error {
	hashes := map[string]string{}
	for _, p := range a.Accept.Files {
		sum, err := hashFile(p)
		if err != nil {
			return fmt.Errorf("accept: hash %s: %w", relOrBase(p, a.Accept.Root), err)
		}
		hashes[p] = sum
	}
	a.Accept.Hashes = hashes
	return nil
}

func hashFile(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

// acceptMaxContentSize bounds how large a locked file's full content is
// kept for immediate restore, per file. A file over the cap still has
// its hash checked (acceptIntegrityCheck, acceptRestoreTampered), so a
// change is still caught and reported — it just cannot be put back in
// place automatically, which an acceptance test this large would be an
// odd shape for anyway.
const acceptMaxContentSize = 256 * 1024

// captureAcceptContents (re)reads every locked file's content into
// Accept.Contents, alongside rehashAccept's hashes. This is the content
// half of "locked means locked": a hash alone only tells the harness a
// file moved outside a tool call, after the fact; keeping the bytes
// themselves is what lets acceptRestoreTampered put a tampered file back
// immediately, on its own, rather than merely reporting the tamper and
// waiting for the model or the user to fix it.
func (a *Agent) captureAcceptContents() {
	contents := make(map[string][]byte, len(a.Accept.Files))
	for _, p := range a.Accept.Files {
		data, err := os.ReadFile(p)
		if err != nil || len(data) > acceptMaxContentSize {
			continue
		}
		contents[p] = data
	}
	a.Accept.Contents = contents
}

// acceptRestoreTampered is the backstop behind the lock: called after
// every mutating tool call while accept mode is locked (see executeTool
// and runNested), whatever tool just ran. The pre-execution checks
// (acceptDenial, built on the file tools' declared targets and
// tool.WriteTargets for bash) are the first line of defence, and are
// necessarily incomplete — a shell trick they do not recognise, a
// nested script or run_code call, anything at all that ends up writing
// to disk. This looks at what is actually there afterwards: any locked
// file whose hash no longer matches the one recorded at approve/lock
// time is restored immediately from the content captured then, and the
// tool call that caused it gets told so in its own result, in addition
// to a notice for the user. A file too large to have kept in full (see
// acceptMaxContentSize) cannot be restored this way; it is still
// reported here, and acceptIntegrityCheck fails the verification gate
// over it regardless.
func (a *Agent) acceptRestoreTampered(toolName string, emit func(Event)) string {
	msg := a.acceptRestoreTamperedLocked(toolName)
	if msg != "" {
		emit(Event{Kind: EvNotice, Text: "accept: " + msg, IsError: true})
	}
	return msg
}

func (a *Agent) acceptRestoreTamperedLocked(toolName string) string {
	a.acceptMu.Lock()
	defer a.acceptMu.Unlock()
	if a.Accept == nil || a.Accept.Phase != session.AcceptLocked {
		return ""
	}
	var restored, unrestorable []string
	for _, p := range a.Accept.Files {
		want, ok := a.Accept.Hashes[p]
		if !ok {
			continue
		}
		got, err := hashFile(p)
		if err == nil && got == want {
			continue
		}
		name := relOrBase(p, a.Accept.Root)
		content, haveContent := a.Accept.Contents[p]
		if !haveContent {
			unrestorable = append(unrestorable, name)
			continue
		}
		if werr := tool.AtomicWrite(p, content); werr != nil {
			unrestorable = append(unrestorable, fmt.Sprintf("%s (could not restore it: %s)", name, werr))
			continue
		}
		restored = append(restored, name)
	}
	if len(restored) == 0 && len(unrestorable) == 0 {
		return ""
	}
	var b strings.Builder
	for _, name := range restored {
		if toolName == "" {
			fmt.Fprintf(&b, "%s is a locked acceptance test; it had been changed outside the session and was restored to the approved version.\n", name)
		} else {
			fmt.Fprintf(&b, "%s is a locked acceptance test; the change your %s call made to it was reverted. Fix the code instead.\n", name, toolName)
		}
	}
	for _, name := range unrestorable {
		fmt.Fprintf(&b, "%s is a locked acceptance test and changed, but is too large to restore automatically; tell the user it needs manual attention.\n", name)
	}
	return strings.TrimRight(b.String(), "\n")
}

// isLockedPath reports whether abs (already resolved) names a file
// currently locked against tool-call writes. Called with acceptMu held.
func (a *Agent) isLockedPath(abs string) bool {
	if a.Accept == nil || a.Accept.Phase != session.AcceptLocked {
		return false
	}
	abs = filepath.Clean(abs)
	for _, f := range a.Accept.Files {
		if filepath.Clean(f) == abs {
			return true
		}
	}
	return false
}

// resolvedTouches is a file tool's own resolved write target.
// pathrules.go's touchedPaths (which this package also uses for path-
// scoped rule matching) deliberately echoes the raw "path" field
// verbatim alongside the resolved one, since a rule may be written
// either way; accept mode hashes and compares real files on disk, so it
// needs exactly the one path tool.Toucher says the call will write, not
// a second, unresolved spelling of the same file.
func resolvedTouches(t tool.Tool, input json.RawMessage) []string {
	tc, ok := t.(tool.Toucher)
	if !ok {
		return nil
	}
	paths, err := tc.Touches(input)
	if err != nil {
		return nil
	}
	return paths
}

// acceptDenial reports why a mutating call must not run because it
// touches a locked accept-test file, or "" when it may proceed. Covers
// exactly the two ways a tool call writes a file: a file tool's declared
// target (touchedPaths, shared with pathrules.go) and a bash command's
// write targets (tool.WriteTargets, built on the same path-escape
// analysis Classify uses for the workspace guard).
func (a *Agent) acceptDenial(t tool.Tool, input json.RawMessage) string {
	a.acceptMu.Lock()
	defer a.acceptMu.Unlock()
	if a.Accept == nil || a.Accept.Phase != session.AcceptLocked {
		return ""
	}
	var hit string
	switch t.Name() {
	case "write_file", "edit_file":
		for _, p := range resolvedTouches(t, input) {
			if a.isLockedPath(p) {
				hit = p
				break
			}
		}
	case "bash":
		var in struct {
			Command string `json:"command"`
		}
		if json.Unmarshal(input, &in) == nil {
			for _, p := range tool.WriteTargets(in.Command, a.Accept.Root) {
				if a.isLockedPath(p) {
					hit = p
					break
				}
			}
		}
	}
	if hit == "" {
		return ""
	}
	return fmt.Sprintf("%s is a locked acceptance test for %q: it may not be changed by any tool call. Fix the code so the test passes as written, or ask the user to run /accept unlock if the test itself is wrong.",
		relOrBase(hit, a.Accept.Root), a.Accept.Request)
}

// acceptWritingDenial reports why a mutating call must not run during
// the writing or proposed phase — every mutating tool is refused except
// a write_file or edit_file whose every touched path is a test file —
// or "" when the call may proceed. The restriction has to hold for both
// phases: a QA run found the writing-phase-only version of this check
// let the model edit a non-test file the moment its turn ended and the
// mode moved to "proposed", well before the user had a chance to run
// /accept approve. Called from both executeTool (the model's own calls)
// and runNested (script/execute_code/run_code calls), so a script cannot
// reach past it either.
func (a *Agent) acceptWritingDenial(t tool.Tool, input json.RawMessage) string {
	a.acceptMu.Lock()
	defer a.acceptMu.Unlock()
	if a.Accept == nil {
		return ""
	}
	switch a.Accept.Phase {
	case session.AcceptWriting, session.AcceptProposed:
	default:
		return ""
	}
	if t.Name() == "write_file" || t.Name() == "edit_file" {
		paths := resolvedTouches(t, input)
		allTests := len(paths) > 0
		for _, p := range paths {
			if !tool.IsTestFile(p) {
				allTests = false
				break
			}
		}
		if allTests {
			return ""
		}
	}
	if a.Accept.Phase == session.AcceptProposed {
		return fmt.Sprintf("this session proposed acceptance tests for %q and is waiting on the user to run /accept approve: only test files may be written until then, or until /accept off. Wait for the user rather than starting the implementation.", a.Accept.Request)
	}
	return fmt.Sprintf("this session is writing acceptance tests for %q: only test files may be written, and no other tool is available until the user reviews them and runs /accept approve. End your final message with a line \"ACCEPT-VERIFY: <command>\" naming how to run the tests you wrote.", a.Accept.Request)
}

// acceptTrackEdit records a test file the writing phase just wrote, or
// refreshes a locked file's hash after a change made while unlocked —
// the only way a locked file's content may legitimately move, so the
// next integrity check sees it as expected rather than as tampering.
func (a *Agent) acceptTrackEdit(t tool.Tool, input json.RawMessage) {
	a.acceptMu.Lock()
	defer a.acceptMu.Unlock()
	if a.Accept == nil {
		return
	}
	paths := resolvedTouches(t, input)
	if len(paths) == 0 {
		return
	}
	changed := false
	switch a.Accept.Phase {
	case session.AcceptWriting:
		for _, p := range paths {
			if !tool.IsTestFile(p) || containsStr(a.Accept.Files, p) {
				continue
			}
			a.Accept.Files = append(a.Accept.Files, p)
			sort.Strings(a.Accept.Files)
			changed = true
		}
	case session.AcceptUnlocked:
		for _, p := range paths {
			if !containsStr(a.Accept.Files, p) {
				continue
			}
			if sum, err := hashFile(p); err == nil {
				if a.Accept.Hashes == nil {
					a.Accept.Hashes = map[string]string{}
				}
				a.Accept.Hashes[p] = sum
				changed = true
			}
		}
	}
	if changed {
		a.persistAccept()
	}
}

func containsStr(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// acceptIntegrityCheck compares each locked file's current hash to the
// one recorded at approve/lock time (or the last unlocked edit). A
// mismatch means the file moved some other way — a bash trick the lock
// check did not catch, an external editor, anything — and is reported
// rather than silently trusted, whatever the test command itself says.
// Called with acceptMu held.
func (a *Agent) acceptIntegrityCheck() []string {
	if a.Accept == nil {
		return nil
	}
	var bad []string
	for _, p := range a.Accept.Files {
		want, ok := a.Accept.Hashes[p]
		if !ok {
			continue
		}
		got, err := hashFile(p)
		switch {
		case err != nil:
			bad = append(bad, relOrBase(p, a.Accept.Root)+" is missing or unreadable")
		case got != want:
			bad = append(bad, relOrBase(p, a.Accept.Root)+" changed outside a tool call")
		}
	}
	return bad
}

// acceptCapture handles the end of a writing-phase turn: it looks for
// the model's stated verification command and moves the mode to
// "proposed" for the user to review, nudges once for tests or a command
// that are missing, and otherwise falls back to an inferred command
// rather than nudging forever. It reports whether the turn should
// continue for another model call.
func (a *Agent) acceptCapture(text string, nudged *bool, emit func(Event)) bool {
	notice, harness := a.acceptCaptureLocked(text, nudged)
	if notice != "" {
		emit(Event{Kind: EvNotice, Text: notice})
	}
	if harness == "" {
		return false
	}
	a.appendHarnessText(harness)
	return true
}

// acceptCaptureLocked is acceptCapture's decision, made under acceptMu: the
// notice to show, and the harness text to send the model back with ("" to
// end the turn).
func (a *Agent) acceptCaptureLocked(text string, nudged *bool) (notice, harness string) {
	a.acceptMu.Lock()
	defer a.acceptMu.Unlock()
	if a.Accept == nil || a.Accept.Phase != session.AcceptWriting {
		return "", ""
	}
	if len(a.Accept.Files) == 0 {
		if *nudged {
			return "", "" // said once; a model that will not write tests is not made to by looping
		}
		*nudged = true
		return "accept: no test files written yet", "[accept] You have not written any test files yet. Write acceptance tests for the request, touching only test files, then end your message with a line \"ACCEPT-VERIFY: <command to run them>\"."
	}
	if m := acceptVerifyLine.FindStringSubmatch(text); m != nil {
		if cmd := cleanVerifyCommand(m[1]); cmd != "" {
			a.Accept.Proposed = cmd
			a.Accept.Phase = session.AcceptProposed
			a.persistAccept()
			return fmt.Sprintf("accept: %d test file(s) proposed · verifies with: %s · review, then /accept approve", len(a.Accept.Files), a.Accept.Proposed), ""
		}
		// Captured something, but it is not usable as one literal shell
		// command as stated — empty once any wrapping backticks/quotes
		// are stripped, or it spans more than one line. Running it
		// as-is is exactly how a backtick-wrapped command turned into a
		// command-substitution bug (see cleanVerifyCommand), so this
		// falls back the same way a missing command does rather than
		// storing something unsafe to run.
		if *nudged {
			a.Accept.Proposed = InferTestCommand(a.Accept.Files, a.Accept.Root, a.verifyCommands())
			a.Accept.Phase = session.AcceptProposed
			a.persistAccept()
			return "accept: the stated ACCEPT-VERIFY command was not usable as a single command; using an inferred one — check it before approving: " + a.Accept.Proposed, ""
		}
		*nudged = true
		return "accept: ACCEPT-VERIFY command was not usable", "[accept] The ACCEPT-VERIFY line must name exactly one single-line shell command, with nothing else on that line — no wrapping quotes or backticks needed. State it again."
	}
	if *nudged {
		a.Accept.Proposed = InferTestCommand(a.Accept.Files, a.Accept.Root, a.verifyCommands())
		a.Accept.Phase = session.AcceptProposed
		a.persistAccept()
		return "accept: no command was stated; using an inferred one — check it before approving: " + a.Accept.Proposed, ""
	}
	*nudged = true
	return "accept: no verification command stated", "[accept] End your message with a line of the exact form \"ACCEPT-VERIFY: <command>\" naming how to run the tests you just wrote."
}

// acceptVerifyGate is the locked tests' run command as an extra
// verification gate for this session only (not written to
// .janetcode/verify.json, and not applied to any other session): the
// turn is not done until it passes, on top of whatever the project's own
// verify.json already requires. Integrity is checked before every run —
// a tampered file fails the gate outright, without even running the
// command, since a passing result over changed tests would prove
// nothing.
func (a *Agent) acceptVerifyGate(ctx context.Context, g *gateState, emit func(Event)) bool {
	// The state is read under the lock and the command run without it:
	// the tests can take minutes, and a front-end must still be able to
	// run /accept off or /accept status meanwhile.
	a.acceptMu.Lock()
	acc := a.Accept
	if acc == nil || acc.Command == "" || (acc.Phase != session.AcceptLocked && acc.Phase != session.AcceptUnlocked) {
		a.acceptMu.Unlock()
		return false
	}
	if a.mutations == 0 || a.mutations == g.seen {
		a.acceptMu.Unlock()
		return false
	}
	g.seen = a.mutations
	command, root := acc.Command, acc.Root
	var reason string
	if bad := a.acceptIntegrityCheck(); len(bad) > 0 {
		reason = strings.Join(bad, "; ")
		acc.Tamper = reason
		a.persistAccept()
	}
	a.acceptMu.Unlock()
	if reason != "" {
		msg := "accept: integrity check failed — " + reason
		emit(Event{Kind: EvNotice, Text: msg, IsError: true})
		g.rounds++
		if g.rounds > verify.MaxRounds {
			emit(Event{Kind: EvNotice, Text: "accept: giving up after " + fmt.Sprint(verify.MaxRounds) + " rounds"})
			return false
		}
		a.appendHarnessText("[accept] " + msg + ". A locked acceptance test changed outside a tool call, which is reported instead of trusted. Restore the locked test files exactly as approved, or tell the user the tests need /accept unlock.")
		return true
	}
	emit(Event{Kind: EvNotice, Text: "accept: verifying locked tests · " + command})
	r := &verify.Runner{Config: verify.Config{Commands: []string{command}}, Cwd: root}
	res := r.Run(ctx)
	if ctx.Err() != nil {
		return false
	}
	a.acceptMu.Lock()
	// Only the mode this gate ran for records its result: an /accept off
	// or a new /accept <request> while the tests ran has replaced it.
	if a.Accept == acc {
		acc.Gate = &session.AcceptGateResult{At: time.Now(), Passed: res.OK(), Summary: res.Summary()}
		a.persistAccept()
	}
	a.acceptMu.Unlock()
	if res.OK() {
		emit(Event{Kind: EvNotice, Text: "accept: " + res.Summary()})
		return false
	}
	// Exit 126/127 means the command itself could not be run at all — not
	// found, or not executable — not that a test failed. Sending the
	// model back to "fix the code" for that burns whole rounds (a QA run
	// saw ~7 minutes of retries) on something no code change can fix; the
	// gate gives up on the first one and points at the one thing that
	// actually helps.
	if res.Exit == 126 || res.Exit == 127 {
		emit(Event{Kind: EvNotice, Text: fmt.Sprintf("accept: %s · the command itself did not run (exit %d) — this is not a failing test; fix it with /accept command <cmd> instead of retrying", res.Summary(), res.Exit), IsError: true})
		return false
	}
	g.rounds++
	if g.rounds > verify.MaxRounds {
		emit(Event{Kind: EvNotice, Text: "accept: " + res.Summary() + " · giving up after " + fmt.Sprint(verify.MaxRounds) + " rounds"})
		return false
	}
	emit(Event{Kind: EvNotice, Text: fmt.Sprintf("accept: %s · sending the model back to fix it (%d/%d)", res.Summary(), g.rounds, verify.MaxRounds)})
	a.appendHarnessText("[accept] The locked acceptance tests are this session's gate:\n\n" + res.Feedback())
	return true
}

// acceptSystemText is appended to the system prompt while accept mode is
// on, so the model is told the rules without paying for them once the
// mode ends (see systemPrompt in context.go).
func (a *Agent) acceptSystemText() string {
	a.acceptMu.Lock()
	defer a.acceptMu.Unlock()
	if a.Accept == nil {
		return ""
	}
	switch a.Accept.Phase {
	case session.AcceptWriting:
		return fmt.Sprintf("\n\n# Acceptance tests (writing)\nThe user wants acceptance tests for this request, approved before any implementation:\n\n%s\n\nWrite only test files: ones that fail against the code as it stands and would pass once the request is done. No implementation changes — every other mutating tool is refused while this phase lasts. When the tests are ready, end your final message with one line of the exact form:\nACCEPT-VERIFY: <command that runs the tests you wrote>\nThe user reviews the tests as a diff and runs /accept approve; only then does work on the implementation start.", a.Accept.Request)
	case session.AcceptProposed:
		return fmt.Sprintf("\n\n# Acceptance tests (proposed)\nAcceptance tests for %q are written and awaiting the user's /accept approve (proposed verification command: %s). Do not start implementing until it is approved.", a.Accept.Request, a.Accept.Proposed)
	case session.AcceptLocked:
		return fmt.Sprintf("\n\n# Acceptance tests (locked)\nThe acceptance tests for %q are approved and locked: %s. Any edit_file, write_file or bash command that would change them is refused — implement the code so they pass instead. They also gate this session: a turn is not done until `%s` passes. If a test is actually wrong, tell the user to run /accept unlock.", a.Accept.Request, strings.Join(relFiles(a.Accept.Files, a.Accept.Root), ", "), a.Accept.Command)
	case session.AcceptUnlocked:
		return fmt.Sprintf("\n\n# Acceptance tests (unlocked)\nThe locked acceptance tests for %q are temporarily unlocked so a wrong test can be fixed: %s. Change only what is actually wrong, then tell the user to run /accept lock again.", a.Accept.Request, strings.Join(relFiles(a.Accept.Files, a.Accept.Root), ", "))
	}
	return ""
}

func relFiles(files []string, root string) []string {
	out := make([]string, len(files))
	for i, f := range files {
		out[i] = relOrBase(f, root)
	}
	return out
}

// relOrBase is p relative to root when that is a real subpath, else p
// itself — for messages, so a locked file reads as "internal/foo_test.go"
// rather than the full absolute path when it can.
func relOrBase(p, root string) string {
	if root != "" {
		if rel, err := filepath.Rel(root, p); err == nil && !strings.HasPrefix(rel, "..") {
			return rel
		}
	}
	return p
}

// InferTestCommand guesses a command that runs files, from the
// project's own verify commands when one already looks like a check, or
// from per-language convention otherwise: go test ./<pkg>/..., pytest
// <files>, npm test, cargo test. It exists as a fallback for a
// test-writing turn that ends with no ACCEPT-VERIFY line — the model is
// still asked to state one first, because it knows which package or
// binary it actually wrote tests for, which this can only guess at from
// the file paths — and is kept to one convention per common ecosystem on
// purpose, so approval is never blocked waiting on a better guess.
func InferTestCommand(files []string, root string, verifyCommands []string) string {
	for _, c := range verifyCommands {
		if isCheck(c) {
			return c
		}
	}
	exts := map[string]bool{}
	for _, f := range files {
		exts[strings.ToLower(filepath.Ext(f))] = true
	}
	switch {
	case exts[".go"]:
		pkgs := map[string]bool{}
		for _, f := range files {
			if !strings.HasSuffix(f, "_test.go") {
				continue
			}
			pkgs[goPackageOf(f, root)] = true
		}
		if len(pkgs) == 1 {
			for p := range pkgs {
				if p != "" {
					return "go test " + p
				}
			}
		}
		return "go test ./..."
	case exts[".py"]:
		names := make([]string, 0, len(files))
		for _, f := range files {
			names = append(names, relOrBase(f, root))
		}
		sort.Strings(names)
		return "pytest " + strings.Join(names, " ")
	case exts[".js"], exts[".ts"], exts[".jsx"], exts[".tsx"], exts[".mjs"]:
		return "npm test"
	case exts[".rs"]:
		return "cargo test"
	}
	return ""
}

// goPackageOf is the ./... form of a Go test file's package, relative to
// root, or "" when it cannot be placed under root.
func goPackageOf(f, root string) string {
	dir := filepath.Dir(f)
	if root == "" {
		return ""
	}
	rel, err := filepath.Rel(root, dir)
	if err != nil || strings.HasPrefix(rel, "..") {
		return ""
	}
	if rel == "." {
		return "./..."
	}
	return "./" + filepath.ToSlash(rel) + "/..."
}
