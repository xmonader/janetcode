package shell

import "runtime"

// BackendCapability is whether one sandbox backend can run on this
// machine, and when it cannot, why not.
type BackendCapability struct {
	Name      string `json:"name"`
	Available bool   `json:"available"`
	// Why is the reason an unavailable backend cannot run; empty when
	// it is available.
	Why string `json:"why,omitempty"`
}

// Capabilities reports, for every backend /sandbox can force, whether
// this machine can run it, and which one "auto" would pick. It starts
// nothing but the probes the sandbox already does to decide (looking
// on PATH, the Landlock ABI, whether bwrap can create a namespace).
//
// It is what lets a report tell "this machine cannot sandbox" apart
// from "the sandbox is off because nobody asked for it": the first is a
// fact about the machine, the second a choice.
func (s *Sandbox) Capabilities() (backends []BackendCapability, auto string, autoWhy string) {
	for _, name := range Backends {
		if name == BackendAuto {
			continue
		}
		probe := s.cloneFor(name)
		_, err := probe.backendFor()
		c := BackendCapability{Name: name, Available: err == nil}
		if err != nil {
			c.Why = err.Error()
		}
		backends = append(backends, c)
	}
	picked, err := s.cloneFor(BackendAuto).backendFor()
	if err != nil {
		return backends, "", err.Error()
	}
	return backends, picked, ""
}

// cloneFor is a sandbox with the same probes (and so the same test
// doubles) that is asked for one backend, so probing does not disturb
// the session's own choice.
func (s *Sandbox) cloneFor(backend string) *Sandbox {
	c := &Sandbox{}
	if s != nil {
		s.mu.Lock()
		c.look, c.abi, c.probe, c.profile, c.proxy = s.look, s.abi, s.probe, s.profile, s.proxy
		s.mu.Unlock()
	}
	if backend != BackendAuto {
		c.backend = backend
	}
	return c
}

// HostOS is the operating system the capabilities were probed on.
func HostOS() string { return runtime.GOOS + "/" + runtime.GOARCH }

// ProbeSandbox is a sandbox whose probes are replaced: look stands in
// for finding a program on PATH and abi for the kernel's Landlock ABI.
// Reports and their tests use it to ask "what would this machine say"
// without being that machine.
func ProbeSandbox(look func(string) (string, error), abi func() int) *Sandbox {
	return &Sandbox{look: look, abi: abi}
}
