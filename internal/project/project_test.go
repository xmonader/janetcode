package project

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func dirWith(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestDetectsTheObviousProjects(t *testing.T) {
	for _, tc := range []struct {
		files      map[string]string
		kind, verb string
	}{
		{map[string]string{"go.mod": "module x\n"}, "Go", "go test ./..."},
		{map[string]string{"Cargo.toml": "[package]\n"}, "Rust", "cargo test"},
		{map[string]string{"pyproject.toml": "[project]\n"}, "Python", "pytest"},
		{map[string]string{"requirements.txt": "pytest\n"}, "Python", "pytest"},
		{map[string]string{"package.json": `{"scripts":{"test":"jest"}}`}, "Node", "npm test"},
		{map[string]string{"Makefile": "build:\n\tgo build\ntest:\n\tgo test ./...\n"}, "Make", "make test"},
	} {
		p, ok := Detect(dirWith(t, tc.files))
		if !ok || p.Kind != tc.kind || p.Verify != tc.verb {
			t.Errorf("%v → %+v ok=%v, wanted %s / %q", tc.files, p, ok, tc.kind, tc.verb)
		}
	}
}

func TestAPackageJsonWithNoTestScriptProposesNoCommand(t *testing.T) {
	p, ok := Detect(dirWith(t, map[string]string{"package.json": `{"name":"x","dependencies":{}}`}))
	if !ok || p.Kind != "Node" {
		t.Fatalf("%+v ok=%v", p, ok)
	}
	if p.Verify != "" {
		t.Errorf("proposed %q for a project with no test script", p.Verify)
	}
}

func TestAMakefileWithNoCheckTargetProposesNoCommand(t *testing.T) {
	p, ok := Detect(dirWith(t, map[string]string{"Makefile": "all:\n\tcc x.c\ninstall:\n\tcp a b\n"}))
	if !ok || p.Verify != "" {
		t.Fatalf("%+v ok=%v", p, ok)
	}
}

func TestARecipeLineIsNotATarget(t *testing.T) {
	// "test:" indented under a recipe is shell, not a target.
	p, _ := Detect(dirWith(t, map[string]string{"Makefile": "all:\n\techo test: nope\n"}))
	if p.Verify != "" {
		t.Errorf("read a recipe line as a target: %q", p.Verify)
	}
}

func TestGoWinsOverAMakefile(t *testing.T) {
	// A Go project with a Makefile is still a Go project: go test is the
	// command that works in a checkout somebody just cloned.
	p, _ := Detect(dirWith(t, map[string]string{"go.mod": "module x\n", "Makefile": "test:\n\tgo test ./...\n"}))
	if p.Kind != "Go" {
		t.Errorf("kind = %s", p.Kind)
	}
}

func TestAnUnrecognisedDirectorySaysNothing(t *testing.T) {
	p, ok := Detect(dirWith(t, map[string]string{"notes.txt": "hello"}))
	if ok {
		t.Fatalf("recognised %+v in a directory of notes", p)
	}
	if p.Line(func(string) bool { return false }) != "" {
		t.Error("an unrecognised project still produced a line")
	}
}

func TestTheLineIsOneLineAndNamesBothThings(t *testing.T) {
	p, _ := Detect(dirWith(t, map[string]string{"go.mod": "module x\n"}))
	line := p.Line(func(string) bool { return false })
	if strings.Contains(line, "\n") {
		t.Fatalf("more than one line:\n%s", line)
	}
	for _, want := range []string{"Go project", `/verify set "go test ./..."`, "gopls is not on PATH"} {
		if !strings.Contains(line, want) {
			t.Errorf("line is missing %q:\n%s", want, line)
		}
	}
}

func TestAnInstalledServerIsNotMentioned(t *testing.T) {
	p, _ := Detect(dirWith(t, map[string]string{"go.mod": "module x\n"}))
	line := p.Line(func(name string) bool { return name == "gopls" })
	if strings.Contains(line, "gopls") {
		t.Fatalf("mentioned a server that is already installed:\n%s", line)
	}
	if !strings.Contains(line, "/verify set") {
		t.Fatalf("dropped the verify proposal too:\n%s", line)
	}
}

func TestDetectWritesNothing(t *testing.T) {
	dir := dirWith(t, map[string]string{"go.mod": "module x\n"})
	before, _ := os.ReadDir(dir)
	p, _ := Detect(dir)
	p.Line(func(string) bool { return false })
	after, _ := os.ReadDir(dir)
	if len(before) != len(after) {
		t.Fatalf("detection created something: %d → %d entries", len(before), len(after))
	}
}
