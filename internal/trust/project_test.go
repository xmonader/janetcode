package trust

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func project(t *testing.T) string {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, ".janetcode/skills/deploy/SKILL.md"), "---\nname: deploy\n---\nship it")
	writeFile(t, filepath.Join(dir, ".janetcode/hooks.json"), `{"hooks":{}}`)
	writeFile(t, filepath.Join(dir, ".agents/reviewer.md"), "review")
	return dir
}

func newGate(t *testing.T) *Gate {
	return &Gate{Store: Store{Path: filepath.Join(t.TempDir(), "trust.json")}}
}

func TestCapabilitiesFindsProjectFiles(t *testing.T) {
	dir := project(t)
	var kinds []string
	for _, c := range Capabilities(dir) {
		kinds = append(kinds, c.Kind+"="+c.Path)
	}
	got := strings.Join(kinds, ",")
	for _, want := range []string{"skills=.janetcode/skills", "hooks=.janetcode/hooks.json", "agent profiles=.agents/reviewer.md"} {
		if !strings.Contains(got, want) {
			t.Errorf("capabilities %s missing %s", got, want)
		}
	}
	if len(Capabilities(t.TempDir())) != 0 {
		t.Error("an empty directory has capabilities")
	}
}

func TestNewProjectStartsUntrusted(t *testing.T) {
	dir := project(t)
	g := newGate(t)
	if g.Instructions(dir) {
		t.Error("a project nobody decided about had its skills read")
	}
	if !g.NeedsDecision(dir) {
		t.Error("a project with capability files did not ask")
	}
	// Undecided: an executable falls back to its own approval.
	if g.Exec(dir, false) || !g.Exec(dir, true) {
		t.Error("undecided project did not follow the per-file approval")
	}
	if g.ProjectDir(dir) != "" {
		t.Error("ProjectDir exposed an undecided project")
	}
}

func TestPersistentLevels(t *testing.T) {
	dir := project(t)
	g := newGate(t)
	if err := g.Set(dir, Instructions, true); err != nil {
		t.Fatal(err)
	}
	if !g.Instructions(dir) || g.Exec(dir, true) {
		t.Error("instructions level: want skills in, executables out even when approved per file")
	}
	// A second gate over the same store sees it: it was written down.
	g2 := &Gate{Store: g.Store}
	if !g2.Instructions(dir) {
		t.Error("recorded level lost across processes")
	}
	if err := g.Set(dir, Full, true); err != nil {
		t.Fatal(err)
	}
	if !g.Exec(dir, false) {
		t.Error("full level did not enable an executable")
	}
	if g.NeedsDecision(dir) {
		t.Error("a decided project asks again")
	}
}

func TestChangedFilesAskAgain(t *testing.T) {
	dir := project(t)
	g := newGate(t)
	if err := g.Set(dir, Full, true); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(dir, ".janetcode/hooks.json"), `{"hooks":{"pre_tool":[{"command":"curl evil"}]}}`)
	g.Invalidate()
	st := g.State(dir)
	if st.Decided || st.Source != "changed" {
		t.Fatalf("state after an edit = %+v, want undecided/changed", st)
	}
	if g.Exec(dir, false) {
		t.Error("an edited hooks file kept its full trust")
	}
	// A new skill is a change too.
	g3 := newGate(t)
	if err := g3.Set(dir, Instructions, true); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(dir, ".janetcode/skills/new/SKILL.md"), "new")
	g3.Invalidate()
	if g3.Instructions(dir) {
		t.Error("a skill added after trust was let in")
	}
}

func TestSessionTrustIsNotRecorded(t *testing.T) {
	dir := project(t)
	g := newGate(t)
	if err := g.Set(dir, Full, false); err != nil {
		t.Fatal(err)
	}
	if st := g.State(dir); st.Level != Full || st.Source != "session" {
		t.Fatalf("session state = %+v", st)
	}
	g2 := &Gate{Store: g.Store}
	if g2.Instructions(dir) {
		t.Error("session trust outlived the process")
	}
}

func TestRevokeClearsPerFileApprovals(t *testing.T) {
	dir := project(t)
	g := newGate(t)
	if err := g.Store.Trust(HooksKey(dir), "abc"); err != nil {
		t.Fatal(err)
	}
	if err := g.Set(dir, Untrusted, true); err != nil {
		t.Fatal(err)
	}
	if g.Store.Trusted(HooksKey(dir), "abc") {
		t.Error("revoking left the hook approval behind")
	}
	if g.Exec(dir, true) {
		t.Error("untrusted project ran an approved executable")
	}
	// Distrust survives edits: a changed file is no reason to trust.
	writeFile(t, filepath.Join(dir, ".janetcode/hooks.json"), `{}`)
	g.Invalidate()
	if st := g.State(dir); st.Level != Untrusted || !st.Decided {
		t.Errorf("untrusted project after an edit = %+v", st)
	}
}

func TestFlagWins(t *testing.T) {
	dir := project(t)
	g := newGate(t)
	_ = g.Set(dir, Untrusted, true)
	g.Flag = Instructions
	if !g.Instructions(dir) || g.Exec(dir, true) {
		t.Error("--trust instructions did not win over the recorded level")
	}
}

func TestNilGateLetsEverythingIn(t *testing.T) {
	var g *Gate
	if !g.Instructions("/x") || g.Exec("/x", false) || !g.Exec("/x", true) || g.NeedsDecision("/x") {
		t.Error("nil gate changed behaviour")
	}
}

func TestProjectIdentityIsTheDirectory(t *testing.T) {
	a, b := project(t), project(t)
	g := newGate(t)
	_ = g.Set(a, Full, true)
	if g.Instructions(b) {
		t.Error("trust for one project leaked to another with identical files")
	}
}

func TestReportNamesEveryCapability(t *testing.T) {
	dir := project(t)
	g := newGate(t)
	_ = g.Set(dir, Instructions, true)
	r := g.State(dir).Report()
	for _, want := range []string{"instructions", ".janetcode/skills", ".janetcode/hooks.json", "never approves"} {
		if !strings.Contains(r, want) {
			t.Errorf("report missing %q:\n%s", want, r)
		}
	}
}

func TestParseLevel(t *testing.T) {
	for in, want := range map[string]Level{"full": Full, "instructions": Instructions, "untrusted": Untrusted, "NONE": Untrusted} {
		if got, err := ParseLevel(in); err != nil || got != want {
			t.Errorf("ParseLevel(%q) = %v, %v", in, got, err)
		}
	}
	if _, err := ParseLevel("maybe"); err == nil {
		t.Error("nonsense parsed")
	}
}
