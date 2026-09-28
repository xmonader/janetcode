package main

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"

	"github.com/xmonader/janetcode/internal/backup"
	"github.com/xmonader/janetcode/internal/headless"
	"github.com/xmonader/janetcode/internal/textfmt"
	"github.com/xmonader/janetcode/internal/xdg"
)

// backupRoots are this user's config and data directories.
func backupRoots() (backup.Roots, error) {
	cfg, err := xdg.ConfigDir(os.Getenv)
	if err != nil {
		return nil, err
	}
	data, err := xdg.DataDir(os.Getenv)
	if err != nil {
		return nil, err
	}
	return backup.Roots{"config": cfg, "data": data}, nil
}

func backupMode(w io.Writer, dest string) error {
	roots, err := backupRoots()
	if err != nil {
		return err
	}
	sum, err := backup.Create(dest, roots)
	if err != nil {
		return fmt.Errorf("backup: %w", err)
	}
	if sum.Files == 0 {
		// Create wrote nothing to dest; whatever is there was the user's.
		return headless.BadInput("nothing to back up: %s and %s are empty", roots["config"], roots["data"])
	}
	fmt.Fprintf(w, "wrote %s: %s, %s (it holds your API keys; keep it private)\n", dest, textfmt.Plural(sum.Files, "file"), textfmt.Bytes(sum.Bytes))
	return nil
}

func restoreMode(w io.Writer, src string) error {
	if _, err := os.Stat(src); errors.Is(err, fs.ErrNotExist) {
		return headless.BadInput("%s does not exist", src)
	}
	roots, err := backupRoots()
	if err != nil {
		return err
	}
	sum, err := backup.Restore(src, roots)
	if err != nil {
		return headless.BadInput("restore: %v", err)
	}
	fmt.Fprintf(w, "restored %s (%s) into %s and %s\n", textfmt.Plural(sum.Files, "file"), textfmt.Bytes(sum.Bytes), roots["config"], roots["data"])
	if sum.Before != "" {
		fmt.Fprintf(w, "the files it replaced are in %s; janetcode --restore %s puts them back\n", sum.Before, sum.Before)
	}
	return nil
}
