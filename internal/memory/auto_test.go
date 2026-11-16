package memory

import (
	"strings"
	"testing"
)

func TestAutoSessionAndProjectSwitches(t *testing.T) {
	root := t.TempDir()
	s := &Store{Root: root, Key: "proj"}
	if !s.AutoAllowed() {
		t.Fatal("automatic memory starts on")
	}

	out, handled, err := s.ConfirmCommand("auto off")
	if err != nil || !handled || !strings.Contains(out, "off for this session") {
		t.Fatalf("auto off = %q %v %v", out, handled, err)
	}
	if s.AutoAllowed() {
		t.Fatal("session switch did not stop automatic memory")
	}
	// A session switch is not a project switch: a new session starts on.
	if fresh := (&Store{Root: root, Key: "proj"}); !fresh.AutoAllowed() {
		t.Fatal("a session switch leaked into the next session")
	}
	if _, _, err := s.ConfirmCommand("auto on"); err != nil || !s.AutoAllowed() {
		t.Fatalf("auto on: allowed=%v err=%v", s.AutoAllowed(), err)
	}

	if out, _, err := s.ConfirmCommand("auto off project"); err != nil || !strings.Contains(out, "off for this project") {
		t.Fatalf("auto off project = %q %v", out, err)
	}
	if s.AutoAllowed() {
		t.Fatal("project switch did not stop automatic memory")
	}
	// The project switch outlives the session and does not touch another project.
	if fresh := (&Store{Root: root, Key: "proj"}); fresh.AutoAllowed() {
		t.Fatal("project switch did not persist to a new session")
	}
	if other := (&Store{Root: root, Key: "other"}); !other.AutoAllowed() {
		t.Fatal("project switch leaked into another project")
	}
	// "auto on" for the session cannot override the project's switch.
	s.ConfirmCommand("auto on")
	if s.AutoAllowed() {
		t.Fatal("session auto on overrode the project switch")
	}
	if _, _, err := s.ConfirmCommand("auto on project"); err != nil || !s.AutoAllowed() {
		t.Fatalf("auto on project: allowed=%v err=%v", s.AutoAllowed(), err)
	}
	// Turning it on again when it is already on is not an error.
	if _, _, err := s.ConfirmCommand("auto on project"); err != nil {
		t.Fatalf("idempotent on: %v", err)
	}
}

func TestAutoCommandRejectsJunk(t *testing.T) {
	s := &Store{Root: t.TempDir(), Key: "proj"}
	for _, arg := range []string{"auto maybe", "auto off everywhere", "auto on project now"} {
		if _, handled, err := s.ConfirmCommand(arg); !handled || err == nil {
			t.Errorf("%q: handled=%v err=%v, want a usage error", arg, handled, err)
		}
	}
	if !s.AutoAllowed() {
		t.Fatal("a rejected command changed the setting")
	}
}
