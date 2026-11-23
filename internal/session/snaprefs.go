package session

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// commitID matches a git object id as it appears in an undo record.
var commitID = regexp.MustCompile(`[0-9a-f]{40,64}`)

// SnapshotRefs returns the commit ids named by the undo records of
// sessions modified since the given time. Sessions do not store snapshot
// hashes in their transcript; the checkpoints that hold them live in the
// <id>.undo.json file beside it. The ids are taken as bare hex tokens
// rather than by decoding the file, so the answer survives a change to
// the record's shape, and a token that is not a snapshot is harmless: it
// only keeps a ref nobody has. Reading the files is cheap: they are small
// and no transcript is opened.
func SnapshotRefs(dir string, since time.Time) map[string]bool {
	refs := map[string]bool{}
	files, _ := filepath.Glob(filepath.Join(dir, "*.undo.json"))
	for _, f := range files {
		// The session's own file says when it was last used; fall back
		// to the undo record when the transcript was never written.
		st, err := os.Stat(strings.TrimSuffix(f, ".undo.json") + ".jsonl")
		if err != nil {
			if st, err = os.Stat(f); err != nil {
				continue
			}
		}
		if st.ModTime().Before(since) {
			continue
		}
		data, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		for _, id := range commitID.FindAll(data, -1) {
			refs[string(id)] = true
		}
	}
	return refs
}
