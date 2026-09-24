package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/headless"
)

// --backup then --restore brings a lost config back, says where the
// replaced files went, and a bad archive is a usage error (exit 42).
func TestBackupAndRestoreFlags(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
	cfg := filepath.Join(home, ".config", "janetcode", "config.json")
	os.MkdirAll(filepath.Dir(cfg), 0o700)
	os.WriteFile(cfg, []byte(`{"model":"kept"}`), 0o600)

	archive := filepath.Join(t.TempDir(), "jc.tar.gz")
	var out bytes.Buffer
	if err := backupMode(&out, archive); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "1 file") {
		t.Errorf("backup said %q", out.String())
	}
	os.WriteFile(cfg, []byte(`{"model":"broken"`), 0o600)
	out.Reset()
	if err := restoreMode(&out, archive); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(cfg); string(b) != `{"model":"kept"}` {
		t.Errorf("config after restore = %s", b)
	}
	if !strings.Contains(out.String(), "janetcode --restore ") {
		t.Errorf("restore did not say how to undo it: %q", out.String())
	}

	junk := filepath.Join(t.TempDir(), "junk.tar.gz")
	os.WriteFile(junk, []byte("not an archive"), 0o600)
	for _, p := range []string{junk, filepath.Join(t.TempDir(), "missing.tar.gz")} {
		if err := restoreMode(&out, p); !errors.Is(err, headless.ErrBadInput) {
			t.Errorf("restore %s: err = %v, want a usage error", filepath.Base(p), err)
		}
	}
}
