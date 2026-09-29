package mcp

import (
	"path/filepath"
	"testing"
)

// roots/list advertises the project as a real URI: a space in the path is
// escaped rather than sent raw.
func TestFileURIEscapesThePath(t *testing.T) {
	if got, want := fileURI(filepath.FromSlash("/home/me/my project")), "file:///home/me/my%20project"; got != want {
		t.Fatalf("fileURI = %q, want %q", got, want)
	}
	if got, want := fileURI(filepath.FromSlash("/srv/a#b")), "file:///srv/a%23b"; got != want {
		t.Fatalf("fileURI = %q, want %q", got, want)
	}
}
