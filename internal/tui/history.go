package tui

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// The prompt history used to die with the process: reopening janetcode in
// the same tree lost every line you had typed. It is now a JSONL file per
// working directory, beside the sessions, one record per prompt:
//
//	{"text":"run the tests","bash":false}
//
// The bash flag remembers that the line was typed with a "!" prefix, so
// recalling it restores the shell prompt rather than sending the command
// to the model as a question.
type histEntry struct {
	Text string `json:"text"`
	Bash bool   `json:"bash,omitempty"`
	// full is Text with its paste chips put back, "" when it has none.
	// Only this process knows what "[paste #1 +40 lines]" stands for: the
	// numbering starts again at 1 in the next one, so a chip written to
	// the file would recall as whatever that process pasted first. The
	// file gets the full text; the in-memory entry keeps the chip.
	full string
}

// stored is the entry as it is written to the file.
func (e histEntry) stored() histEntry {
	if e.full != "" {
		return histEntry{Text: e.full, Bash: e.Bash}
	}
	return e
}

// histCap is how many prompts are kept. Beyond that the oldest go: the
// file is a convenience, not a record — the sessions are the record.
const histCap = 1000

// historyState is the up/down recall: the prompts submitted in this
// directory, where the browsing cursor sits, and the draft that was in
// the input when browsing started, so esc can put it back. idx ==
// len(entries) means "not browsing".
type historyState struct {
	entries []histEntry
	file    string
	idx     int    // == len(entries) when not browsing
	draft   string // input saved while browsing
	// edits holds what the input said at an entry when the browse
	// stepped off it, if that differs from the entry: as in a shell, an
	// edited recall comes back edited on the way back to it, until the
	// browse ends. The entries themselves never change.
	edits map[int]string
	// lastUser is the prompt of the running or last turn.
	lastUser string
	// expand puts paste chips back into a prompt (model.inflatePastes).
	expand func(string) string
}

// load reads the history recorded for one working directory.
func (h *historyState) load(sessionsDir, cwd string) {
	h.file = histPath(sessionsDir, cwd)
	h.entries = loadPromptHistory(h.file)
	h.idx = len(h.entries)
}

// remember adds a submitted prompt to the up/down history and to the
// file that outlives the process. A "!" command keeps its flag so
// recalling it restores the shell prompt rather than asking the model.
func (h *historyState) remember(text string) {
	e := histEntry{Text: text}
	if strings.HasPrefix(text, "!") {
		e = histEntry{Text: strings.TrimPrefix(text, "!"), Bash: true}
	}
	if e.Text == "" {
		return
	}
	if h.expand != nil {
		if full := h.expand(e.Text); full != e.Text {
			e.full = full
		}
	}
	if n := len(h.entries); n == 0 || h.entries[n-1] != e {
		h.entries = append(h.entries, e)
		if len(h.entries) > histCap {
			h.entries = h.entries[len(h.entries)-histCap:]
		}
		if h.file != "" {
			_ = savePromptHistory(h.file, e)
		}
	}
	h.idx = len(h.entries)
	h.draft = ""
	h.edits = nil
}

// recall renders one history entry back into the input's own language.
func (h *historyState) recall(i int) string {
	e := h.entries[i]
	if e.Bash {
		return "!" + e.Text
	}
	return e.Text
}

// shown is what the input holds at entry i: the edit made to it during
// this browse, or the entry as recalled.
func (h *historyState) shown(i int) string {
	if e, ok := h.edits[i]; ok {
		return e
	}
	return h.recall(i)
}

// keep records what the input says as the browse steps off the entry it
// is on, so an edit to a recalled prompt is not lost to ↑ or ↓.
func (h *historyState) keep(cur string) {
	if h.idx >= len(h.entries) {
		return
	}
	if cur == h.recall(h.idx) {
		delete(h.edits, h.idx)
		return
	}
	if h.edits == nil {
		h.edits = map[int]string{}
	}
	h.edits[h.idx] = cur
}

// back steps to the previous prompt, saving the draft on the way in (or
// the edit to the entry it leaves). It reports whether there was one to
// step to.
func (h *historyState) back(cur string) (string, bool) {
	if len(h.entries) == 0 || h.idx == 0 {
		return "", false
	}
	if h.idx == len(h.entries) {
		h.draft = cur
	} else {
		h.keep(cur)
	}
	h.idx--
	return h.shown(h.idx), true
}

// forward steps back towards the draft, which is what the last step
// hands back, keeping the edit to the entry it leaves.
func (h *historyState) forward(cur string) (string, bool) {
	if h.idx >= len(h.entries) {
		return "", false
	}
	h.keep(cur)
	h.idx++
	if h.idx == len(h.entries) {
		return h.draft, true
	}
	return h.shown(h.idx), true
}

// cancel leaves the history, handing back what was being typed before
// the first step up went looking, rather than throwing it away.
func (h *historyState) cancel() (string, bool) {
	if h.idx >= len(h.entries) {
		return "", false
	}
	h.idx = len(h.entries)
	h.edits = nil
	return h.draft, true
}

// leave stops browsing without handing anything back: the input was
// cleared, so there is no draft to restore and the next step up starts
// from the newest prompt again.
func (h *historyState) leave() {
	h.idx = len(h.entries)
	h.draft = ""
	h.edits = nil
}

// histPath is the history file for one working directory, keyed by a hash
// of the path so two projects with the same base name never share one.
func histPath(sessionsDir, cwd string) string {
	sum := sha256.Sum256([]byte(cwd))
	return filepath.Join(filepath.Dir(sessionsDir), "history", hex.EncodeToString(sum[:8])+".jsonl")
}

// loadPromptHistory reads the prompts recorded for this directory,
// oldest first, at most histCap of them.
func loadPromptHistory(path string) []histEntry {
	out := readPromptHistory(path)
	if len(out) > histCap {
		out = out[len(out)-histCap:]
	}
	return out
}

// readPromptHistory reads every prompt in the file, oldest first. A
// damaged line is skipped rather than losing the rest of the file.
func readPromptHistory(path string) []histEntry {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var out []histEntry
	for _, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var e histEntry
		if json.Unmarshal([]byte(line), &e) == nil && e.Text != "" {
			out = append(out, e)
		}
	}
	return out
}

// savePromptHistory adds one prompt to the file. Two sessions in the same
// directory share the file, so the save works from what is on disk, never
// from this process's copy: each used to rewrite the file from its own
// entries once the cap was reached, and the second to save erased what
// the first had just added. Under an advisory lock (a sidecar file, since
// the rewrite replaces the history file itself) the prompt is appended,
// skipped when it repeats the newest line, and once the file is past the
// cap its tail is written to a temporary file and renamed over it, so a
// reader never sees half a file.
func savePromptHistory(path string, e histEntry) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	lf, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer lf.Close()
	if err := lockHistory(lf); err != nil {
		return err
	}
	defer unlockHistory(lf)

	e = e.stored()
	disk := readPromptHistory(path)
	if n := len(disk); n > 0 && disk[n-1] == e {
		return nil
	}
	if len(disk)+1 > histCap {
		disk = append(disk, e)
		return rewritePromptHistory(path, disk[len(disk)-histCap:])
	}
	line, err := json.Marshal(e)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(append(line, '\n')); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// rewritePromptHistory replaces the file with entries, through a
// temporary file renamed over it. Callers that share the file hold the
// history lock.
func rewritePromptHistory(path string, entries []histEntry) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	var sb strings.Builder
	for _, e := range entries {
		line, err := json.Marshal(e.stored())
		if err != nil {
			continue
		}
		sb.Write(line)
		sb.WriteByte('\n')
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".tmp*")
	if err != nil {
		return err
	}
	if _, err := tmp.WriteString(sb.String()); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return nil
}
