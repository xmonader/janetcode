package shell

import (
	"runtime"
	"strings"
	"testing"
)

// A sandbox a managed policy forced cannot be turned off or moved to
// another profile.
func TestForcedSandboxCannotBeTurnedOff(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sandbox is not supported on windows")
	}
	s := &Sandbox{look: func(name string) (string, error) { return "/usr/bin/" + name, nil }, backend: BackendDocker}
	if err := s.Force("strict", "the managed policy"); err != nil {
		t.Fatal(err)
	}
	if err := s.Enable(); err != nil {
		t.Fatal(err)
	}
	if _, err := SetSandbox(s, "off"); err == nil || !strings.Contains(err.Error(), "managed policy") {
		t.Fatalf("off: err = %v, want refused", err)
	}
	s.Disable()
	if !s.Enabled() {
		t.Fatal("Disable turned a forced sandbox off")
	}
	for _, arg := range []string{"on", "read-only", "workspace"} {
		if _, err := SetSandbox(s, arg); err == nil {
			t.Errorf("%s: moving a forced profile was allowed", arg)
		}
	}
	if s.ProfileName() != "strict" || s.Forced() != "strict" {
		t.Errorf("profile = %s forced = %s", s.ProfileName(), s.Forced())
	}
	if _, err := SetSandbox(s, "strict"); err != nil {
		t.Errorf("re-selecting the forced profile: %v", err)
	}
}
