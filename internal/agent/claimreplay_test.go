package agent

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/session"
)

// The offline pre-screen for claim-checked handoffs: replay recorded
// sessions, take the facts of every working-memory record written at a
// compaction, and check them against the calls logged before it. No
// model is called. Without the workspace there is no disk to read, so
// definition claims are judged from the calls' outputs alone and a
// missing file can only make a claim unverified, never contradicted.
//
//	JANETCODE_CLAIM_REPLAY='docs/benchmarks/**/*.json,~/.local/share/janetcode/sessions/*.jsonl' \
//	  go test ./internal/agent -run TestClaimReplay -v
//
// A .json file is an eval result (results[].events); a .jsonl file is a
// session log. Identical records (the same run in two result files) are
// counted once.
func TestClaimReplay(t *testing.T) {
	spec := os.Getenv("JANETCODE_CLAIM_REPLAY")
	if spec == "" {
		t.Skip("set JANETCODE_CLAIM_REPLAY to replay recorded sessions")
	}
	var files []string
	for _, pat := range strings.Split(spec, ",") {
		if strings.HasPrefix(pat, "~/") {
			home, _ := os.UserHomeDir()
			pat = filepath.Join(home, pat[2:])
		}
		files = append(files, globAll(pat)...)
	}
	var total claimReplayTally
	seen := map[string]bool{}
	for _, f := range files {
		for _, events := range replayLogs(t, f) {
			total.add(replayClaims(events, seen))
		}
	}
	t.Logf("%d files, %d records, %d facts: %d checkable — %d backed, %d unverified, %d contradicted (unverified+contradicted = %.0f%% of facts, %.0f%% of checkable)",
		len(files), total.records, total.facts, total.backed+total.unbacked+total.dropped, total.backed, total.unbacked, total.dropped,
		share(total.unbacked+total.dropped, total.facts), share(total.unbacked+total.dropped, total.backed+total.unbacked+total.dropped))
	for _, ex := range total.examples {
		t.Log("  " + ex)
	}
}

type claimReplayTally struct {
	records, facts, backed, unbacked, dropped int
	examples                                  []string
}

func (t *claimReplayTally) add(o claimReplayTally) {
	t.records += o.records
	t.facts += o.facts
	t.backed += o.backed
	t.unbacked += o.unbacked
	t.dropped += o.dropped
	for _, ex := range o.examples {
		if len(t.examples) < 25 {
			t.examples = append(t.examples, ex)
		}
	}
}

func share(a, b int) float64 {
	if b == 0 {
		return 0
	}
	return 100 * float64(a) / float64(b)
}

// replayClaims checks the facts of each compaction record in one log
// against the surface messages logged before it.
func replayClaims(events []session.Event, seen map[string]bool) claimReplayTally {
	var out claimReplayTally
	var msgs []llm.Message
	afterSummary := false
	for _, e := range events {
		switch {
		case e.Type == session.TypeCompactSummary:
			afterSummary = true
			continue
		case e.Message == nil:
			continue
		}
		text := e.Message.Text()
		if afterSummary && e.Type == session.TypeUserMessage && strings.Contains(text, workingMarker) {
			afterSummary = false
			if seen[text] {
				continue
			}
			seen[text] = true
			facts := factsOf(text)
			out.records++
			out.facts += len(facts)
			c := &claimChecker{proofs: gatherProofs(msgs, "")}
			for _, f := range facts {
				switch k, _ := c.check(f); k {
				case claimBacked:
					out.backed++
				case claimUnbacked:
					out.unbacked++
					out.examples = append(out.examples, "unverified: "+prefix(f, 140))
				case claimContradicted:
					out.dropped++
					out.examples = append(out.examples, "contradicted: "+prefix(f, 140))
				}
			}
			continue
		}
		if e.Type == session.TypeAssistant || e.Type == session.TypeToolResult {
			msgs = append(msgs, *e.Message)
		}
	}
	return out
}

// factsOf is the "Facts about the codebase" section of a rendered record.
func factsOf(note string) []string {
	i := strings.Index(note, "\nFacts about the codebase:\n")
	if i < 0 {
		return nil
	}
	var out []string
	for _, line := range strings.Split(note[i+len("\nFacts about the codebase:\n"):], "\n") {
		if !strings.HasPrefix(line, "- ") {
			break
		}
		out = append(out, strings.TrimPrefix(line, "- "))
	}
	return out
}

// replayLogs reads the event logs in one file.
func replayLogs(t *testing.T, path string) [][]session.Event {
	if strings.HasSuffix(path, ".jsonl") {
		f, err := os.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		var events []session.Event
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 1<<20), 64<<20)
		for sc.Scan() {
			var e session.Event
			if json.Unmarshal(sc.Bytes(), &e) == nil {
				events = append(events, e)
			}
		}
		return [][]session.Event{events}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var res struct {
		Results []struct {
			Events []session.Event `json:"events"`
		} `json:"results"`
	}
	if json.Unmarshal(data, &res) != nil {
		return nil
	}
	var out [][]session.Event
	for _, r := range res.Results {
		out = append(out, r.Events)
	}
	return out
}

// globAll is filepath.Glob with "**" matching any depth.
func globAll(pat string) []string {
	if !strings.Contains(pat, "**") {
		m, _ := filepath.Glob(pat)
		return m
	}
	root, rest, _ := strings.Cut(pat, "**")
	rest = strings.TrimPrefix(rest, string(filepath.Separator))
	var out []string
	filepath.WalkDir(filepath.Clean(root), func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			if ok, _ := filepath.Match(rest, filepath.Base(p)); ok {
				out = append(out, p)
			}
		}
		return nil
	})
	return out
}

// The replay reads the facts back out of a rendered record and checks
// them against the calls logged before the compaction.
func TestClaimReplayReadsRecordsFromALog(t *testing.T) {
	w := &WorkingMemory{Goal: "g", Facts: []string{"tests pass", "a.go holds main", "the parser is small"}}
	use := llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{{Type: llm.BlockToolUse, ID: "c1", Name: "bash", Input: json.RawMessage(`{"command":"go test ./..."}`)}}}
	res := llm.ToolResultMessage(llm.ToolResult("c1", "FAIL\n[exit code 1]", false))
	note := llm.TextMessage(llm.RoleUser, w.Render())
	events := []session.Event{
		{Type: session.TypeAssistant, Message: &use},
		{Type: session.TypeToolResult, Message: &res},
		{Type: session.TypeCompactSummary},
		{Type: session.TypeUserMessage, Message: &note},
	}
	seen := map[string]bool{}
	got := replayClaims(events, seen)
	if got.records != 1 || got.facts != 3 || got.dropped != 1 || got.unbacked != 1 || got.backed != 0 {
		t.Fatalf("tally = %+v", got)
	}
	if again := replayClaims(events, seen); again.records != 0 {
		t.Fatalf("the same record counted twice: %+v", again)
	}
}
