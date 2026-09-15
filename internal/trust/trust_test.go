package trust

import (
	"path/filepath"
	"testing"
)

func TestStoreMatchesKeyAndHashExactly(t *testing.T) {
	ts := Store{Path: filepath.Join(t.TempDir(), "sub", "trust.json")}
	cwd := "/work/project"
	if ts.Trusted(cwd, "h1") {
		t.Fatal("untrusted by default")
	}
	if err := ts.Trust(cwd, "h1"); err != nil {
		t.Fatal(err)
	}
	if err := ts.Trust(HooksKey(cwd), "h2"); err != nil {
		t.Fatal(err)
	}
	if !ts.Trusted(cwd, "h1") || ts.Trusted(cwd, "h2") || !ts.Trusted(HooksKey(cwd), "h2") {
		t.Fatal("trust must match key and hash exactly")
	}
	if ts.Trusted(VerifyKey(cwd), "h1") || ts.Trusted(PermissionsKey(cwd), "h2") || ts.Trusted(cwd, "") {
		t.Fatal("keys must stay apart and an empty hash is never trusted")
	}
	if (Store{}).Trusted(cwd, "h1") || (Store{}).Trust(cwd, "h1") == nil {
		t.Fatal("a store without a path trusts nothing and records nothing")
	}
}

func TestKeys(t *testing.T) {
	if HooksKey("/d") != "/d#hooks" || VerifyKey("/d") != "/d#verify" || PermissionsKey("/d") != "/d#permissions" {
		t.Fatal("key format changed; existing trust files would no longer match")
	}
}
