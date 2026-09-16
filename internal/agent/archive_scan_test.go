package agent

import (
	"fmt"
	"strings"
	"testing"
)

// Storing an archived output used to re-read the header of every file in
// the archive directory, to decide whether the cap had been reached. A
// long session spills hundreds of outputs, so that made archiving
// quadratic in what it had already archived — on a directory shared by
// every session of the project.
func TestArchivingDoesNotReReadTheArchive(t *testing.T) {
	ar := NewArchive(t.TempDir())
	body := strings.Repeat("x", 4096)
	for i := 0; i < 50; i++ {
		if _, err := ar.Put(fmt.Sprintf("use-%d", i), "bash", body); err != nil {
			t.Fatal(err)
		}
	}
	ar.mu.Lock()
	scans, bytes := ar.scans, ar.bytes
	ar.mu.Unlock()
	if scans != 1 {
		t.Fatalf("stored 50 outputs and walked the directory %d times, want 1 (the initial load)", scans)
	}
	if want := int64(50 * len(body)); bytes != want {
		t.Fatalf("archive believes it holds %d bytes, want %d", bytes, want)
	}
}
