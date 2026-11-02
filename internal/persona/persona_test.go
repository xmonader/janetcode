package persona

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func put(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestLoadMergesSourcesAndShadows(t *testing.T) {
	cwd, cfgDir := t.TempDir(), t.TempDir()
	cfg := map[string]Persona{
		"terse": {Instructions: "from config"},
		"calm":  {Tone: "calm"},
	}
	put(t, filepath.Join(cfgDir, "personas/terse.md"), "---\ndescription: global terse\n---\nglobal body")
	put(t, filepath.Join(cwd, ".janetcode/personas/terse.json"), `{"description":"project terse","instructions":"project body","effort":"low"}`)
	put(t, filepath.Join(cwd, ".janetcode/personas/rev.md"), "---\ntone: harsh\noutput_format: bullet list\nmodel: big\n---\nFind bugs.")
	put(t, filepath.Join(cwd, ".janetcode/personas/broken.json"), `{not json`)
	put(t, filepath.Join(cwd, ".janetcode/personas/empty.md"), "---\ndescription: nothing\n---\n")
	put(t, filepath.Join(cwd, ".janetcode/personas/badeffort.json"), `{"instructions":"x","effort":"turbo"}`)
	ps, bad := Load(cfg, cwd, cfgDir)
	var got []string
	for _, p := range ps {
		got = append(got, p.Name+":"+p.Source)
	}
	if want := []string{"calm:config", "rev:project", "terse:project"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("personas %v want %v", got, want)
	}
	if len(bad) != 3 {
		t.Fatalf("bad %v", bad)
	}
	terse, _ := Find(ps, "TERSE")
	if terse.Instructions != "project body" || terse.Effort != "low" {
		t.Errorf("project did not shadow: %+v", terse)
	}
	rev, _ := Find(ps, "rev")
	if rev.Model != "big" || !strings.Contains(rev.Body(), "Tone: harsh\n\nOutput format: bullet list\n\nFind bugs.") {
		t.Errorf("rev %+v / %q", rev, rev.Body())
	}
}

func TestUntrustedProjectContributesNothing(t *testing.T) {
	cwd := t.TempDir()
	put(t, filepath.Join(cwd, ".janetcode/personas/x.md"), "body")
	if ps, _ := Load(nil, "", ""); len(ps) != 0 {
		t.Errorf("%v", ps)
	}
}

func TestBlockCarriesTheContract(t *testing.T) {
	p := Persona{Name: "rev", Tone: "dry", Instructions: "Review the diff.",
		Inputs:  []IOField{{Name: "diff", Type: "file", Required: true, Description: "path to the diff"}},
		Outputs: []IOField{{Name: "report", Description: "findings, worst first"}}}
	got := p.Block()
	for _, w := range []string{`<persona name="rev">`, "Tone: dry", "Review the diff.", "Inputs you expect from the brief:", "- diff (file, required): path to the diff", "Outputs you must produce:", "- report (text): findings, worst first", "</persona>"} {
		if !strings.Contains(got, w) {
			t.Errorf("block lacks %q:\n%s", w, got)
		}
	}
}

func TestFindFailsClosed(t *testing.T) {
	if _, err := Find(nil, "x"); err == nil || !strings.Contains(err.Error(), "none are defined") {
		t.Errorf("%v", err)
	}
	if _, err := Find([]Persona{{Name: "a"}, {Name: "b"}}, "x"); err == nil || !strings.Contains(err.Error(), "a, b") {
		t.Errorf("%v", err)
	}
}

func TestValidate(t *testing.T) {
	for name, p := range map[string]Persona{
		"no name":    {Instructions: "x"},
		"spaces":     {Name: "a b", Instructions: "x"},
		"empty":      {Name: "a"},
		"bad effort": {Name: "a", Instructions: "x", Effort: "turbo"},
	} {
		if p.Validate() == nil {
			t.Errorf("%s accepted", name)
		}
	}
	if err := (Persona{Name: "ok", Outputs: []IOField{{Name: "r", Description: "d"}}}).Validate(); err != nil {
		t.Errorf("contract-only persona refused: %v", err)
	}
}

func TestReport(t *testing.T) {
	if r := Report(nil, nil); !strings.Contains(r, "no personas defined") {
		t.Error(r)
	}
	r := Report([]Persona{{Name: "rev", Source: "project", Description: "reviewer", Model: "big", Inputs: []IOField{{Name: "diff", Description: "d"}}}}, []string{"x: broken"})
	for _, w := range []string{"rev  (project)  reviewer", "model big effort -", "in:  diff - d", "skipped: x: broken"} {
		if !strings.Contains(r, w) {
			t.Errorf("report lacks %q:\n%s", w, r)
		}
	}
}
