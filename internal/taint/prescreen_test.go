package taint

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The offline pre-screen for the kill criterion "more than one extra
// approval prompt per clean run": every recorded session under
// docs/benchmarks is replayed through the labels and the gate, in order,
// with no model and no network. A run turns tainted at its first
// untrusted result; from then on each call the gate would stop is one
// extra prompt (an approved host counts once). The numbers are logged
// (go test -run Prescreen -v); the test fails only if the recorded
// sessions would average more than one extra prompt a run.

type recorded struct {
	Results []struct {
		Name   string `json:"name"`
		Events []struct {
			T         string `json:"t"`
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
			Output    string `json:"output"`
		} `json:"events"`
	} `json:"results"`
}

type screen struct {
	files, runs, tainted, prompts, egressCmds, sensitiveEdits int
	sources                                                   map[string]int
	examples                                                  []string
}

func (s *screen) replay(r *recorded) {
	for _, run := range r.Results {
		if len(run.Events) == 0 {
			continue
		}
		s.runs++
		var tr Tracker
		approved := map[string]bool{}
		for _, e := range run.Events {
			if e.T != "tool/result" {
				continue
			}
			input := json.RawMessage(e.Arguments)
			gated := false
			switch e.Name {
			case "bash":
				var in struct{ Command string }
				_ = json.Unmarshal(input, &in)
				if _, ok := EgressCommand(in.Command); ok {
					s.egressCmds++
					s.examples = append(s.examples, in.Command)
					gated = true
				}
			case "write_file", "edit_file":
				var in struct{ Path string }
				_ = json.Unmarshal(input, &in)
				if SensitivePath(in.Path) != "" {
					s.sensitiveEdits++
					gated = true
				}
			case "fetch_url", "web_search":
				host := e.Name
				if !approved[host] {
					approved[host] = true
					gated = true
				}
			}
			if gated && tr.Tainted() {
				s.prompts++
			}
			if src, ok := Untrusted(e.Name, input, e.Output); ok {
				if !tr.Tainted() {
					s.tainted++
				}
				tr.Add(src, e.Output)
				s.sources[e.Name]++
			}
		}
	}
}

func TestPrescreenRecordedSessions(t *testing.T) {
	root := filepath.Join("..", "..", "docs", "benchmarks")
	if _, err := os.Stat(root); err != nil {
		t.Skip("no recorded sessions in this checkout")
	}
	s := &screen{sources: map[string]int{}}
	_ = filepath.Walk(root, func(p string, fi os.FileInfo, err error) error {
		if err != nil || fi.IsDir() || !strings.HasSuffix(p, ".json") {
			return nil
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return nil
		}
		var r recorded
		if json.Unmarshal(data, &r) != nil || len(r.Results) == 0 {
			return nil
		}
		s.files++
		s.replay(&r)
		return nil
	})
	if s.runs == 0 {
		t.Skip("no recorded runs with events")
	}
	t.Logf("pre-screen: %d files, %d runs; %d runs turned tainted (by %v); %d extra prompts in all (%.3f a run); "+
		"untainted, the gate would have matched %d network commands and %d CI/credential edits",
		s.files, s.runs, s.tainted, s.sources, s.prompts, float64(s.prompts)/float64(s.runs), s.egressCmds, s.sensitiveEdits)
	for _, c := range s.examples {
		t.Logf("  network command: %.160s", c)
	}
	if float64(s.prompts)/float64(s.runs) > 1 {
		t.Fatalf("recorded sessions would average %.2f extra prompts a run (kill criterion: > 1)", float64(s.prompts)/float64(s.runs))
	}
}
