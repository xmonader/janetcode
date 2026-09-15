package skill

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/llm"
)

// writeSkill puts one skill file in a project and returns the project.
func writeSkills(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range files {
		p := filepath.Join(dir, ".janetcode", "skills", name+".md")
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// TestRowsPriceAndOrder is the arithmetic /skills reports: a skill's
// body is priced with the shared estimator, the index line is priced
// separately because it is paid for every turn, and the heaviest skill
// comes first.
func TestRowsPriceAndOrder(t *testing.T) {
	dir := writeSkills(t, map[string]string{
		"small": "---\ndescription: a small one\n---\n\nshort body\n",
		"big":   "---\ndescription: a big one\n---\n\n" + strings.Repeat("a fairly long paragraph about migrations. ", 60),
		"user":  "---\ndescription: typed only\ndisable-model-invocation: true\n---\n\nbody\n",
	})
	rows := Rows(Load(dir, ""), nil)
	if len(rows) != 3 {
		t.Fatalf("got %d rows, want 3", len(rows))
	}
	if rows[0].Name != "big" {
		t.Errorf("heaviest first: got %q", rows[0].Name)
	}
	for _, r := range rows {
		if r.Body <= 0 {
			t.Errorf("%s priced at %d tokens", r.Name, r.Body)
		}
	}
	var big, small, user Row
	for _, r := range rows {
		switch r.Name {
		case "big":
			big = r
		case "small":
			small = r
		case "user":
			user = r
		}
	}
	if big.Body <= small.Body*2 {
		t.Errorf("big (%d) should dwarf small (%d)", big.Body, small.Body)
	}
	if small.Index <= 0 {
		t.Errorf("a model-invocable skill costs an index line every turn, got %d", small.Index)
	}
	if user.Index != 0 {
		t.Errorf("a /name-only skill is not in the index, got %d tokens", user.Index)
	}
}

// TestUsedThisSession is the second half of the question: which of the
// skills that cost tokens every turn have ever fired. Attaching one,
// reading one with the skill tool and touching a matching path all
// count; a skill nothing matched stays unused.
func TestUsedThisSession(t *testing.T) {
	dir := writeSkills(t, map[string]string{
		"migrations": "---\ndescription: how to write a database migration\nkeywords: migrations\n---\n\nrun the migration tool\n",
		"quiet":      "---\ndescription: nothing matches this one\n---\n\nquiet body\n",
	})
	load := func() []Skill { return Load(dir, "") }
	in := &Injector{Skills: load}

	for _, tc := range []struct {
		name  string
		run   func()
		used  map[string]int
		fresh []string
	}{
		{name: "nothing yet", run: func() {}, used: map[string]int{}},
		{name: "matched a turn", run: func() {
			in.Prepare([]llm.Block{{Type: llm.BlockText, Text: "write the migrations for the users table"}})
		}, used: map[string]int{"migrations": 1}, fresh: []string{"migrations"}},
		{name: "read with the tool", run: func() {
			(&Tool{Skills: load, Used: in.MarkUsed}).Run(t.Context(), []byte(`{"name":"quiet"}`))
		}, used: map[string]int{"migrations": 1, "quiet": 1}, fresh: []string{"migrations", "quiet"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.run()
			stats := in.Stats()
			for name, want := range tc.used {
				if stats[name].Used != want {
					t.Errorf("%s used %d times, want %d", name, stats[name].Used, want)
				}
			}
			for name := range stats {
				if _, ok := tc.used[name]; !ok {
					t.Errorf("%s was never used but has a stat", name)
				}
			}
			for _, name := range tc.fresh {
				if !stats[name].Fresh {
					t.Errorf("%s should still be in the context", name)
				}
			}
		})
	}
}

// TestReportSaysWhatItCostsAndWhetherItFired is the sheet itself.
func TestReportSaysWhatItCostsAndWhetherItFired(t *testing.T) {
	dir := writeSkills(t, map[string]string{
		"migrations": "---\ndescription: how to write a database migration\n---\n\nrun the migration tool\n",
		"quiet":      "---\ndescription: nothing matches this one\n---\n\nquiet body\n",
	})
	skills := Load(dir, "")
	in := &Injector{Skills: skills2fn(skills)}
	in.MarkUsed("migrations")
	res := Report(skills, in.Stats(), 10000, "nowhere")
	for _, want := range []string{"migrations", "quiet", "in context", "unused", "index lines in every request", "of the 10k prefix", "1 of 2 have been sent"} {
		if !strings.Contains(res.Sheet, want) {
			t.Errorf("sheet does not mention %q:\n%s", want, res.Sheet)
		}
	}
	if len(Report(nil, nil, 0, "add a skill file").Tips) != 0 {
		t.Error("an empty listing should not scold the user about unused skills")
	}
	if tips := Report(skills, nil, 0, "x").Tips; len(tips) == 0 {
		t.Error("skills that have never fired deserve a tip saying so")
	}
}

func skills2fn(s []Skill) func() []Skill { return func() []Skill { return s } }
