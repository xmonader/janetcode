package memory

import (
	"strconv"
	"strings"
	"testing"
)

func TestWhyNamesTheWordsAndWhereTheyMatched(t *testing.T) {
	s := &Store{Root: t.TempDir(), Key: "proj"}
	e := Entry{Name: "deploy-flow", Description: "deploys go through make release", Body: "tag first, then run the pipeline", Concerns: []string{"scripts/release.sh"}}
	q := "how do I deploy with the pipeline using release.sh"
	got := s.Why(q, e)
	for _, want := range []string{"name/description:", "deploy", "body:", "pipeline", "score"} {
		if !strings.Contains(got, want) {
			t.Errorf("Why = %q, missing %q", got, want)
		}
	}
	// The reported score is the one Recall ranks by.
	have := map[string]bool{}
	for _, w := range s.words(q) {
		have[w] = true
	}
	score, _ := scoreEntry(e, have)
	if !strings.Contains(got, "score "+strconv.Itoa(score)) {
		t.Errorf("Why = %q, score %d not reported", got, score)
	}
}

func TestWhyWithoutSharedWordsIsSemantic(t *testing.T) {
	s := &Store{Root: t.TempDir(), Key: "proj"}
	got := s.Why("kubernetes rollout", Entry{Name: "tabs", Description: "prefers tabs", Body: "indent with tabs"})
	if !strings.Contains(got, "meaning") {
		t.Errorf("Why = %q, want the semantic reason", got)
	}
}
