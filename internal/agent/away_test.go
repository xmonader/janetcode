package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/perm"
	"github.com/xmonader/janetcode/internal/tool"
)

func TestAwayRestoresThePreviousModeExactly(t *testing.T) {
	for _, start := range []Settings{{}, {AutoEdits: true}, {AutoMode: true}, {AutoApprove: true}} {
		a := &Agent{}
		a.UpdateSettings(func(s *Settings) {
			s.AutoApprove, s.AutoMode, s.AutoEdits = start.AutoApprove, start.AutoMode, start.AutoEdits
		})
		if _, err := a.SetAway(true); err != nil {
			t.Fatal(err)
		}
		if s := a.Settings(); !a.Away() || !s.AutoApprove || s.AutoMode || s.AutoEdits {
			t.Fatalf("away settings = %+v", s)
		}
		if _, err := a.SetAway(false); err != nil {
			t.Fatal(err)
		}
		if s := a.Settings(); a.Away() || s.AutoApprove != start.AutoApprove || s.AutoMode != start.AutoMode || s.AutoEdits != start.AutoEdits {
			t.Fatalf("restored %+v, want %+v", s, start)
		}
	}
}

func TestAskWhileAwayReturnsTheAwayTextAndRecordsIt(t *testing.T) {
	ag := &Agent{}
	ag.Asker = AskFunc(func(context.Context, []Question) ([]Answer, error) {
		t.Fatal("the user was asked while away")
		return nil, nil
	})
	if _, err := ag.SetAway(true); err != nil {
		t.Fatal(err)
	}
	out, err := (&AskTool{Parent: ag}).Run(context.Background(), json.RawMessage(`{"questions":[
		{"prompt":"Goal?","recommended":"Speed","options":[{"label":"Speed"},{"label":"Clarity"}]}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(out, "The user is away. State the assumption you are making") {
		t.Fatalf("out = %q", out)
	}
	if ag.AwayPending() != 1 {
		t.Fatalf("pending = %d", ag.AwayPending())
	}
	notes, err := ag.SetAway(false)
	if err != nil || len(notes) != 1 || notes[0].Question != "Goal?" || notes[0].Assumed != "Speed" {
		t.Fatalf("notes = %+v, %v", notes, err)
	}
	if got := FormatAwayNotes(notes); !strings.Contains(got, "While you were away: 1 question answered with assumptions") || !strings.Contains(got, "Goal?") {
		t.Fatalf("list = %q", got)
	}
	if ag.AwayPending() != 0 {
		t.Fatal("notes survived being shown")
	}
}

func TestAwayApprovesWithoutAskingButDenyRulesHold(t *testing.T) {
	f := &fake{responses: []*llm.Response{
		toolCall("t1", "write_file", `{"path":"w.txt","content":"x"}`),
		toolCall("t2", "bash", `{"command":"git push origin"}`),
		text("done"),
	}}
	ap := &seesApprover{decision: Deny}
	a, root := newAgent(t, f, ap)
	a.Tools = tool.NewRegistry(tool.Bash{Root: root}, tool.WriteFile{Root: root})
	r, _ := perm.Parse("bash(git push*)")
	a.Rules = perm.Rules{Deny: []perm.Rule{r}}
	if _, err := a.SetAway(true); err != nil {
		t.Fatal(err)
	}
	if err := a.Run(context.Background(), "go", nil); err != nil {
		t.Fatal(err)
	}
	if len(ap.inputs) != 0 {
		t.Fatalf("approver was asked: %q", ap.inputs)
	}
	if _, err := os.Stat(filepath.Join(root, "w.txt")); err != nil {
		t.Fatalf("the approved write did not happen: %v", err)
	}
	if res := a.Messages[4].Blocks[0]; !res.IsError || !strings.Contains(res.Text, "Denied by permission rule") {
		t.Fatalf("deny rule result = %+v", res)
	}
}
