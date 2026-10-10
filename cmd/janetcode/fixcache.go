package main

import (
	"os"
	"path/filepath"

	"github.com/xmonader/janetcode/internal/xdg"
)

// fixCacheDir is where the fix cache keeps one file per repository, or ""
// when there is no data directory, which leaves the cache off.
func fixCacheDir() string {
	data, err := xdg.DataDir(os.Getenv)
	if err != nil {
		return ""
	}
	return filepath.Join(data, "fixcache")
}
