package occ

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const oneImport = `package pkg

import "fmt"

func A() string { return fmt.Sprint("a") }

func B() string { return "b" }

func C() string { return "c" }
`

// The shape two subagents produce most: each turned the one-line import
// into a block holding what its own code needs.
func TestMergeBothWidenASingleImport(t *testing.T) {
	ours := strings.Replace(oneImport, `import "fmt"`, "import (\n\t\"fmt\"\n\t\"strings\"\n)", 1)
	ours = strings.Replace(ours, `return "b"`, `return strings.ToUpper("b")`, 1)
	theirs := strings.Replace(oneImport, `import "fmt"`, "import (\n\t\"fmt\"\n\t\"sort\"\n)", 1)
	theirs = strings.Replace(theirs, `return "c"`, `s := []string{"c"}; sort.Strings(s); return s[0]`, 1)
	got, why := Merge("p.go", []byte(oneImport), []byte(ours), []byte(theirs))
	if why != "" {
		t.Fatalf("refused: %s", why)
	}
	want := "import (\n\t\"fmt\"\n\t\"sort\"\n\t\"strings\"\n)\n"
	if !strings.Contains(string(got), want) || !strings.Contains(string(got), "strings.ToUpper") || !strings.Contains(string(got), "sort.Strings") {
		t.Fatalf("merged:\n%s", got)
	}
}

// One side removed the last use of fmt and its import; the other added
// an import. The union would keep fmt, unused, which Go refuses.
func TestMergeImportsPrunesWhatNoSideUses(t *testing.T) {
	ours := strings.Replace(oneImport, `import "fmt"`, "import (\n\t\"strings\"\n)", 1)
	ours = strings.Replace(ours, `return fmt.Sprint("a")`, `return strings.ToLower("A")`, 1)
	theirs := strings.Replace(oneImport, `import "fmt"`, "import (\n\t\"fmt\"\n\t\"os\"\n)", 1)
	theirs = strings.Replace(theirs, `return "c"`, `return os.Getenv("C")`, 1)
	got, why := Merge("p.go", []byte(oneImport), []byte(ours), []byte(theirs))
	if why != "" {
		t.Fatalf("refused: %s", why)
	}
	if strings.Contains(string(got), `"fmt"`) || !strings.Contains(string(got), `"os"`) || !strings.Contains(string(got), `"strings"`) {
		t.Fatalf("merged:\n%s", got)
	}
}

// A comment in the imports would not survive the rewrite, so the rewrite
// declines and the overlap is reported.
func TestMergeImportsWithCommentDeclines(t *testing.T) {
	base := strings.Replace(oneImport, `import "fmt"`, `import "fmt" // printing`, 1)
	ours := strings.Replace(base, `import "fmt" // printing`, "import (\n\t\"fmt\" // printing\n\t\"strings\"\n)", 1)
	theirs := strings.Replace(base, `import "fmt" // printing`, "import (\n\t\"fmt\" // printing\n\t\"sort\"\n)", 1)
	if _, why := Merge("p.go", []byte(base), []byte(ours), []byte(theirs)); why == "" {
		t.Fatal("imports with a comment were rewritten")
	}
}

// The offline pre-screen for the fanout fixture: the three changes 60
// asks for, written as three subagents would write them from one base,
// merged in finishing order. Sorting Names and trimming in Put touch
// different methods and merge; folding case touches Put too, so it
// meets the trim edit there — the overlap the fixture exists to make,
// and the one conflict that must reach a subagent.
func TestFanoutFixtureOverlap(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "evals", "fanout", "60-parallel-overlap.json"))
	if err != nil {
		t.Fatal(err)
	}
	var task struct {
		Files map[string]string `json:"files"`
	}
	if err := json.Unmarshal(raw, &task); err != nil {
		t.Fatal(err)
	}
	base := task.Files["pkg/registry.go"]
	if base == "" {
		t.Fatal("fixture has no pkg/registry.go")
	}
	trim := strings.Replace(base, `import "fmt"`, "import (\n\t\"fmt\"\n\t\"strings\"\n)", 1)
	trim = strings.Replace(trim, "\tif name == \"\" {", "\tname = strings.TrimSpace(name)\n\tif name == \"\" {", 1)
	fold := strings.Replace(base, `import "fmt"`, "import (\n\t\"fmt\"\n\t\"strings\"\n)", 1)
	fold = strings.Replace(fold, "\tr.items[name] = value", "\tr.items[strings.ToLower(name)] = value", 1)
	fold = strings.Replace(fold, "\tv, ok := r.items[name]", "\tv, ok := r.items[strings.ToLower(name)]", 1)
	sorted := strings.Replace(base, `import "fmt"`, "import (\n\t\"fmt\"\n\t\"sort\"\n)", 1)
	sorted = strings.Replace(sorted, "\treturn out\n}", "\tsort.Strings(out)\n\treturn out\n}", 1)

	// sorted lands first; trim merges onto it.
	project, why := Merge("pkg/registry.go", []byte(base), []byte(trim), []byte(sorted))
	if why != "" {
		t.Fatalf("trim onto sorted refused: %s", why)
	}
	if !strings.Contains(string(project), "sort.Strings(out)") || !strings.Contains(string(project), "strings.TrimSpace") || !strings.Contains(string(project), "\t\"sort\"\n\t\"strings\"\n") {
		t.Fatalf("trim+sorted:\n%s", project)
	}
	// fold, from the same base, meets trim in Put.
	_, why = Merge("pkg/registry.go", []byte(base), []byte(fold), project)
	if !strings.Contains(why, "Put") {
		t.Fatalf("fold onto trim+sorted: why = %q, want the clash in Put", why)
	}
}
