package typosquat

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResembleKinds(t *testing.T) {
	for _, c := range []struct{ name, known, want string }{
		{"Requests", "requests", KindCase},
		{"python_dateutil", "python-dateutil", KindSeparator},
		{"jeIlyfish", "jellyfish", KindLookalike},
		{"rеquests", "requests", KindLookalike}, // a Cyrillic е
		{"rnoment", "moment", KindLookalike},
		{"nodemailer-js", "nodemailer", KindAffix},
		{"python3-dateutil", "python-dateutil", KindAffix},
		{"reqeusts", "requests", KindEdit}, // a transposition is one edit
		{"lodashs", "lodash", KindEdit},
		{"requests", "requests", ""},
		{"ws", "wx", ""}, // too short for the edit rule
		{"viper", "cobra", ""},
	} {
		if got := Resemble(c.name, c.known); got != c.want {
			t.Errorf("Resemble(%q, %q) = %q, want %q", c.name, c.known, got, c.want)
		}
	}
}

func TestCheckNeverFlagsAKnownName(t *testing.T) {
	k := Popular()
	for _, n := range []Name{{NPM, "preact"}, {NPM, "react"}, {NPM, "request"}, {NPM, "requests"}, {PyImport, "json"}, {NPM, "fs"}} {
		if f, ok := Check(k, n); ok {
			t.Errorf("%v flagged: %+v", n, f)
		}
	}
	// PyPI compares as it normalises: python_dateutil IS python-dateutil.
	if f, ok := Check(k, Name{PyPI, "python_dateutil"}); ok {
		t.Errorf("a PyPI spelling of the same name flagged: %+v", f)
	}
}

func TestCheckGoPaths(t *testing.T) {
	k := Popular()
	for path, want := range map[string]string{
		"github.com/Sirupsen/logrus":            KindCase,
		"github.com/boltdb-go/bolt":             KindAffix,
		"github.com/stretchr/testfy/assert":     KindEdit,
		"github.com/stretchr/testify/assert":    "",
		"github.com/sirupsen/logrus/hooks/test": "",
		"gitlab.com/spf13/cobra":                "", // another host is another module
		"example.com/internal/thing":            "",
	} {
		f, ok := Check(k, Name{Go, path})
		if (want == "") == ok || (ok && f.Kind != want) {
			t.Errorf("%s: %+v %v, want %q", path, f, ok, want)
		}
	}
}

func TestCheckNonASCIIAlone(t *testing.T) {
	f, ok := Check(Popular(), Name{NPM, "ünicorn-thing"})
	if !ok || f.Kind != KindNonASCII {
		t.Fatalf("got %+v %v", f, ok)
	}
}

func TestAddedReadsImportsAndManifests(t *testing.T) {
	for _, c := range []struct {
		path, before, after string
		want                []string
	}{
		{"main.go", "package m\nimport \"fmt\"\n", "package m\nimport (\n\t\"fmt\"\n\tlog \"github.com/Sirupsen/logrus\"\n\t\"os\"\n)\n", []string{"github.com/Sirupsen/logrus"}},
		// Half an edit that does not parse still has its import lines read.
		{"x.go", "", "package m\nimport (\n\t\"github.com/a/b\"\n", []string{"github.com/a/b"}},
		{"app.py", "import os\n", "import os, reqeusts\nfrom numpi.linalg import norm\nfrom . import local\nfrom .x import y\n", []string{"numpi", "reqeusts"}},
		{"a.ts", "", "import x from 'axois';\nimport './side';\nconst f = require(\"fs\");\nimport y from '@babel/croe/lib/x';\nimport z from 'node:path';\nconst w = await import('lodashs');\n", []string{"@babel/croe", "axois", "fs", "lodashs"}},
		{"requirements.txt", "requests\n", "requests\nreqeusts==1.0\n", []string{"reqeusts"}},
		{"README.md", "", "import x from 'axois'", nil},
	} {
		var got []string
		for _, n := range Added(c.path, []byte(c.before), []byte(c.after)) {
			got = append(got, n.Name)
		}
		if strings.Join(got, ",") != strings.Join(c.want, ",") {
			t.Errorf("%s: added %v, want %v", c.path, got, c.want)
		}
	}
}

func TestLoadKnowsTheProject(t *testing.T) {
	root := t.TempDir()
	write := func(p, s string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(filepath.Join(root, p)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, p), []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("go.mod", "module example.com/me/app\n\nrequire (\n\tgithub.com/acme/widgetz v1.0.0\n\tgithub.com/acme/gadget v1.0.0 // indirect\n)\n")
	write("package.json", `{"dependencies":{"left-padd":"1.0.0"}}`)
	write("node_modules/@acme/thing/package.json", "{}")
	write("requirements.txt", "my-private-lib\n")
	write("pkg/helpers.py", "")
	k := Load(root, filepath.Join(root, "pkg", "main.py"))
	for _, n := range []Name{{Go, "github.com/acme/widgetz"}, {Go, "github.com/acme/gadget"}, {Go, "example.com/me/app"}, {NPM, "left-padd"}, {NPM, "@acme/thing"}, {PyPI, "my_private_lib"}, {PyImport, "my_private_lib"}, {PyImport, "helpers"}} {
		if !k.Has(n.Eco, n.Name) {
			t.Errorf("Load does not know %v", n)
		}
	}
	// A near miss of the project's own dependency is flagged as such.
	f, ok := Check(k, Name{Go, "github.com/acme/widgets"})
	if !ok || f.Like != "github.com/acme/widgetz" || f.Source != SrcProject {
		t.Errorf("near miss of a project dependency: %+v %v", f, ok)
	}
	// The project's own module's packages are never flagged.
	if f, ok := Check(k, Name{Go, "example.com/me/app/internal/x"}); ok {
		t.Errorf("own package flagged: %+v", f)
	}
}

func TestScanModCacheUnescapes(t *testing.T) {
	dir := t.TempDir()
	for _, p := range []string{"cache/download/github.com/!burnt!sushi/toml/@v", "cache/download/golang.org/x/net/@v", "cache/download/sumdb/sum.golang.org/lookup"} {
		if err := os.MkdirAll(filepath.Join(dir, p), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	got := scanModCache(dir, 100, 1e9)
	if strings.Join(got, ",") != "github.com/BurntSushi/toml,golang.org/x/net" {
		t.Fatalf("scanModCache = %v", got)
	}
}

func TestNoteNamesTheResemblance(t *testing.T) {
	n := Note("/r/requirements.txt", []Finding{{Eco: PyPI, Name: "reqeusts", Like: "requests", Kind: KindEdit, Source: SrcPopular}})
	if !strings.Contains(n, "requirements.txt adds") || !strings.Contains(n, "reqeusts is one keystroke from requests (a well-known package)") {
		t.Fatalf("note %q", n)
	}
	if Note("x", nil) != "" {
		t.Fatal("a note for nothing")
	}
}

// TestSeededTyposquats is the idea's pre-screen: the detection rate on
// seeded typosquat names, and the false-alarm rate on real packages that
// are not on the list, both against what an empty project knows (the
// well-known lists alone). Kill criterion: detection under 60% or false
// alarms over 15%.
func TestSeededTyposquats(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "seeded.txt"))
	if err != nil {
		t.Fatal(err)
	}
	k := Popular()
	var squats, caught, legit, alarms int
	var missed, alarmed []string
	for _, line := range strings.Split(string(data), "\n") {
		f := strings.Fields(line)
		if len(f) < 3 || strings.HasPrefix(f[0], "#") {
			continue
		}
		fd, ok := Check(k, Name{Eco: f[1], Name: f[2]})
		switch f[0] {
		case "squat":
			squats++
			if ok {
				caught++
			} else {
				missed = append(missed, f[2])
			}
		case "legit":
			legit++
			if ok {
				alarms++
				alarmed = append(alarmed, f[2]+" ("+fd.Kind+" "+fd.Like+")")
			}
		}
	}
	det := float64(caught) / float64(squats)
	far := float64(alarms) / float64(legit)
	t.Logf("seeded typosquats: caught %d/%d (%.0f%%); missed %v", caught, squats, 100*det, missed)
	t.Logf("real packages off the list: %d/%d flagged (%.0f%%): %v", alarms, legit, 100*far, alarmed)
	if det < 0.60 || far > 0.15 {
		t.Errorf("pre-screen fails its kill criterion: detection %.0f%% (want >= 60%%), false alarms %.0f%% (want <= 15%%)", 100*det, 100*far)
	}
}
