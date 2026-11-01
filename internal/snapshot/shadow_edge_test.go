package snapshot

import (
	"context"
	"path/filepath"
	"testing"
)

func TestOpenShadowRejectsMissingDirectory(t *testing.T) {
	root := t.TempDir()
	missing := filepath.Join(root, "missing")
	if _, err := OpenShadow(context.Background(), missing, filepath.Join(root, "store")); err == nil {
		t.Fatal("OpenShadow accepted a working directory that does not exist")
	}
}
