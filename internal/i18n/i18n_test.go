package i18n

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// Every locale carries exactly the English key set, with the same format
// verbs in the same order: a translation that drops a %s prints
// "%!(EXTRA string=…)" into the interface.
func TestLocalesMatchTheKeySet(t *testing.T) {
	seen := map[string]bool{}
	for _, k := range source {
		if seen[k] {
			t.Errorf("key listed twice: %q", k)
		}
		seen[k] = true
	}
	verbs := regexp.MustCompile(`%[a-z]`)
	for code, cat := range locales {
		for k := range English {
			v, ok := cat[k]
			if !ok || v == "" {
				t.Errorf("%s: missing %q", code, k)
				continue
			}
			if a, b := verbs.FindAllString(k, -1), verbs.FindAllString(v, -1); strings.Join(a, ",") != strings.Join(b, ",") {
				t.Errorf("%s: %q has verbs %v, translation %q has %v", code, k, a, v, b)
			}
		}
		for k := range cat {
			if !English[k] {
				t.Errorf("%s: %q is not a key (stale translation?)", code, k)
			}
		}
	}
}

// The other direction: every string the tree passes to T or Tf is a key,
// and every key is used — by a T call, or as text the web page carries
// (which it translates by matching its own elements' text).
func TestCatalogueAndTreeAgree(t *testing.T) {
	root := filepath.Join("..", "..")
	call := regexp.MustCompile(`i18n\.Tf?\(("(?:[^"\\]|\\.)*")`)
	used := map[string]bool{}
	var web, goSrc strings.Builder
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "node_modules", "vendor", ".claude":
				return filepath.SkipDir
			case "repos":
				// evals/realrepo/repos: other projects cloned for evals.
				if filepath.Base(filepath.Dir(p)) == "realrepo" {
					return filepath.SkipDir
				}
			}
			return nil
		}
		switch {
		case strings.HasSuffix(p, ".go") && !strings.HasSuffix(p, "_test.go"):
			b, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			if !strings.Contains(p, filepath.Join("internal", "i18n")) {
				goSrc.Write(b)
			}
			for _, m := range call.FindAllStringSubmatch(string(b), -1) {
				s, err := strconv.Unquote(m[1])
				if err != nil {
					t.Errorf("%s: %v", p, err)
					continue
				}
				used[s] = true
				if !English[s] {
					t.Errorf("%s: i18n.T(%q) is not in the catalogue", p, s)
				}
			}
		case strings.Contains(p, filepath.Join("webui", "static")) && !strings.Contains(p, "vendor") &&
			(strings.HasSuffix(p, ".html") || strings.HasSuffix(p, ".js")):
			b, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			web.Write(b)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	// A key reaching T through a variable (a table of help lines, a phase
	// name) is used when the tree spells it as a literal somewhere.
	page, src := web.String(), goSrc.String()
	for _, k := range source {
		if !used[k] && !strings.Contains(page, k) && !strings.Contains(src, strconv.Quote(k)) {
			t.Errorf("key %q is used by nothing", k)
		}
	}
}

func TestResolve(t *testing.T) {
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	cases := []struct {
		lang string
		env  map[string]string
		want string
	}{
		{"", nil, "en"},
		{"es", nil, "es"},
		{"Spanish", nil, "es"},
		{"español", nil, "es"},
		{"fr-CA", nil, "fr"},
		{"German", map[string]string{"LANG": "es_ES.UTF-8"}, "en"}, // asked for, not carried: English, not LANG
		{"", map[string]string{"LANG": "es_ES.UTF-8"}, "es"},
		{"", map[string]string{"LANG": "fr_FR.UTF-8", "LC_ALL": "C"}, "en"},
		{"", map[string]string{"LC_MESSAGES": "fr_BE@euro"}, "fr"},
	}
	for _, c := range cases {
		if got := Resolve(c.lang, env(c.env)); got != c.want {
			t.Errorf("Resolve(%q, %v) = %q, want %q", c.lang, c.env, got, c.want)
		}
	}
}

func TestFallsBackToEnglish(t *testing.T) {
	defer SetLanguage("en")
	SetLanguage("es")
	if got := T("allow once"); got != "permitir una vez" {
		t.Errorf("T = %q", got)
	}
	if got := T("not a key at all"); got != "not a key at all" {
		t.Errorf("unknown key = %q", got)
	}
	SetLanguage("xx")
	if Active() != "en" || T("allow once") != "allow once" {
		t.Errorf("unknown locale should be English, got %q", Active())
	}
}

func TestKeyLabel(t *testing.T) {
	SetLanguage("fr")
	t.Cleanup(func() { SetLanguage("en") })
	for in, want := range map[string]string{"enter": "entrée", "shift+tab": "maj+tab", "esc esc": "échap échap", "ctrl+x ctrl+e": "ctrl+x ctrl+e", "": ""} {
		if got := KeyLabel(in); got != want {
			t.Errorf("KeyLabel(%q) = %q, want %q", in, got, want)
		}
	}
	SetLanguage("en")
	if got := KeyLabel("enter"); got != "enter" {
		t.Errorf("English KeyLabel(enter) = %q", got)
	}
}
