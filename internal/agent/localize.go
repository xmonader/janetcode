package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/xmonader/janetcode/internal/localize"
	"github.com/xmonader/janetcode/internal/session"
	"github.com/xmonader/janetcode/internal/shell"
)

// Failure localization (LocalizeFailures): when a test run comes back red
// after the turn changed more than one hunk, whether the gate ran it or
// the model's own bash call did, the harness works out which of the
// turn's hunks make the failing tests fail and which functions the
// failing tests ran that the passing ones did not (internal/localize),
// and appends that to the failing output. The model is not offered a
// tool for it: a localize tool the model must choose to call is the kind
// of mechanism this harness has measured going unused.
//
// It is opt-in. Nothing it does calls a model, but every localization
// reruns the failing tests in a throwaway worktree, up to
// localize.DefaultBudget, while the model waits, and whether that buys
// fewer calls from red to green is unmeasured.

// maxLocalizePerTurn bounds the localizations one turn pays for.
const maxLocalizePerTurn = 3

// localizeState is one turn's record of what was localized, so the same
// red state — the same edits, the same failing output — is not worked out
// twice when the model reruns the tests without changing anything.
type localizeState struct {
	mu   sync.Mutex
	turn int
	n    int
	done map[string]bool
}

// claim reports whether a localization keyed k may run in turn, and
// counts it.
func (s *localizeState) claim(turn int, k string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.turn != turn {
		s.turn, s.n, s.done = turn, 0, map[string]bool{}
	}
	if s.done[k] || s.n >= maxLocalizePerTurn {
		return false
	}
	s.done[k] = true
	s.n++
	return true
}

// localizeBash is the note for a bash call's result: something only when
// the call was a check, it came back red, and localization found
// something. output is the call's result before it was archived.
func (a *Agent) localizeBash(ctx context.Context, name string, input []byte, output string, isErr bool, emit func(Event)) string {
	if name != "bash" || isErr || !a.LocalizeOn() || a.Off["verify"] {
		return ""
	}
	cmd := shell.TrimWorkspaceCd(shell.CommandOf(string(input)), a.RootDir())
	e := session.Event{Name: name, Arguments: string(input), Output: output}
	if !shell.IsCheck(cmd) || !runFailed(e) || neverRan(e) {
		return ""
	}
	return a.localizeFailure(ctx, output, emit)
}

// localizeFailure localizes a red run's output against the turn's edits
// and returns the text to append to it, with its leading blank line, or
// "" when there is nothing to say.
func (a *Agent) localizeFailure(ctx context.Context, output string, emit func(Event)) string {
	if !a.LocalizeOn() || a.Localize == nil || a.readOnly() || a.DryRunOn() {
		return ""
	}
	edits := a.turnEdits()
	if len(localize.Units(edits)) < localize.MinHunks {
		return ""
	}
	if !a.localized.claim(a.turnN, localizeKey(edits, output)) {
		return ""
	}
	emit(Event{Kind: EvNotice, Text: "localizing the failure against this turn's hunks"})
	rep := a.Localize(ctx, localize.Request{Root: a.RootDir(), Edits: edits, Output: output})
	if ctx.Err() != nil {
		return ""
	}
	emit(Event{Kind: EvNotice, Text: rep.Summary()})
	a.logEvent(session.Event{Type: session.TypeNotice, Turn: a.turnN, Name: "localize", Output: rep.Summary()})
	if !rep.Found() {
		return ""
	}
	return "\n\n" + rep.Text(0)
}

// turnEdits is every file this turn's tool calls changed, as the turn
// found it and as it is on disk now, paths relative to the root. A change
// since undone counts for nothing; a file outside the root is left out,
// since the throwaway worktree holds only the repository.
func (a *Agent) turnEdits() []localize.Edit {
	root := a.RootDir()
	byPath := map[string]*localize.Edit{}
	var order []string
	for _, c := range a.Changes() {
		if c.Turn != a.turnN || c.Undone {
			continue
		}
		if _, ok := byPath[c.Path]; ok {
			continue
		}
		rel, err := filepath.Rel(root, c.Path)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
			continue
		}
		byPath[c.Path] = &localize.Edit{Path: filepath.ToSlash(rel), Existed: c.Existed, Before: c.Before}
		order = append(order, c.Path)
	}
	var out []localize.Edit
	for _, p := range order {
		e := byPath[p]
		if data, err := os.ReadFile(p); err == nil {
			e.After, e.Exists = data, true
		}
		if e.Exists == e.Existed && string(e.After) == string(e.Before) {
			continue
		}
		out = append(out, *e)
	}
	return out
}

// localizeKey identifies a red state: the edits as they stand and the
// failing output's test lines (timings and addresses vary between two
// runs of the same failure, so the whole output would never repeat).
func localizeKey(edits []localize.Edit, output string) string {
	h := sha256.New()
	for _, e := range edits {
		h.Write([]byte(e.Path + "\x00"))
		h.Write(e.After)
		h.Write([]byte{0})
	}
	var fails []string
	for _, l := range strings.Split(output, "\n") {
		if strings.HasPrefix(l, "--- FAIL: ") {
			name, _, _ := strings.Cut(strings.TrimPrefix(l, "--- FAIL: "), " ")
			fails = append(fails, name)
		}
	}
	sort.Strings(fails)
	h.Write([]byte(strings.Join(fails, "\n")))
	return hex.EncodeToString(h.Sum(nil))
}
