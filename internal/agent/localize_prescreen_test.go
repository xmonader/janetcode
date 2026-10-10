package agent

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/change"
	"github.com/xmonader/janetcode/internal/diff"
	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/localize"
	"github.com/xmonader/janetcode/internal/session"
	"github.com/xmonader/janetcode/internal/shell"
	"github.com/xmonader/janetcode/internal/verify"
)

// TestLocalizePrescreen is the zero-token pre-screen for failure
// localization (localize.go): over recorded sessions, how many turns have
// a red test run that followed an edit set of two or more hunks — the
// only turns localization would act on — and how many tool calls those
// turns spent from that red run to the next green one.
//
// It reads directories of session logs, so it is skipped unless
// JANETCODE_LOCALIZE_PRESCREEN names them (a path list):
//
//	JANETCODE_LOCALIZE_PRESCREEN=docs/benchmarks/effbench/runs go test ./internal/agent -run LocalizePrescreen -v
//
// Hunks are counted by replaying the turn's write_file and edit_file
// calls over the repository the first message carried whole (effbench
// repositories are small enough to be inlined). An edit the replay cannot
// reproduce exactly — a fuzzy match, an anchor — makes its file count one
// hunk per edit call instead, and is reported. Files changed by shell
// commands are invisible to it, so the hunk counts are a lower bound.
func TestLocalizePrescreen(t *testing.T) {
	dir := os.Getenv("JANETCODE_LOCALIZE_PRESCREEN")
	if dir == "" {
		t.Skip("set JANETCODE_LOCALIZE_PRESCREEN to a directory of session logs")
	}
	var files []string
	for _, root := range filepath.SplitList(dir) {
		_ = filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
			if err == nil && !d.IsDir() && strings.HasSuffix(p, ".jsonl") && strings.Contains(p, "janetcode-sessions") {
				files = append(files, p)
			}
			return nil
		})
	}
	sort.Strings(files)
	var tot prescreenTotals
	for _, f := range files {
		s, _, err := session.Open(filepath.Dir(f), strings.TrimSuffix(filepath.Base(f), ".jsonl"))
		if err != nil {
			t.Logf("%s: %v", f, err)
			continue
		}
		tot.add(prescreenSession(s.Meta.Cwd, s.Events()), f)
	}
	t.Log(tot.String())
}

type prescreenTurn struct {
	edited      bool // the turn changed a file through a tool
	red         bool // a red check after an edit
	multiRed    bool // ... with two or more hunks in play
	goNamed     bool // ... whose failures the Go runner names
	anyNamed    bool // ... whose failures some runner names
	callsToNext int  // tool calls from the first multi-hunk red to the next green check
	greened     bool
	approx      bool // a file's hunks were counted by proxy
	hunks       int  // hunks in play at the first multi-hunk red
}

type prescreenTotals struct {
	sessions, turns, edited, red, multiRed, goNamed, anyNamed, greened, approx int
	calls                                                                      []int
	hunks                                                                      []int
	examples                                                                   []string
}

func (tt *prescreenTotals) add(turns []prescreenTurn, file string) {
	tt.sessions++
	for _, x := range turns {
		tt.turns++
		if x.edited {
			tt.edited++
		}
		if x.red {
			tt.red++
		}
		if !x.multiRed {
			continue
		}
		tt.multiRed++
		tt.hunks = append(tt.hunks, x.hunks)
		if x.goNamed {
			tt.goNamed++
		}
		if x.anyNamed {
			tt.anyNamed++
		}
		if x.approx {
			tt.approx++
		}
		if x.greened {
			tt.greened++
			tt.calls = append(tt.calls, x.callsToNext)
		}
		if len(tt.examples) < 8 {
			tt.examples = append(tt.examples, fmt.Sprintf("%s (%d hunks)", filepath.Base(filepath.Dir(filepath.Dir(file))), x.hunks))
		}
	}
}

func (tt prescreenTotals) String() string {
	pct := func(n, d int) string {
		if d == 0 {
			return "n/a"
		}
		return fmt.Sprintf("%.1f%%", 100*float64(n)/float64(d))
	}
	med := func(xs []int) string {
		if len(xs) == 0 {
			return "n/a"
		}
		c := append([]int(nil), xs...)
		sort.Ints(c)
		sum := 0
		for _, v := range c {
			sum += v
		}
		return fmt.Sprintf("median %d, mean %.1f, n=%d", c[len(c)/2], float64(sum)/float64(len(c)), len(c))
	}
	var b strings.Builder
	fmt.Fprintf(&b, "sessions %d, turns %d, turns that edited %d\n", tt.sessions, tt.turns, tt.edited)
	fmt.Fprintf(&b, "turns with a red check after an edit: %d (%s of turns)\n", tt.red, pct(tt.red, tt.turns))
	fmt.Fprintf(&b, "turns with a red check after a >=2-hunk edit set: %d (%s of turns, %s of editing turns); %d counted partly by proxy\n", tt.multiRed, pct(tt.multiRed, tt.turns), pct(tt.multiRed, tt.edited), tt.approx)
	fmt.Fprintf(&b, "  of those, failures the go test runner names: %d; that any known runner names: %d\n", tt.goNamed, tt.anyNamed)
	fmt.Fprintf(&b, "  hunks in play: %s\n", med(tt.hunks))
	fmt.Fprintf(&b, "  reached a later green check: %d; tool calls from that red to the next green: %s\n", tt.greened, med(tt.calls))
	fmt.Fprintf(&b, "  e.g. %s", strings.Join(tt.examples, ", "))
	return b.String()
}

var inlineFileRe = regexp.MustCompile(`(?s)<file path="([^"]+)">\n(.*?)</file>\n`)

// prescreenSession folds one session's events into per-turn facts.
func prescreenSession(cwd string, events []session.Event) []prescreenTurn {
	state := map[string]string{} // path -> content, as replayed
	known := map[string]bool{}   // the replay knows the path's content
	turnStart := map[string]string{}
	turnKnown := map[string]bool{}
	approxCalls := map[string]int{} // edit calls on a file the replay lost, this turn
	var turns []prescreenTurn
	cur := -1
	turnNo := 0
	pendingMulti := false
	calls := 0
	rel := func(p string) string {
		if filepath.IsAbs(p) && cwd != "" {
			if r, err := filepath.Rel(cwd, p); err == nil {
				return filepath.ToSlash(r)
			}
		}
		return filepath.ToSlash(filepath.Clean(p))
	}
	hunksNow := func() (n int, approx bool) {
		for p, before := range turnStart {
			if !turnKnown[p] || !known[p] {
				continue
			}
			n += len(change.MakeHunks(change.DiffLines(diff.SplitLinesKeepEOL(before), diff.SplitLinesKeepEOL(state[p]))))
		}
		for _, c := range approxCalls {
			n += c
			approx = true
		}
		return n, approx
	}
	touch := func(p string) {
		if _, ok := turnStart[p]; !ok {
			turnStart[p], turnKnown[p] = state[p], known[p]
		}
	}
	for _, e := range events {
		switch {
		case e.Type == session.TypeTurnStart:
			turns = append(turns, prescreenTurn{})
			cur = len(turns) - 1
			turnNo = e.Turn
			turnStart, turnKnown, approxCalls = map[string]string{}, map[string]bool{}, map[string]int{}
			pendingMulti, calls = false, 0
			continue
		case e.Type == session.TypeUserMessage && e.Message != nil:
			for _, m := range inlineFileRe.FindAllStringSubmatch(e.Message.Text(), -1) {
				if _, seen := known[m[1]]; !seen {
					state[m[1]], known[m[1]] = m[2], true
				}
			}
			continue
		}
		if cur < 0 || e.Turn != turnNo {
			continue
		}
		x := &turns[cur]
		check, red, green := false, false, false
		var output string
		switch {
		case e.Type == session.TypeGate:
			check, red, green, output = true, e.IsError, !e.IsError, e.Output
		case e.Type == session.TypeToolResult && e.CallID != "" && !refused(e):
			calls++
			if editTool(e.Name) && !e.IsError {
				prescreenApply(e, rel, state, known, touch, approxCalls)
				x.edited = true
				continue
			}
			if e.Name == "bash" && shell.IsCheck(shell.CommandOf(e.Arguments)) && !neverRan(e) {
				check, output = true, e.Output
				red = runFailed(e)
				green = !red
			}
		}
		if !check {
			continue
		}
		if red && x.edited {
			x.red = true
			if n, approx := hunksNow(); n >= localize.MinHunks && !x.multiRed {
				x.multiRed, x.hunks, x.approx = true, n, approx
				x.goNamed = len(localize.Go{}.Failures(output)) > 0
				names, _ := verify.ParseFailures(output)
				x.anyNamed = len(names) > 0
				pendingMulti, calls = true, 0
			}
		}
		if green && pendingMulti {
			x.greened, x.callsToNext = true, calls
			pendingMulti = false
		}
	}
	return turns
}

// prescreenApply replays one successful write_file or edit_file call.
func prescreenApply(e session.Event, rel func(string) string, state map[string]string, known map[string]bool, touch func(string), approx map[string]int) {
	var in struct {
		Path    string `json:"path"`
		Content string `json:"content"`
		oneEditArgs
		ReplaceAll bool          `json:"replace_all"`
		Edits      []oneEditArgs `json:"edits"`
	}
	if json.Unmarshal([]byte(e.Arguments), &in) != nil || in.Path == "" {
		return
	}
	p := rel(in.Path)
	touch(p)
	lose := func() {
		known[p] = false
		approx[p]++
	}
	if e.Name == "write_file" {
		state[p], known[p] = in.Content, true
		return
	}
	if e.Name != "edit_file" || !known[p] {
		lose()
		return
	}
	edits := in.Edits
	if in.Old != "" || in.byLine() {
		edits = append(edits, in.oneEditArgs)
	}
	s := state[p]
	for _, ed := range edits {
		switch {
		case ed.Old != "" && strings.Contains(s, ed.Old):
			if in.ReplaceAll {
				s = strings.ReplaceAll(s, ed.Old, ed.New)
			} else {
				s = strings.Replace(s, ed.Old, ed.New, 1)
			}
		case ed.Old == "" && ed.From > 0 && ed.To >= ed.From && ed.FromAnchor == "" && ed.ToAnchor == "":
			lines := diff.SplitLinesKeepEOL(s)
			if ed.To > len(lines) {
				lose()
				return
			}
			repl := ed.New
			if repl != "" && !strings.HasSuffix(repl, "\n") {
				repl += "\n"
			}
			s = strings.Join(lines[:ed.From-1], "") + repl + strings.Join(lines[ed.To:], "")
		default:
			lose()
			return
		}
	}
	state[p] = s
}

func TestPrescreenCountsAMultiHunkRedRun(t *testing.T) {
	repo := "<repository files=\"1\">\n<file path=\"a.go\">\npackage a\n\nfunc A() int { return 1 }\n\n\n\n\n\n\n\n\nfunc B() int { return 2 }\n</file>\n</repository>"
	edit := func(old, new string) string {
		b, _ := json.Marshal(map[string]string{"path": "a.go", "old_string": old, "new_string": new})
		return string(b)
	}
	bash := func(cmd string) string {
		b, _ := json.Marshal(map[string]string{"command": cmd})
		return string(b)
	}
	ev := []session.Event{
		{Type: session.TypeTurnStart, Turn: 1},
		{Type: session.TypeUserMessage, Turn: 1, Message: ptr(llm.TextMessage(llm.RoleUser, repo))},
		{Type: session.TypeToolResult, Turn: 1, CallID: "1", Name: "edit_file", Arguments: edit("return 1", "return 10")},
		{Type: session.TypeToolResult, Turn: 1, CallID: "2", Name: "edit_file", Arguments: edit("return 2", "return 20")},
		{Type: session.TypeToolResult, Turn: 1, CallID: "3", Name: "bash", Arguments: bash("go test ./..."), Output: "--- FAIL: TestA (0.00s)\nFAIL\texample.com/a\t0.01s\n[exit code 1]"},
		{Type: session.TypeToolResult, Turn: 1, CallID: "4", Name: "edit_file", Arguments: edit("return 10", "return 1")},
		{Type: session.TypeToolResult, Turn: 1, CallID: "5", Name: "bash", Arguments: bash("go test ./..."), Output: "ok  \texample.com/a\t0.01s\n[exit code 0]"},
	}
	turns := prescreenSession("", ev)
	if len(turns) != 1 || !turns[0].multiRed || turns[0].hunks != 2 || !turns[0].goNamed || !turns[0].greened || turns[0].callsToNext != 2 || turns[0].approx {
		t.Fatalf("turn = %+v", turns)
	}
	// One hunk in play: the run is red but localization would not act.
	one := append(append([]session.Event(nil), ev[:3]...), ev[4])
	if turns := prescreenSession("", one); !turns[0].red || turns[0].multiRed {
		t.Fatalf("one hunk: %+v", turns)
	}
}

func ptr[T any](v T) *T { return &v }
