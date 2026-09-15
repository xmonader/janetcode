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
	// lastUser is the prompt of the running or last turn.
	lastUser string
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
	if n := len(h.entries); n == 0 || h.entries[n-1] != e {
		h.entries = append(h.entries, e)
		if h.file != "" {
			if err := appendPromptHistory(h.file, e, len(h.entries)); err == nil && len(h.entries) > histCap {
				h.entries = h.entries[len(h.entries)-histCap:]
				rewritePromptHistory(h.file, h.entries)
			}
		}
	}
	h.idx = len(h.entries)
	h.draft = ""
}

// recall renders one history entry back into the input's own language.
func (h *historyState) recall(i int) string {
	e := h.entries[i]
	if e.Bash {
		return "!" + e.Text
	}
	return e.Text
}

// back steps to the previous prompt, saving the draft on the way in. It
// reports whether there was one to step to.
func (h *historyState) back(draft string) (string, bool) {
	if len(h.entries) == 0 || h.idx == 0 {
		return "", false
	}
	if h.idx == len(h.entries) {
		h.draft = draft
	}
	h.idx--
	return h.recall(h.idx), true
}

// forward steps back towards the draft, which is what the last step
// hands back.
func (h *historyState) forward() (string, bool) {
	if h.idx >= len(h.entries) {
		return "", false
	}
	h.idx++
	if h.idx == len(h.entries) {
		return h.draft, true
	}
	return h.recall(h.idx), true
}

// cancel leaves the history, handing back what was being typed before
// the first step up went looking, rather than throwing it away.
func (h *historyState) cancel() (string, bool) {
	if h.idx >= len(h.entries) {
		return "", false
	}
	h.idx = len(h.entries)
	return h.draft, true
}

// histPath is the history file for one working directory, keyed by a hash
// of the path so two projects with the same base name never share one.
func histPath(sessionsDir, cwd string) string {
	sum := sha256.Sum256([]byte(cwd))
	return filepath.Join(filepath.Dir(sessionsDir), "history", hex.EncodeToString(sum[:8])+".jsonl")
}

// loadHistory reads the prompts recorded for this directory, oldest first.
// A damaged line is skipped rather than losing the rest of the file.
func loadPromptHistory(path string) []histEntry {
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
	if len(out) > histCap {
		out = out[len(out)-histCap:]
	}
	return out
}

// appendPromptHistory adds one prompt to the file, rewriting it when it
// has grown past the cap.
func appendPromptHistory(path string, e histEntry, count int) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	if count > histCap {
		// Past the cap the file is rewritten with the tail we hold.
		return nil
	}
	line, err := json.Marshal(e)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.Write(append(line, '\n'))
	return err
}

// rewritePromptHistory writes the whole kept history back, for the trim
// that follows the cap being reached.
func rewritePromptHistory(path string, entries []histEntry) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	var sb strings.Builder
	for _, e := range entries {
		line, err := json.Marshal(e)
		if err != nil {
			continue
		}
		sb.Write(line)
		sb.WriteByte('\n')
	}
	return os.WriteFile(path, []byte(sb.String()), 0o600)
}
