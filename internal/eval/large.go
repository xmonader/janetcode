package eval

import (
	"fmt"
	"sort"
	"strings"
)

// Large task names (31–35). Distinct from 01–20 and from the 21–26 hard set.
var LargeTaskNames = []string{
	"31-large-inventory",
	"32-large-rename",
	"33-large-sum",
	"34-large-headers",
	"35-large-deadcode",
}

// LargeHarnesses is the 1M-token comparison set: no openclaw.
var LargeHarnesses = []string{
	"pi", "janetcode", "qwencode", "hermes-agent", "grok",
}

// SessionHarnesses can continue a conversation across non-interactive
// invocations (pi --session-id, qwen --resume, grok --resume); hermes-agent
// and openclaw cannot, so a session would restart from nothing on them.
var SessionHarnesses = []string{
	"pi", "janetcode", "qwencode", "grok",
}

const (
	LargeTaskMin = 31
	LargeTaskMax = 35
	// Per-file padding so a multi-turn scan of the tree can accumulate on
	// the order of 1e6 input+cache-read tokens (session total, not one request).
	largeFiles  = 160
	largeRepeat = 220
)

// FilterLarge keeps evals 31–35, sorted by numeric prefix.
func FilterLarge(tasks []Task) []Task {
	want := map[string]bool{}
	for _, n := range LargeTaskNames {
		want[n] = true
	}
	var out []Task
	for _, t := range tasks {
		n := TaskIndex(t.Name)
		if want[t.Name] || (n >= LargeTaskMin && n <= LargeTaskMax) {
			out = append(out, t)
		}
	}
	sort.Slice(out, func(i, j int) bool { return TaskIndex(out[i].Name) < TaskIndex(out[j].Name) })
	return out
}

// LargeCorpus returns the seeded file tree for a large eval, or nil.
func LargeCorpus(name string) map[string]string {
	switch name {
	case "31-large-inventory":
		return corpusInventory()
	case "32-large-rename":
		return corpusRename()
	case "33-large-sum":
		return corpusSum()
	case "34-large-headers":
		return corpusHeaders()
	case "35-large-deadcode":
		return corpusDeadcode()
	case "42-session-records":
		return corpusRecords()
	default:
		return nil
	}
}

// corpusRecords seeds the 42-session-records session: 120 record files,
// each with an id, an owner and a size, plus the expected answers to
// every prompt under .eval-expected/ and the check script that compares
// them. The owners are weighted so the "most records" answer is unique,
// and carol's records are the ones the session later archives.
func corpusRecords() map[string]string {
	owners := []string{"alice", "bob", "carol", "dave", "erin", "frank"}
	weights := []int{30, 25, 24, 20, 12, 9} // sums to 120
	var pool []string
	for i, o := range owners {
		for n := 0; n < weights[i]; n++ {
			pool = append(pool, o)
		}
	}
	const n = 120
	files := map[string]string{}
	count := map[string]int{}
	size := map[string]int{}
	var ids, final []string
	changed := 0
	for i := 1; i <= n; i++ {
		owner := pool[(i*11)%n] // spread the owners through the tree
		sz := (i*37)%500 + 10
		id := fmt.Sprintf("TOKEN_%04d", i)
		files[fmt.Sprintf("data/r%04d.txt", i)] = fmt.Sprintf("id: %s\nowner: %s\nsize: %d\nblob: %s\n", id, owner, sz, pad(i, "rec"))
		count[owner]++
		size[owner] += sz
		ids = append(ids, id)
		if owner == "carol" {
			final = append(final, fmt.Sprintf("ARCHIVED_%04d", i))
			changed++
		} else {
			final = append(final, id)
		}
	}
	sort.Strings(ids)
	sort.Strings(final)
	sorted := append([]string(nil), owners...)
	sort.Strings(sorted)
	top, topN := "", -1
	var sizes strings.Builder
	for _, o := range sorted {
		if count[o] > topN {
			top, topN = o, count[o]
		}
		fmt.Fprintf(&sizes, "%s %d\n", o, size[o])
	}
	files[".eval-expected/owners.txt"] = strings.Join(sorted, "\n") + "\n"
	files[".eval-expected/inventory.txt"] = strings.Join(ids, "\n") + "\n"
	files[".eval-expected/inventory_final.txt"] = strings.Join(final, "\n") + "\n"
	files[".eval-expected/top_owner.txt"] = top + "\n"
	files[".eval-expected/sizes.txt"] = sizes.String()
	files[".eval-expected/changed.txt"] = fmt.Sprint(changed) + "\n"
	files[".eval-expected/check.sh"] = `#!/bin/bash
# Every prompt of the session left a file behind; the last also answered
# with a number. Each is compared with what the tree really contains.
set -u
E=.eval-expected
fail() { echo "check: $1"; exit 1; }
norm() { sed -e 's/[[:space:]]*$//' -e '/^$/d' "$1"; }
[ -f owners.txt ] || fail "owners.txt missing (prompt 1)"
diff <(norm owners.txt) <(norm $E/owners.txt) >/dev/null || fail "owners.txt wrong (prompt 1)"
[ -f top_owner.txt ] || fail "top_owner.txt missing (prompt 3)"
[ "$(tr -d '[:space:]' < top_owner.txt)" = "$(tr -d '[:space:]' < $E/top_owner.txt)" ] || fail "top_owner.txt wrong (prompt 3)"
[ -f sizes.txt ] || fail "sizes.txt missing (prompt 4)"
diff <(norm sizes.txt | tr -s ' ') <(norm $E/sizes.txt) >/dev/null || fail "sizes.txt wrong (prompt 4)"
archived=$(grep -l '^id: ARCHIVED_' data/*.txt 2>/dev/null | wc -l | tr -d ' ')
[ "$archived" = "$(tr -d '[:space:]' < $E/changed.txt)" ] || fail "expected $(cat $E/changed.txt) archived records in data/, found $archived (prompt 5)"
for f in $(grep -l '^owner: carol' data/*.txt); do grep -q '^id: ARCHIVED_' "$f" || fail "$f is carol's but not archived (prompt 5)"; done
for f in $(grep -L '^owner: carol' data/*.txt); do grep -q '^id: TOKEN_' "$f" || fail "$f is not carol's but was changed (prompt 5)"; done
[ -f inventory.txt ] || fail "inventory.txt missing (prompt 2/6)"
diff <(norm inventory.txt) <(norm $E/inventory_final.txt) >/dev/null || fail "inventory.txt does not reflect the renames (prompt 6)"
grep -qw "$(tr -d '[:space:]' < $E/changed.txt)" "$EVAL_OUTPUT" || fail "final reply does not state the number of files changed (prompt 6)"
exit 0
`
	return files
}

func expandLarge(t *Task) {
	extra := LargeCorpus(t.Name)
	if extra == nil {
		return
	}
	if t.Files == nil {
		t.Files = map[string]string{}
	}
	for k, v := range extra {
		if _, ok := t.Files[k]; !ok {
			t.Files[k] = v
		}
	}
}

func pad(id int, tag string) string {
	var b strings.Builder
	tok := fmt.Sprintf("%s_%04d ", tag, id)
	b.Grow(largeRepeat * len(tok))
	for i := 0; i < largeRepeat; i++ {
		b.WriteString(tok)
	}
	return b.String()
}

func corpusInventory() map[string]string {
	files := map[string]string{}
	var tokens []string
	for i := 1; i <= largeFiles; i++ {
		tok := fmt.Sprintf("TOKEN_%04d", i)
		tokens = append(tokens, tok)
		files[fmt.Sprintf("data/f%04d.txt", i)] = fmt.Sprintf("id: %s\nblob: %s\n", tok, pad(i, "inv"))
	}
	sort.Strings(tokens)
	files["expected.txt"] = strings.Join(tokens, "\n") + "\n"
	return files
}

func corpusRename() map[string]string {
	files := map[string]string{
		"go.mod":         "module example.com/large\n\ngo 1.22\n",
		"widget.go":      "package large\n\n// Widget is the exported symbol to rename to Gadget.\nfunc Widget() int { return 0 }\n",
		"widget_test.go": "package large\n\nimport \"testing\"\n\nfunc TestZero(t *testing.T) {\n\tif Widget() != 0 {\n\t\tt.Fatal(0)\n\t}\n}\n",
	}
	for i := 1; i < largeFiles; i++ {
		files[fmt.Sprintf("pkg/p%03d/doc.go", i)] = fmt.Sprintf("package p%03d\n\n// Widget is referenced in this package's docs.\nconst Ref = %d\n// %s\n", i, i, pad(i, "ren"))
	}
	return files
}

func corpusSum() map[string]string {
	files := map[string]string{}
	sum := 0
	for i := 1; i <= largeFiles; i++ {
		sum += i
		files[fmt.Sprintf("vals/v%04d.txt", i)] = fmt.Sprintf("value: %d\n%s\n", i, pad(i, "sum"))
	}
	files["expected_sum.txt"] = fmt.Sprintf("%d\n", sum)
	return files
}

func corpusHeaders() map[string]string {
	files := map[string]string{
		"go.mod": "module example.com/hdr\n\ngo 1.22\n",
	}
	for i := 0; i < largeFiles; i++ {
		files[fmt.Sprintf("pkg/h%03d.go", i)] = fmt.Sprintf("package hdr\n\nfunc H%03d() int { return %d }\n// %s\n", i, i, pad(i, "hdr"))
	}
	files["pkg/hdr_test.go"] = "package hdr\n\nimport \"testing\"\n\nfunc TestH000(t *testing.T) {\n\tif H000() != 0 {\n\t\tt.Fatal(0)\n\t}\n}\n"
	return files
}

func corpusDeadcode() map[string]string {
	files := map[string]string{
		"go.mod": "module example.com/dc\n\ngo 1.22\n",
	}
	var keeps strings.Builder
	keeps.WriteString("package dc\n\nfunc SumKeep() int {\n\ts := 0\n")
	for i := 0; i < largeFiles; i++ {
		files[fmt.Sprintf("k%03d.go", i)] = fmt.Sprintf("package dc\n\nfunc Keep%03d() int { return %d }\nfunc Drop%03d() int { return %d }\n// %s\n", i, i, i, -i, pad(i, "dc"))
		keeps.WriteString(fmt.Sprintf("\ts += Keep%03d()\n", i))
	}
	keeps.WriteString("\treturn s\n}\n")
	files["sum.go"] = keeps.String()
	n := largeFiles * (largeFiles - 1) / 2
	files["sum_test.go"] = fmt.Sprintf("package dc\n\nimport \"testing\"\n\nfunc TestSumKeep(t *testing.T) {\n\tif SumKeep() != %d {\n\t\tt.Fatal(SumKeep())\n\t}\n}\n", n)
	return files
}
