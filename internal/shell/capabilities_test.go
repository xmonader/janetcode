package shell

import (
	"errors"
	"runtime"
	"testing"
)

func capByName(cs []BackendCapability, name string) BackendCapability {
	for _, c := range cs {
		if c.Name == name {
			return c
		}
	}
	return BackendCapability{}
}

// A machine with nothing to sandbox with says so, backend by backend and
// for "auto", rather than saying the sandbox is off.
func TestCapabilitiesOfAMachineWithNoBackend(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("the probes under test are the Linux ones")
	}
	s := &Sandbox{look: func(string) (string, error) { return "", errors.New("not found") }, abi: func() int { return 0 }}
	backends, auto, why := s.Capabilities()
	if auto != "" || why == "" {
		t.Fatalf("auto = %q why = %q", auto, why)
	}
	for _, name := range []string{BackendBwrap, BackendDocker, BackendSeatbelt, BackendLandlock} {
		if c := capByName(backends, name); c.Name == "" || c.Available || c.Why == "" {
			t.Errorf("%s = %+v; want unavailable with a reason", name, c)
		}
	}
	if len(backends) != len(Backends)-1 {
		t.Fatalf("%d backends reported, want every forceable one: %+v", len(backends), backends)
	}
}

func TestCapabilitiesPickDockerThenLandlock(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("the probes under test are the Linux ones")
	}
	onlyDocker := func(name string) (string, error) {
		if name == "docker" {
			return "/usr/bin/docker", nil
		}
		return "", errors.New("not found")
	}
	s := &Sandbox{look: onlyDocker, abi: func() int { return 0 }}
	backends, auto, _ := s.Capabilities()
	if auto != BackendDocker || !capByName(backends, BackendDocker).Available || capByName(backends, BackendLandlock).Available {
		t.Fatalf("auto = %q backends = %+v", auto, backends)
	}

	s = &Sandbox{look: func(string) (string, error) { return "", errors.New("not found") }, abi: func() int { return 4 }}
	backends, auto, _ = s.Capabilities()
	if auto != BackendLandlock || !capByName(backends, BackendLandlock).Available {
		t.Fatalf("auto = %q backends = %+v", auto, backends)
	}
}

// Probing does not change what the session's own sandbox is set to.
func TestCapabilitiesLeavesTheSandboxAlone(t *testing.T) {
	s := &Sandbox{look: func(string) (string, error) { return "/x", nil }, abi: func() int { return 4 }}
	if err := s.SetBackend(BackendLandlock); err != nil && runtime.GOOS == "linux" {
		t.Fatal(err)
	}
	before := s.backend
	s.Capabilities()
	if s.backend != before || s.Enabled() {
		t.Fatalf("backend %q -> %q, enabled %v", before, s.backend, s.Enabled())
	}
}
