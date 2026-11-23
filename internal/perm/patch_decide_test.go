package perm

import (
	"encoding/json"
	"testing"
)

func patchCall(t *testing.T, body string) Call {
	t.Helper()
	in, _ := json.Marshal(map[string]string{"input": body})
	return Call{Tool: "apply_patch", Subject: Subject("apply_patch", in), Input: in, Root: "/w"}
}

// A deny rule on the second file of a patch must refuse the whole patch,
// though the call's Subject is only the first file.
func TestApplyPatchDenyOnLaterFile(t *testing.T) {
	rs := Rules{
		Allow: []Rule{{Tool: "apply_patch"}},
		Deny:  []Rule{{Tool: "apply_patch", Pattern: "secret/*"}},
	}
	c := patchCall(t, "*** Begin Patch\n*** Update File: ok.go\n@@\n-a\n+b\n*** Add File: secret/key\n+x\n*** End Patch")
	if v, _, _ := rs.DecideCall(c); v != Denied {
		t.Fatalf("verdict %v, want Denied", v)
	}
}

// An allow rule must cover every file: one file outside it asks.
func TestApplyPatchAllowMustCoverAll(t *testing.T) {
	rs := Rules{Allow: []Rule{{Tool: "apply_patch", Pattern: "src/*"}}}
	both := patchCall(t, "*** Begin Patch\n*** Update File: src/a.go\n@@\n-a\n+b\n*** Update File: src/b.go\n@@\n-a\n+b\n*** End Patch")
	if v, _, _ := rs.DecideCall(both); v != Allowed {
		t.Fatalf("both in src: verdict %v, want Allowed", v)
	}
	mixed := patchCall(t, "*** Begin Patch\n*** Update File: src/a.go\n@@\n-a\n+b\n*** Update File: other/b.go\n@@\n-a\n+b\n*** End Patch")
	if v, _, _ := rs.DecideCall(mixed); v != Ask {
		t.Fatalf("one outside src: verdict %v, want Ask", v)
	}
}

// A move target is a write target too.
func TestApplyPatchMoveTargetJudged(t *testing.T) {
	rs := Rules{Allow: []Rule{{Tool: "apply_patch"}}, Deny: []Rule{{Tool: "apply_patch", Pattern: "secret/*"}}}
	c := patchCall(t, "*** Begin Patch\n*** Update File: a.go\n*** Move to: secret/a.go\n@@\n-a\n+b\n*** End Patch")
	if v, _, _ := rs.DecideCall(c); v != Denied {
		t.Fatalf("verdict %v, want Denied", v)
	}
}
