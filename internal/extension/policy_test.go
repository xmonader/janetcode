package extension

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGrantStaging(t *testing.T) {
	g := Restricted("installed")
	if !g.Has(Connect) || !g.Has(Read) || g.Has(Mutate) {
		t.Fatalf("restricted = %v", g.Scopes)
	}
	g = g.With(Mutate)
	if len(g.Scopes) != 3 {
		t.Fatalf("with mutate = %v", g.Scopes)
	}
	// Taking read away takes mutate with it.
	if g2 := g.Without(Read); g2.Has(Read) || g2.Has(Mutate) || !g2.Has(Connect) {
		t.Fatalf("without read = %v", g2.Scopes)
	}
	// Taking connect away takes everything.
	if g3 := g.Without(Connect); len(g3.Scopes) != 0 {
		t.Fatalf("without connect = %v", g3.Scopes)
	}
	// Mutate alone pulls in connect and read.
	if g4 := (Grant{}).With(Mutate); !g4.Has(Connect) || !g4.Has(Read) {
		t.Fatalf("mutate alone = %v", g4.Scopes)
	}
}

func TestParseScope(t *testing.T) {
	if s, err := ParseScope(" Mutate "); err != nil || s != Mutate {
		t.Fatalf("ParseScope = %v, %v", s, err)
	}
	if _, err := ParseScope("shell"); err == nil {
		t.Fatal("unknown scope accepted")
	}
}

func TestStoreRoundTripAndFailClosed(t *testing.T) {
	dir := t.TempDir()
	st := Store{Path: filepath.Join(dir, "sub", FileName)}
	if _, ok, err := st.Get("x"); ok || err != nil {
		t.Fatalf("empty store: %v %v", ok, err)
	}
	g := Restricted("because").With(Mutate)
	g.DisabledTools = []string{"b", "a"}
	if err := st.Set(UserKey("srv"), g); err != nil {
		t.Fatal(err)
	}
	got, ok, err := st.Get(UserKey("srv"))
	if err != nil || !ok || !got.Has(Mutate) || got.DisabledTools[0] != "a" {
		t.Fatalf("got %+v %v %v", got, ok, err)
	}
	if info, _ := os.Stat(st.Path); info.Mode().Perm() != 0o600 {
		t.Fatalf("store mode %v", info.Mode().Perm())
	}
	if err := st.Delete(UserKey("srv")); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := st.Get(UserKey("srv")); ok {
		t.Fatal("grant survived Delete")
	}

	// A corrupt store is an error, never "nothing recorded".
	if err := os.WriteFile(st.Path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.Get("x"); err == nil {
		t.Fatal("corrupt store read as empty")
	}
	p := Policy{Store: st, Cwd: "/p"}
	if err := p.CheckConnect("srv"); err == nil {
		t.Fatal("connect allowed with an unreadable store")
	}
	if err := p.CheckCall("srv", "t", false); err == nil {
		t.Fatal("call allowed with an unreadable store")
	}
}

func TestPolicyChecks(t *testing.T) {
	st := Store{Path: filepath.Join(t.TempDir(), FileName)}
	p := Policy{Store: st, Cwd: "/proj"}

	// No record: as the gate that started it left it.
	if err := p.CheckConnect("old"); err != nil {
		t.Fatalf("unrecorded server refused: %v", err)
	}
	if err := p.CheckCall("old", "write", true); err != nil {
		t.Fatalf("unrecorded server's mutating tool refused: %v", err)
	}

	// Newly installed: restricted.
	if err := st.Set(ProjectKey("/proj", "new"), Restricted("installed")); err != nil {
		t.Fatal(err)
	}
	if err := p.CheckConnect("new"); err != nil {
		t.Fatal(err)
	}
	if err := p.CheckCall("new", "read", false); err != nil {
		t.Fatalf("read-only call refused: %v", err)
	}
	err := p.CheckCall("new", "write", true)
	if err == nil || !strings.Contains(err.Error(), "/mcp enable new mutate") {
		t.Fatalf("mutating call on restricted server: %v", err)
	}

	// Per-tool switch.
	g := Full("approved")
	g.DisabledTools = []string{"danger"}
	if err := st.Set(ProjectKey("/proj", "new"), g); err != nil {
		t.Fatal(err)
	}
	if err := p.CheckCall("new", "write", true); err != nil {
		t.Fatal(err)
	}
	if err := p.CheckCall("new", "danger", true); err == nil {
		t.Fatal("switched-off tool callable")
	}

	// Disabled: nothing runs, nothing is called.
	if err := st.Set(ProjectKey("/proj", "new"), g.Without(Connect)); err != nil {
		t.Fatal(err)
	}
	if err := p.CheckConnect("new"); err == nil {
		t.Fatal("disabled server may connect")
	}
	if err := p.CheckCall("new", "read", false); err == nil {
		t.Fatal("disabled server callable")
	}

	// Another project's grant does not apply here.
	other := Policy{Store: st, Cwd: "/elsewhere"}
	if err := other.CheckConnect("new"); err != nil {
		t.Fatalf("grant leaked across projects: %v", err)
	}

	// The project's grant shadows the user's of the same name.
	if err := st.Set(UserKey("both"), Full("user")); err != nil {
		t.Fatal(err)
	}
	if err := st.Set(ProjectKey("/proj", "both"), Restricted("project")); err != nil {
		t.Fatal(err)
	}
	if err := p.CheckCall("both", "w", true); err == nil {
		t.Fatal("user grant overrode the project's restriction")
	}
}
