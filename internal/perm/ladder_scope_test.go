package perm

import "testing"

// "Always allow" for a network command offers the host, never every host.
func TestNetworkLadderIsHostBound(t *testing.T) {
	cmd := "curl -s https://api.github.com/repos/x/y"
	got := Suggest("bash", cmd)
	if got.String() != "bash(cmd:curl *, host:api.github.com)" {
		t.Fatalf("Suggest = %s", got)
	}
	rs := Rules{}
	rs.Add(Allowed, got)
	if v := verdictOf(rs, bashCall("/w", "curl https://api.github.com/other")); v != Allowed {
		t.Errorf("same host = %v", v)
	}
	if v := verdictOf(rs, bashCall("/w", "curl https://evil.example/x")); v == Allowed {
		t.Errorf("another host allowed by a saved host rule")
	}
	for _, r := range SuggestLadder("bash", cmd) {
		if r.String() == "bash(curl *)" {
			t.Errorf("ladder offers %s, which covers every host", r)
		}
	}
	if len(SuggestLadder("bash", "curl https://a.example https://b.example")) != 1 {
		t.Errorf("two hosts: want only the exact command")
	}
}
