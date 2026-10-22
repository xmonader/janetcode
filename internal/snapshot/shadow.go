package snapshot

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/xmonader/janetcode/internal/gitx"
)

// Outside a git repository /undo used to have only the copies the file
// tools made: a turn whose `sed -i`, formatter or `rm` changed files left
// nothing an undo could put back, and said nothing about it. A shadow
// repository gives such a directory the same whole-tree snapshots a git
// work tree gets, from a bare repository in janetcode's own data
// directory that points at the directory as its work tree. Nothing is
// written into the directory itself, HEAD never moves (the store has no
// branch), and the snapshots are the only commits it ever holds.

// Shadow store bounds: a directory past either is not snapshotted, since
// every mutating turn would copy it into the store. $HOME or / as a
// working directory is the usual way to get there.
const (
	maxShadowFiles = 20000
	maxShadowBytes = 512 << 20
)

// ErrShadowTooLarge means the directory holds too much to snapshot on
// every mutating turn.
var ErrShadowTooLarge = errors.New("too large to snapshot")

// OpenShadow opens (creating on first use) the shadow repository under
// store for dir, a directory that is not inside a git work tree. It
// refuses a directory past the shadow bounds, the file system root and
// the home directory.
func OpenShadow(ctx context.Context, dir, store string) (*Repo, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	if real, err := filepath.EvalSymlinks(abs); err == nil {
		abs = real
	}
	// abs is resolved, so home must be too: a $HOME that is a symlink
	// would otherwise never compare equal.
	home, herr := os.UserHomeDir()
	if herr == nil {
		home = filepath.Clean(home)
		if real, err := filepath.EvalSymlinks(home); err == nil {
			home = real
		}
	}
	if herr == nil && home == abs || filepath.Dir(abs) == abs {
		return nil, fmt.Errorf("%s: %w (a home directory or the file system root)", abs, ErrShadowTooLarge)
	}
	if err := checkShadowSize(ctx, abs); err != nil {
		return nil, err
	}
	sum := sha256.Sum256([]byte(abs))
	gitDir := filepath.Join(store, hex.EncodeToString(sum[:10])+".git")
	if _, err := os.Stat(filepath.Join(gitDir, "HEAD")); err != nil {
		if err := os.MkdirAll(store, 0o700); err != nil {
			return nil, err
		}
		if _, err := gitx.Run(ctx, store, nil, "init", "-q", "--bare", gitDir); err != nil {
			return nil, err
		}
		// Private: the store holds the content of every file the tree
		// had when a turn began.
		_ = os.Chmod(gitDir, 0o700)
	}
	// The store is janetcode's own, so no one's git configuration has any
	// business in it: not the system's or the user's (a global
	// core.autocrlf, a hooks path, an fsmonitor), only the overrides that
	// keep the bytes exact.
	r := &Repo{Root: abs, gitDir: gitDir, env: []string{"GIT_DIR=" + gitDir, "GIT_WORK_TREE=" + abs, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=" + os.DevNull}}
	r.raw = r.byteExact(ctx)
	return r, nil
}

// checkShadowSize walks dir and stops at the first bound it passes. A
// nested repository's .git is not counted: git records it as a gitlink,
// not its files.
func checkShadowSize(ctx context.Context, dir string) error {
	files, bytes := 0, int64(0)
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // unreadable: git will skip it too
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if d.IsDir() {
			if d.Name() == ".git" && p != dir {
				return filepath.SkipDir
			}
			return nil
		}
		files++
		if info, err := d.Info(); err == nil && info.Mode().IsRegular() {
			bytes += info.Size()
		}
		if files > maxShadowFiles || bytes > maxShadowBytes {
			return ErrShadowTooLarge
		}
		return nil
	})
	if errors.Is(err, ErrShadowTooLarge) {
		return fmt.Errorf("%s: %w (over %d files or %d MiB)", dir, ErrShadowTooLarge, maxShadowFiles, maxShadowBytes>>20)
	}
	return err
}
