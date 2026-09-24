// Package backup writes janetcode's per-user state — the config directory
// (config, permission rules, MCP trust and logins) and the data directory
// (sessions, memory, archives) — to one tar.gz, and puts it back. It is
// what moving to a new machine, or recovering from a bad edit, needs.
//
// The archive holds two trees, config/ and data/. Restoring overwrites
// the files the archive holds and leaves the rest; before it does, the
// state it is about to change is itself backed up, so a restore can be
// undone.
package backup

import (
	"archive/tar"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"
)

// Roots names the directories an archive holds, by the name each has in
// the archive: "config" and "data".
type Roots map[string]string

// MaxRestoreBytes bounds what a restore will write, so a hostile or
// corrupt archive cannot fill the disk.
const MaxRestoreBytes = 8 << 30

// Summary is what Create or Restore did.
type Summary struct {
	Files int
	Bytes int64
	// Before is where Restore saved the state it replaced, or "".
	Before string
}

// Create writes every regular file under roots to a tar.gz at dest. The
// file is written beside dest and renamed into place, readable only by
// its owner: it holds API keys. A root that does not exist is skipped.
func Create(dest string, roots Roots) (Summary, error) {
	var sum Summary
	tmp, err := os.CreateTemp(filepath.Dir(dest), ".janetcode-backup-*")
	if err != nil {
		return sum, err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return sum, err
	}
	// A backup written inside one of the roots must not archive itself.
	sum, werr := write(tmp, roots, tmp.Name())
	if cerr := tmp.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		return sum, werr
	}
	return sum, os.Rename(tmp.Name(), dest)
}

func write(w io.Writer, roots Roots, skip string) (Summary, error) {
	var sum Summary
	gz := gzip.NewWriter(w)
	tw := tar.NewWriter(gz)
	for _, name := range []string{"config", "data"} {
		root, ok := roots[name]
		if !ok || root == "" {
			continue
		}
		err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				if p == root && errors.Is(err, fs.ErrNotExist) {
					return fs.SkipAll
				}
				return err
			}
			// Symlinks and sockets are not state janetcode wrote; a link
			// could also point anywhere on the machine.
			if !d.Type().IsRegular() || p == skip {
				return nil
			}
			rel, err := filepath.Rel(root, p)
			if err != nil {
				return err
			}
			n, err := addFile(tw, p, name+"/"+filepath.ToSlash(rel))
			if err != nil {
				return fmt.Errorf("%s: %w", p, err)
			}
			sum.Files++
			sum.Bytes += n
			return nil
		})
		if err != nil {
			return sum, err
		}
	}
	if err := tw.Close(); err != nil {
		return sum, err
	}
	return sum, gz.Close()
}

// addFile copies one file into the archive. A session being appended to
// while this runs is copied up to the size it had when it was opened, so
// the archive never holds a length its header does not match.
func addFile(tw *tar.Writer, src, name string) (int64, error) {
	f, err := os.Open(src)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return 0, err
	}
	hdr := &tar.Header{Name: name, Mode: int64(fi.Mode().Perm()), Size: fi.Size(), ModTime: fi.ModTime(), Typeflag: tar.TypeReg}
	if err := tw.WriteHeader(hdr); err != nil {
		return 0, err
	}
	n, err := io.CopyN(tw, f, fi.Size())
	if err != nil {
		return n, fmt.Errorf("copying: %w", err)
	}
	return n, nil
}

// Restore puts the files in the archive at src back under roots. The
// current copies of the files it will overwrite go to a backup of their
// own first, named in the summary. Entries that are not regular files, or
// whose path would leave their root, are refused before anything is
// written.
func Restore(src string, roots Roots) (Summary, error) {
	var sum Summary
	entries, err := list(src, roots)
	if err != nil {
		return sum, err
	}
	if len(entries) == 0 {
		return sum, fmt.Errorf("%s holds no janetcode files", src)
	}
	// What is about to be replaced, saved first.
	var existing []string
	for _, e := range entries {
		if _, err := os.Stat(e.dest); err == nil {
			existing = append(existing, e.dest)
		}
	}
	if len(existing) > 0 {
		before := freeName(filepath.Join(filepath.Dir(src), "janetcode-before-restore-"+time.Now().Format("20060102-150405")))
		if err := saveFiles(before, existing, roots); err != nil {
			return sum, fmt.Errorf("saving the current files before restoring: %w", err)
		}
		sum.Before = before
	}
	f, err := os.Open(src)
	if err != nil {
		return sum, err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return sum, err
	}
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return sum, err
		}
		// Checked again: the archive is read a second time, and it is not
		// ours to trust that it did not change in between.
		dest, err := target(hdr.Name, roots)
		if err != nil || hdr.Typeflag != tar.TypeReg || hdr.Size < 0 || hdr.Size > MaxRestoreBytes {
			return sum, fmt.Errorf("%s changed while it was being restored; %d files were written", src, sum.Files)
		}
		if err := os.MkdirAll(filepath.Dir(dest), 0o700); err != nil {
			return sum, err
		}
		n, err := writeFile(dest, tr, hdr.Size, os.FileMode(hdr.Mode).Perm()&^0o022)
		if err != nil {
			return sum, fmt.Errorf("%s: %w", dest, err)
		}
		sum.Files++
		sum.Bytes += n
	}
	return sum, nil
}

type entry struct {
	name, dest string
}

// list reads the whole archive once and checks every entry before
// Restore writes anything, so a bad entry at the end does not leave a
// half-restored state behind.
func list(src string, roots Roots) ([]entry, error) {
	f, err := os.Open(src)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return nil, fmt.Errorf("%s is not a janetcode backup: %w", src, err)
	}
	tr := tar.NewReader(gz)
	var out []entry
	var total int64
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return out, nil
		}
		if err != nil {
			return nil, fmt.Errorf("%s: %w", src, err)
		}
		if hdr.Typeflag != tar.TypeReg {
			return nil, fmt.Errorf("%s: %s is not a regular file; refusing the archive", src, hdr.Name)
		}
		dest, err := target(hdr.Name, roots)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", src, err)
		}
		if total += hdr.Size; hdr.Size < 0 || total > MaxRestoreBytes {
			return nil, fmt.Errorf("%s: more than %d GiB; refusing the archive", src, MaxRestoreBytes>>30)
		}
		out = append(out, entry{hdr.Name, dest})
	}
}

// target maps an archive name to the file it restores, or says why it
// may not be restored.
func target(name string, roots Roots) (string, error) {
	clean := path.Clean(name)
	top, rest, ok := strings.Cut(clean, "/")
	root := roots[top]
	refuse := fmt.Errorf("%q is outside config/ and data/; refusing the archive", name)
	// A backslash is a separator on Windows, where "config/..\\x" would
	// climb out of the root.
	if !ok || root == "" || rest == "" || path.IsAbs(clean) || rest == ".." || strings.HasPrefix(rest, "../") || strings.ContainsAny(rest, `\:`) {
		return "", refuse
	}
	dest := filepath.Join(root, filepath.FromSlash(rest))
	if rel, err := filepath.Rel(root, dest); err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", refuse
	}
	return dest, nil
}

// writeFile writes exactly size bytes from r to dest, beside it first and
// then renamed, so an interrupted restore leaves the old file whole.
func writeFile(dest string, r io.Reader, size int64, mode os.FileMode) (int64, error) {
	tmp, err := os.CreateTemp(filepath.Dir(dest), ".restore-*")
	if err != nil {
		return 0, err
	}
	defer os.Remove(tmp.Name())
	n, err := io.CopyN(tmp, r, size)
	if err == nil {
		err = tmp.Chmod(mode)
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return n, err
	}
	return n, os.Rename(tmp.Name(), dest)
}

// saveFiles backs up just the named files, under the archive names they
// would have, so the result can be restored like any other backup.
func saveFiles(dest string, files []string, roots Roots) error {
	tmp, err := os.CreateTemp(filepath.Dir(dest), ".janetcode-backup-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	gz := gzip.NewWriter(tmp)
	tw := tar.NewWriter(gz)
	werr := func() error {
		for _, p := range files {
			name := ""
			for top, root := range roots {
				if rel, err := filepath.Rel(root, p); err == nil && !strings.HasPrefix(rel, "..") {
					name = top + "/" + filepath.ToSlash(rel)
				}
			}
			if name == "" {
				continue
			}
			if _, err := addFile(tw, p, name); err != nil {
				return err
			}
		}
		if err := tw.Close(); err != nil {
			return err
		}
		return gz.Close()
	}()
	if err := tmp.Chmod(0o600); werr == nil {
		werr = err
	}
	if cerr := tmp.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		return werr
	}
	return os.Rename(tmp.Name(), dest)
}

// freeName is base.tar.gz, or base-2.tar.gz and so on when that is taken:
// two restores in one second must not write over each other's safety
// copy, least of all over the archive being restored.
func freeName(base string) string {
	name := base + ".tar.gz"
	for i := 2; ; i++ {
		if _, err := os.Lstat(name); errors.Is(err, fs.ErrNotExist) {
			return name
		}
		name = fmt.Sprintf("%s-%d.tar.gz", base, i)
	}
}
