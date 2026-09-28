// Package fsutil provides small, dependency-free filesystem helpers shared
// across janetcode packages that would otherwise each hand-roll the same
// atomic-write dance.
package fsutil

import (
	"os"
	"path/filepath"
)

// WriteFileAtomic writes data to path via a temp file created in the same
// directory followed by a rename, so a crash or concurrent reader never
// observes a truncated or partially written file. The temp file (and so the
// final file) is always chmod'd to perm — callers that want to preserve an
// existing file's mode instead of forcing one should stat the target first
// and pass that mode as perm.
func WriteFileAtomic(path string, data []byte, perm os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpName) }

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Chmod(perm); err != nil {
		tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		cleanup()
		return err
	}
	syncDir(filepath.Dir(path))
	return nil
}

// syncDir forces the rename itself to durable storage. Syncing the temp
// file only guarantees its contents; the directory entry pointing at them
// is a separate write, and without this a crash just after the rename can
// leave the old file, or no file at all where a new one was created —
// which for a session log means the turn that was just written is gone
// even though the data reached the platter.
//
// Best effort on purpose: not every platform lets a directory be opened
// (Windows does not) or synced, and a write that has already landed must
// not be reported as failed because the extra durability step could not
// be taken.
func syncDir(dir string) {
	d, err := os.Open(dir)
	if err != nil {
		return
	}
	_ = d.Sync()
	_ = d.Close()
}

// DefaultFileMode is what a new file gets when the caller does not say.
// It is what os.WriteFile's usual argument is, and the right default for
// anything a person is expected to read with other tools.
const DefaultFileMode = os.FileMode(0o644)

// PrivateFileMode is the mode for files that hold a conversation, a
// memory, a credential or anything else recorded on the user's behalf:
// readable by the user and nobody else. Session logs, memory entries and
// configuration are written with it, so a shared machine does not leak a
// transcript through a default umask.
const PrivateFileMode = os.FileMode(0o600)

// AtomicWrite replaces the file at p with data via a temp file and rename,
// so a crash mid-write never leaves a truncated file. An existing file keeps
// its mode; a new one is created DefaultFileMode.
//
// Anything written on the user's behalf rather than for the project should
// call AtomicWriteMode with PrivateFileMode instead.
func AtomicWrite(p string, data []byte) error {
	return AtomicWriteMode(p, data, DefaultFileMode)
}

// AtomicWriteMode is AtomicWrite with the mode a new file is created
// with. An existing file still keeps the mode it has: tightening a file
// the user has deliberately opened up, or loosening one they have
// tightened, is not this function's decision to make.
func AtomicWriteMode(p string, data []byte, newFile os.FileMode) error {
	// A symlink is written through, not replaced: the rename would
	// otherwise land on the link itself and turn it into a regular file,
	// leaving the file it named untouched — CLAUDE.md -> AGENTS.md edited
	// into two diverging copies. The temp file then goes beside the
	// target, which is where the rename has to happen anyway. A dangling
	// link resolves to nothing and is written at p as before.
	if st, err := os.Lstat(p); err == nil && st.Mode()&os.ModeSymlink != 0 {
		if real, err := filepath.EvalSymlinks(p); err == nil {
			p = real
		}
	}
	mode := newFile
	if st, err := os.Stat(p); err == nil {
		mode = st.Mode().Perm()
	}
	return WriteFileAtomic(p, data, mode)
}
