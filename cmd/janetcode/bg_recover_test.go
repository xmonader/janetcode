package main

import (
	"strings"
	"testing"
)

// --bg --recover starts a child that carries on the dead turn: no -p, the
// --recover flag, and the permission flags the user gave.
func TestBackgroundRecoverChildArgs(t *testing.T) {
	got := strings.Join(backgroundChildArgs("sess-1", "", nil, []string{"--yolo"}), " ")
	if got != "--resume sess-1 --recover --yolo" {
		t.Fatalf("child args = %q", got)
	}
	if got := strings.Join(backgroundChildArgs("sess-1", "do it", nil, nil), " "); strings.Contains(got, "--recover") {
		t.Fatalf("a prompted run recovers: %q", got)
	}
}
