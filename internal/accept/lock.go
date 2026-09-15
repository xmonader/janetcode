package accept

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

	"github.com/xmonader/janetcode/internal/fsutil"
	"github.com/xmonader/janetcode/internal/project"
	"github.com/xmonader/janetcode/internal/session"
	"github.com/xmonader/janetcode/internal/shell"
	"github.com/xmonader/janetcode/internal/textfmt"
	"github.com/xmonader/janetcode/internal/tool"
	"github.com/xmonader/janetcode/internal/verify"
)

func (m *Mode) rehash() error {
	hashes := map[string]string{}
	for _, p := range m.meta.Files {
		sum, err := hashFile(p)
		if err != nil {
			return fmt.Errorf("hash %s: %w", textfmt.RelTo(m.meta.Root, p), err)
		}
		hashes[p] = sum
	}
	m.meta.Hashes = hashes
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

// maxContentSize bounds how large a locked file's full content is
// kept for immediate restore, per file. A file over the cap still has
// its hash checked (integrityCheckLocked, RestoreTampered), so a
// change is still caught and reported — it just cannot be put back in
// place automatically, which an acceptance test this large would be an
// odd shape for anyway.
const maxContentSize = 256 * 1024

// captureContents (re)reads every locked file's content into
// Accept.Contents, alongside rehash's hashes. This is the content
// half of "locked means locked": a hash alone only tells the harness a
// file moved outside a tool call, after the fact; keeping the bytes
// themselves is what lets RestoreTampered put a tampered file back
// immediately, on its own, rather than merely reporting the tamper and
// waiting for the model or the user to fix it.
func (m *Mode) captureContents() {
	contents := make(map[string][]byte, len(m.meta.Files))
	for _, p := range m.meta.Files {
		data, err := os.ReadFile(p)
		if err != nil || len(data) > maxContentSize {
			continue
		}
		contents[p] = data
	}
	m.meta.Contents = contents
}

// RestoreTampered is the backstop behind the lock: called after
// every mutating tool call while accept mode is locked (see executeTool
// and runNested), whatever tool just ran. The pre-execution checks
// (Denial, built on the file tools' declared targets and
// shell.WriteTargets for bash) are the first line of defence, and are
// necessarily incomplete — a shell trick they do not recognise, a
// nested script or run_code call, anything at all that ends up writing
// to disk. This looks at what is actually there afterwards: any locked
// file whose hash no longer matches the one recorded at approve/lock
// time is restored immediately from the content captured then, and the
// tool call that caused it gets told so in its own result; the caller
// turns the same text into a notice for the user. A file too large to have kept in full (see
// maxContentSize) cannot be restored this way; it is still
// reported here, and integrityCheckLocked fails the verification gate
// over it regardless.
func (m *Mode) RestoreTampered(toolName string) string {
	if m == nil {
		return ""
	}
	return m.restoreTamperedLocked(toolName)
}

func (m *Mode) restoreTamperedLocked(toolName string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.meta == nil || m.meta.Phase != session.AcceptLocked {
		return ""
	}
	var restored, unrestorable []string
	for _, p := range m.meta.Files {
		want, ok := m.meta.Hashes[p]
		if !ok {
			continue
		}
		got, err := hashFile(p)
		if err == nil && got == want {
			continue
		}
		name := textfmt.RelTo(m.meta.Root, p)
		content, haveContent := m.meta.Contents[p]
		if !haveContent {
			unrestorable = append(unrestorable, name)
			continue
		}
		if werr := fsutil.AtomicWrite(p, content); werr != nil {
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
func (m *Mode) isLockedPath(abs string) bool {
	if m.meta == nil || m.meta.Phase != session.AcceptLocked {
		return false
	}
	abs = filepath.Clean(abs)
	for _, f := range m.meta.Files {
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

// Denial reports why a mutating call must not run because it
// touches a locked accept-test file, or "" when it may proceed. Covers
// exactly the two ways a tool call writes a file: a file tool's declared
// target (touchedPaths, shared with pathrules.go) and a bash command's
// write targets (shell.WriteTargets, built on the same path-escape
// analysis Classify uses for the workspace guard).
func (m *Mode) Denial(t tool.Tool, input json.RawMessage) string {
	if m == nil {
		return ""
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.meta == nil || m.meta.Phase != session.AcceptLocked {
		return ""
	}
	var hit string
	switch name := t.Name(); {
	case isEditTool(name):
		for _, p := range resolvedTouches(t, input) {
			if m.isLockedPath(p) {
				hit = p
				break
			}
		}
	case name == "bash":
		var in struct {
			Command string `json:"command"`
		}
		if json.Unmarshal(input, &in) == nil {
			for _, p := range shell.WriteTargets(in.Command, m.meta.Root) {
				if m.isLockedPath(p) {
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
		textfmt.RelTo(m.meta.Root, hit), m.meta.Request)
}

// WritingDenial reports why a mutating call must not run during
// the writing or proposed phase — every mutating tool is refused except
// a write_file or edit_file whose every touched path is a test file —
// or "" when the call may proceed. The restriction has to hold for both
// phases: a QA run found the writing-phase-only version of this check
// let the model edit a non-test file the moment its turn ended and the
// mode moved to "proposed", well before the user had a chance to run
// /accept approve. Called from both executeTool (the model's own calls)
// and runNested (script/execute_code/run_code calls), so a script cannot
// reach past it either.
func (m *Mode) WritingDenial(t tool.Tool, input json.RawMessage) string {
	if m == nil {
		return ""
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.meta == nil {
		return ""
	}
	switch m.meta.Phase {
	case session.AcceptWriting, session.AcceptProposed:
	default:
		return ""
	}
	if isEditTool(t.Name()) {
		paths := resolvedTouches(t, input)
		allTests := len(paths) > 0
		for _, p := range paths {
			if !project.IsTestFile(p) {
				allTests = false
				break
			}
		}
		if allTests {
			return ""
		}
	}
	if t.Name() == "bash" && m.phaseAllowsCommand(shell.CommandOf(string(input))) {
		return ""
	}
	if m.meta.Phase == session.AcceptProposed {
		return fmt.Sprintf("this session proposed acceptance tests for %q and is waiting on the user to run /accept approve: only test files may be written until then, or until /accept off. Wait for the user rather than starting the implementation.", m.meta.Request)
	}
	return fmt.Sprintf("this session is writing acceptance tests for %q: only test files may be written, and besides read-only commands and running the tests no other tool is available until the user reviews them and runs /accept approve. End your final message with a line \"ACCEPT-VERIFY: <command>\" naming how to run the tests you wrote.", m.meta.Request)
}

// phaseAllowsCommand reports whether a bash command may pass the
// writing/proposed phase's gate: a read-only command (shell.SafeRead) — the
// model has to look at the code to test it — or a run of the tests: the
// proposed command itself, or one simple command whose leading words run
// tests and that writes nothing it names. It only passes the mode gate;
// the command still needs approval like any other. Called with acceptMu
// held.
func (m *Mode) phaseAllowsCommand(cmd string) bool {
	cmd = shell.TrimWorkspaceCd(cmd, m.meta.Root)
	if cmd == "" {
		return false
	}
	if shell.SafeRead(cmd) || cmd == verify.CleanCommand(m.meta.Proposed) {
		return true
	}
	if strings.ContainsAny(strings.ReplaceAll(cmd, "2>&1", ""), ";|&`<>\n") || strings.Contains(cmd, "$(") {
		return false
	}
	words := strings.Fields(cmd)
	lead := strings.Join(words[:min(len(words), 3)], " ")
	return shell.IsTestRun(lead) && len(shell.WriteTargets(cmd, m.meta.Root)) == 0
}

// TrackEdit records a test file the writing phase just wrote, or
// refreshes a locked file's hash after a change made while unlocked —
// the only way a locked file's content may legitimately move, so the
// next integrity check sees it as expected rather than as tampering.
func (m *Mode) TrackEdit(t tool.Tool, input json.RawMessage) {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.meta == nil {
		return
	}
	paths := resolvedTouches(t, input)
	if len(paths) == 0 {
		return
	}
	changed := false
	switch m.meta.Phase {
	case session.AcceptProposed:
		// The proposed command was stated for the tests as they were; a
		// revision can drop or rename what it runs. Back to writing, so
		// the turn ends by stating the command again (see acceptCapture).
		if slices.ContainsFunc(paths, project.IsTestFile) {
			m.meta.Phase = session.AcceptWriting
			changed = true
		}
		fallthrough
	case session.AcceptWriting:
		for _, p := range paths {
			if !project.IsTestFile(p) || slices.Contains(m.meta.Files, p) {
				continue
			}
			m.meta.Files = append(m.meta.Files, p)
			sort.Strings(m.meta.Files)
			changed = true
		}
	case session.AcceptUnlocked:
		for _, p := range paths {
			if !slices.Contains(m.meta.Files, p) {
				continue
			}
			if sum, err := hashFile(p); err == nil {
				if m.meta.Hashes == nil {
					m.meta.Hashes = map[string]string{}
				}
				m.meta.Hashes[p] = sum
				changed = true
			}
		}
	}
	if changed {
		m.persist()
	}
}

// integrityCheckLocked compares each locked file's current hash to the
// one recorded at approve/lock time (or the last unlocked edit). A
// mismatch means the file moved some other way — a bash trick the lock
// check did not catch, an external editor, anything — and is reported
// rather than silently trusted, whatever the test command itself says.
// Called with acceptMu held.
func (m *Mode) integrityCheckLocked() []string {
	if m.meta == nil {
		return nil
	}
	var bad []string
	for _, p := range m.meta.Files {
		want, ok := m.meta.Hashes[p]
		if !ok {
			continue
		}
		got, err := hashFile(p)
		switch {
		case err != nil:
			bad = append(bad, textfmt.RelTo(m.meta.Root, p)+" is missing or unreadable")
		case got != want:
			bad = append(bad, textfmt.RelTo(m.meta.Root, p)+" changed outside a tool call")
		}
	}
	return bad
}

// isEditTool reports the tools that change files through the harness,
// never the shell — the same two agent's own AutoEdits mode covers.
func isEditTool(name string) bool { return name == "write_file" || name == "edit_file" }

// IntegrityCheck compares each locked file's current hash to the one
// recorded at approve/lock time, and reports what no longer matches.
func (m *Mode) IntegrityCheck() []string {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.integrityCheckLocked()
}
