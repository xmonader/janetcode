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
