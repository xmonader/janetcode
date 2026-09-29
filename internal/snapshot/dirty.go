package snapshot

import (
	"context"
	"path/filepath"
	"strings"

	"github.com/xmonader/janetcode/internal/gitx"
)

// Dirty lists the work tree's files that differ from the index or are
// untracked (ignored ones left out), as absolute paths. It is git status
// against the real index, so it costs a stat per tracked file, not a
// hash: cheap enough to run around a shell command.
func (r *Repo) Dirty(ctx context.Context) ([]string, error) {
	out, err := gitx.Run(ctx, r.Root, r.env, "status", "--porcelain=v1", "-z", "--untracked-files=all", "--no-renames", "--ignore-submodules")
	if err != nil {
		return nil, err
	}
	var paths []string
	for _, e := range strings.Split(out, "\x00") {
		if len(e) < 4 {
			continue
		}
		paths = append(paths, filepath.Join(r.Root, filepath.FromSlash(e[3:])))
	}
	return paths, nil
}

// IndexContent is path's content as the index has it; ok is false when
// the index has no such file. For a file Dirty did not list, that is the
// file's content on disk.
func (r *Repo) IndexContent(ctx context.Context, path string) (data []byte, ok bool, err error) {
	rel, inside := r.rel(path)
	if !inside {
		return nil, false, nil
	}
	b, err := gitx.RunBytes(ctx, r.Root, r.env, "cat-file", "blob", ":"+rel)
	if err != nil {
		if gitx.ExitCode(err) == 128 {
			return nil, false, nil
		}
		return nil, false, err
	}
	return b, true, nil
}
