package agent

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/xmonader/janetcode/internal/session"
	"github.com/xmonader/janetcode/internal/textfmt"
	"github.com/xmonader/janetcode/internal/tool"
	"github.com/xmonader/janetcode/internal/verify"
)

func (a *Agent) rehashAccept() error {
	hashes := map[string]string{}
	for _, p := range a.Accept.Files {
		sum, err := hashFile(p)
		if err != nil {
			return fmt.Errorf("hash %s: %w", textfmt.RelTo(a.Accept.Root, p), err)
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
		name := textfmt.RelTo(a.Accept.Root, p)
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
	switch name := t.Name(); {
	case editTool(name):
		for _, p := range resolvedTouches(t, input) {
			if a.isLockedPath(p) {
				hit = p
				break
			}
		}
	case name == "bash":
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
		textfmt.RelTo(a.Accept.Root, hit), a.Accept.Request)
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
	if editTool(t.Name()) {
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
	if t.Name() == "bash" && a.acceptPhaseAllowsCommand(commandOf(string(input))) {
		return ""
	}
	if a.Accept.Phase == session.AcceptProposed {
		return fmt.Sprintf("this session proposed acceptance tests for %q and is waiting on the user to run /accept approve: only test files may be written until then, or until /accept off. Wait for the user rather than starting the implementation.", a.Accept.Request)
	}
	return fmt.Sprintf("this session is writing acceptance tests for %q: only test files may be written, and besides read-only commands and running the tests no other tool is available until the user reviews them and runs /accept approve. End your final message with a line \"ACCEPT-VERIFY: <command>\" naming how to run the tests you wrote.", a.Accept.Request)
}

// acceptPhaseAllowsCommand reports whether a bash command may pass the
// writing/proposed phase's gate: a read-only command (tool.SafeRead) — the
// model has to look at the code to test it — or a run of the tests: the
// proposed command itself, or one simple command whose leading words run
// tests and that writes nothing it names. It only passes the mode gate;
// the command still needs approval like any other. Called with acceptMu
// held.
func (a *Agent) acceptPhaseAllowsCommand(cmd string) bool {
	cmd = tool.TrimWorkspaceCd(cmd, a.Accept.Root)
	if cmd == "" {
		return false
	}
	if tool.SafeRead(cmd) || cmd == verify.CleanCommand(a.Accept.Proposed) {
		return true
	}
	if strings.ContainsAny(strings.ReplaceAll(cmd, "2>&1", ""), ";|&`<>\n") || strings.Contains(cmd, "$(") {
		return false
	}
	words := strings.Fields(cmd)
	lead := strings.Join(words[:min(len(words), 3)], " ")
	return testRunRe.MatchString(lead) && len(tool.WriteTargets(cmd, a.Accept.Root)) == 0
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
	case session.AcceptProposed:
		// The proposed command was stated for the tests as they were; a
		// revision can drop or rename what it runs. Back to writing, so
		// the turn ends by stating the command again (see acceptCapture).
		if slices.ContainsFunc(paths, tool.IsTestFile) {
			a.Accept.Phase = session.AcceptWriting
			changed = true
		}
		fallthrough
	case session.AcceptWriting:
		for _, p := range paths {
			if !tool.IsTestFile(p) || slices.Contains(a.Accept.Files, p) {
				continue
			}
			a.Accept.Files = append(a.Accept.Files, p)
			sort.Strings(a.Accept.Files)
			changed = true
		}
	case session.AcceptUnlocked:
		for _, p := range paths {
			if !slices.Contains(a.Accept.Files, p) {
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
			bad = append(bad, textfmt.RelTo(a.Accept.Root, p)+" is missing or unreadable")
		case got != want:
			bad = append(bad, textfmt.RelTo(a.Accept.Root, p)+" changed outside a tool call")
		}
	}
	return bad
}
