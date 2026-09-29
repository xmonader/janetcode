package agent

import (
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/xmonader/janetcode/internal/fsutil"
)

// The checkpoint copy of a file the file tools are about to change used
// to be os.ReadFile into the checkpoint, and from there base64 into the
// undo sidecar: a write_file over a 2 GB dump held 2 GB in memory and
// wrote 2.7 GB of JSON. Copies are now sized on their stat first.
//
// inlineCopyBytes is the largest copy kept inside the checkpoint itself.
// Past it the bytes are streamed to a blob file beside the undo sidecar
// and the checkpoint holds the blob's path.
const inlineCopyBytes = 1 << 20

// maxCopyBytes is the largest file a checkpoint copies at all. Past it
// the checkpoint records that the file was too large to keep, the turn is
// told when it happens, and an undo leaves the file alone and says why,
// rather than a turn stalling on a copy of a disk image. A variable only
// so a test need not write 256 MiB.
var maxCopyBytes int64 = maxManifestBytes

// blobSuffix names the directory beside UndoPath that holds the copies
// too large to keep inline.
const blobSuffix = ".blobs"

// blobDir is where this agent's large checkpoint copies go: beside the
// undo sidecar when there is one, so they survive a resume, and a private
// temporary directory otherwise (an eval, a test, a subagent without a
// session). The fallback is only named here; streamBlob creates it when
// a copy is actually too large to keep inline. Creating it up front left
// an empty janetcode-cp-* directory in the temp dir for every agent
// without a session that took a checkpoint at all (what-if runs, evals).
func (k *checkpoints) blobDir() string {
	if k.UndoPath != "" {
		return k.UndoPath + blobSuffix
	}
	k.blobTemp.once.Do(func() {
		var b [8]byte
		_, _ = rand.Read(b[:])
		k.blobTemp.dir = filepath.Join(os.TempDir(), fmt.Sprintf("janetcode-cp-%d-%x", os.Getpid(), b))
	})
	return k.blobTemp.dir
}

// blobTemp is blobDir's fallback directory, named on first use.
type blobTemp struct {
	once sync.Once
	dir  string
}

// captureCopy records path's current state for a checkpoint, sized on its
// stat: absent, unreadable, too large to keep, kept inline, or streamed to
// a blob under dir.
func captureCopy(path, dir string) Snapshot {
	s := Snapshot{Path: path}
	st, err := os.Stat(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return s
	case err != nil:
		s.Failed = true
		return s
	case st.Mode().IsRegular() && st.Size() > maxCopyBytes:
		s.Existed, s.TooLarge, s.Size = true, true, st.Size()
		return s
	case st.Mode().IsRegular() && st.Size() > inlineCopyBytes && dir != "":
		blob, err := streamBlob(path, dir)
		if err != nil {
			s.Failed = true
			return s
		}
		s.Existed, s.Blob, s.Size = true, blob, st.Size()
		return s
	}
	data, err := os.ReadFile(path)
	if err != nil {
		// Unreadable: the checkpoint records that it could not be
		// captured rather than quietly holding nothing, which an undo
		// would otherwise write over the file as if it were the state
		// before the turn.
		s.Failed = true
		return s
	}
	s.Data, s.Existed = data, true
	return s
}

// streamBlob copies path into dir under the sha256 of its content and
// returns the blob's path. The copy is streamed, so its size costs time
// and disk, never memory; identical content is stored once.
func streamBlob(path, dir string) (string, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	in, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer in.Close()
	tmp, err := os.CreateTemp(dir, ".blob-*")
	if err != nil {
		return "", err
	}
	defer os.Remove(tmp.Name()) // a no-op once renamed
	h := sha256.New()
	_, err = io.Copy(io.MultiWriter(tmp, h), in)
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return "", err
	}
	blob := filepath.Join(dir, fmt.Sprintf("%x", h.Sum(nil)))
	if err := os.Chmod(tmp.Name(), fsutil.PrivateFileMode); err != nil {
		return "", err
	}
	if err := os.Rename(tmp.Name(), blob); err != nil {
		return "", err
	}
	return blob, nil
}

// restoreCopy writes a checkpoint copy back to its path: from the blob
// when it was kept as one, streamed through a temporary file beside the
// target and renamed over it, so an interrupted restore never leaves half
// a file.
func restoreCopy(f Snapshot) error {
	if f.Blob == "" {
		return fsutil.AtomicWrite(f.Path, f.Data)
	}
	in, err := os.Open(f.Blob)
	if err != nil {
		return fmt.Errorf("its copy from before the turn is gone (%w)", err)
	}
	defer in.Close()
	target := f.Path
	if real, err := filepath.EvalSymlinks(target); err == nil {
		target = real
	}
	mode := fsutil.DefaultFileMode
	if st, err := os.Stat(target); err == nil {
		mode = st.Mode().Perm()
	}
	tmp, err := os.CreateTemp(filepath.Dir(target), "."+filepath.Base(target)+".tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	_, err = io.Copy(tmp, in)
	if err == nil {
		err = tmp.Chmod(mode)
	}
	if err == nil {
		err = tmp.Sync()
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	return os.Rename(tmp.Name(), target)
}

// tooLargeReason is what an undo says about a file its checkpoint could
// not afford to copy.
func tooLargeReason(f Snapshot) string {
	return fmt.Sprintf("it was %d MiB before the turn, over the %d MiB a checkpoint copies, so that state was not kept and it was left as it is", f.Size>>20, maxCopyBytes>>20)
}

// collectBlobs removes the blobs under dir that no checkpoint in keep
// names any more: a trimmed checkpoint, a dropped redo state. Only files
// named as a blob is (a sha256 in hex) are considered, so nothing else
// that ended up in the directory is touched.
func collectBlobs(dir string, keep ...[]Checkpoint) {
	if dir == "" {
		return
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	used := map[string]bool{}
	for _, list := range keep {
		for _, cp := range list {
			for _, f := range cp.Files {
				if f.Blob != "" {
					used[filepath.Base(f.Blob)] = true
				}
			}
		}
	}
	for _, e := range entries {
		name := e.Name()
		if len(name) != 64 || strings.Trim(name, "0123456789abcdef") != "" || used[name] {
			continue
		}
		os.Remove(filepath.Join(dir, name))
	}
}
