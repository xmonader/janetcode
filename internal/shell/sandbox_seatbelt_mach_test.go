package shell

import (
	"strings"
	"testing"
)

// TestSeatbeltMachLookupIsAnAllowlist pins the mach-lookup rule. An
// unfiltered "(allow mach-lookup)" reached every service on the machine,
// some of which act outside the sandbox. It cannot be run here (no
// macOS), so the policy text is the thing tested.
func TestSeatbeltMachLookupIsAnAllowlist(t *testing.T) {
	t.Setenv("JANETCODE_SANDBOX_MACH", "")
	for _, p := range Profiles {
		policy := SeatbeltPolicy(p, "/proj", nil)
		if strings.Contains(policy, "(allow mach-lookup)") {
			t.Fatalf("%s: mach-lookup is unfiltered:\n%s", p.Name, policy)
		}
		for _, n := range []string{"com.apple.system.opendirectoryd.libinfo", "com.apple.trustd.agent", "com.apple.bsd.dirhelper"} {
			if !strings.Contains(policy, `(global-name "`+n+`")`) {
				t.Errorf("%s: %s is missing; ordinary tools need it", p.Name, n)
			}
		}
		dns := strings.Contains(policy, `(global-name "com.apple.mDNSResponder")`)
		if dns != p.Network {
			t.Errorf("%s: name resolution service allowed = %v, network = %v", p.Name, dns, p.Network)
		}
		for _, n := range []string{"com.apple.pasteboard.1", "com.apple.coreservices.appleevents", "com.apple.xpc.launchd.domain.system"} {
			if strings.Contains(policy, n) {
				t.Errorf("%s: %s is reachable", p.Name, n)
			}
		}
	}
	t.Setenv("JANETCODE_SANDBOX_MACH", "com.example.tool, ")
	policy := SeatbeltPolicy(Profiles[0], "/proj", nil)
	if !strings.Contains(policy, `(global-name "com.example.tool")`) {
		t.Errorf("JANETCODE_SANDBOX_MACH was not added:\n%s", policy)
	}
	if strings.Count(policy, "(global-name \"\")") != 0 {
		t.Error("an empty name was added")
	}
}
