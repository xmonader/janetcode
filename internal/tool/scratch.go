package tool

import (
	"os"
	"path/filepath"
	"strings"
)

// ScratchDirName is the directory a headless session's own check file
// lives in, under the user's cache dir (janetcode/checks/<session>). It is
// outside the repository, so what the model writes there can never reach
// the diff a benchmark or a reviewer reads, and the name carries "check"
// on purpose: shell.IsCheck is a word match, so any command that runs a
// file from here is read as a check by the verdict ledger whatever the
// file is called.
const ScratchDirName = "checks"

// scratchSegment is how a path is recognised as one of these directories.
var scratchSegment = "/janetcode/" + ScratchDirName + "/"

// IsScratchPath reports whether a path is inside a session scratch
// directory. The model's edits to its own check file are not the work the
// verdict judges: counted as edits, a turn that only reworked its check
// was reported as an unverified change.
func IsScratchPath(p string) bool {
	return strings.Contains(filepath.ToSlash(p), scratchSegment)
}

// OpenScratch creates the scratch directory for a session and returns it
// with the function that removes it. The id is cut to its last element so
// a odd one cannot climb out of the cache dir. The cache dir is the one
// place a login user can always write that is neither the repository nor
// a temp dir swept by the system mid-run.
func OpenScratch(sessionID string) (dir string, remove func(), err error) {
	base, err := os.UserCacheDir()
	if err != nil {
		return "", nil, err
	}
	id := filepath.Base(filepath.Clean("/" + sessionID))
	if id == "/" || id == "." {
		id = "session"
	}
	dir = filepath.Join(base, "janetcode", ScratchDirName, id)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", nil, err
	}
	return dir, func() { _ = os.RemoveAll(dir) }, nil
}
