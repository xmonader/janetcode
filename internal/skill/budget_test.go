package skill

import (
	"fmt"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/memory"
)

// TestSkillIndexStopsAtItsBudget: the index listed every model-invocable
// skill with no ceiling at all, so a config directory with a hundred of
// them put a hundred lines in every request. What does not fit says so,
// and the skill tool still finds it by name.
func TestSkillIndexStopsAtItsBudget(t *testing.T) {
	var skills []Skill
	for i := 0; i < 100; i++ {
		skills = append(skills, Skill{
			Name:           fmt.Sprintf("skill-number-%03d", i),
			Description:    strings.Repeat("what this one is for, at some length. ", 3),
			ModelInvocable: true,
		})
	}
	full := IndexWithin(skills, 0)
	if len(full) > memory.IndexBudget+200 {
		t.Fatalf("the default index is %d bytes, over the %d-byte ceiling", len(full), memory.IndexBudget)
	}
	small := IndexWithin(skills, 1024)
	if len(small) > 1024+120 {
		t.Fatalf("the index is %d bytes for a budget of 1024", len(small))
	}
	if !strings.Contains(small, "more; the skill tool finds them by name") {
		t.Fatalf("the index does not say what it left out:\n%s", small)
	}
	// A list that fits says nothing about leftovers.
	if got := IndexWithin(skills[:3], memory.IndexBudget); strings.Contains(got, "more;") {
		t.Fatalf("a complete list claimed to be partial:\n%s", got)
	}
}
