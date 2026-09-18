package agent

import (
	"strings"
	"testing"
)

// A recorded plan-mode session ended its plan with ask "Ready to
// implement? / Go ahead", which is a trap: answering it cannot start
// anything, because only the user's own /plan approve leaves plan mode.
// The next turn tried write_file and was refused. The prompt has to say
// where approval comes from.
func TestPlanModeSaysWhereApprovalComesFrom(t *testing.T) {
	if !strings.Contains(planSystem, "/plan approve") {
		t.Fatalf("the plan prompt never names the command that approves:\n%s", planSystem)
	}
	if !strings.Contains(planSystem, "ask") {
		t.Fatalf("the plan prompt does not warn against asking for approval itself:\n%s", planSystem)
	}
}
