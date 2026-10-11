package main

import (
	"os"
	"path/filepath"

	"github.com/xmonader/janetcode/internal/config"
	"github.com/xmonader/janetcode/internal/xdg"
)

// fingerprintDir is where the behavioural fingerprints are kept when
// fingerprint is on, "" (off) otherwise or when there is no data
// directory to keep them in.
func fingerprintDir(cfg *config.Config) string {
	if cfg == nil || !cfg.Fingerprint {
		return ""
	}
	dir, err := xdg.DataDir(os.Getenv)
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "fingerprints")
}
