package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/xmonader/janetcode/internal/tool"
)

// AskOption is one labeled choice.
type AskOption struct {
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
}

const (
	IntentPlanReview = "plan-review"
	// OtherLabel is the free-text choice every question offers in the TUI.
	OtherLabel = "Other"
)

// Question is a structured prompt for the human, not a free-text box.
type Question struct {
	ID      string      `json:"id,omitempty"`
	Prompt  string      `json:"prompt"`
	Detail  string      `json:"detail,omitempty"`
	Options []AskOption `json:"options"`
	Intent  string      `json:"intent,omitempty"` // IntentPlanReview or empty
	Approve string      `json:"approve,omitempty"`
}

// Answer is the chosen option label and optional free-text (Other, or a reason).
type Answer struct {
	Label  string `json:"label"`
	Reason string `json:"reason,omitempty"`
}

// Asker asks the user one or more labeled questions and blocks until they
// pick. Several questions in one call are one prompt with switchable tabs.
type Asker interface {
	Ask(ctx context.Context, qs []Question) ([]Answer, error)
}

// AskFunc adapts a function to Asker.
type AskFunc func(ctx context.Context, qs []Question) ([]Answer, error)

func (f AskFunc) Ask(ctx context.Context, qs []Question) ([]Answer, error) { return f(ctx, qs) }

// AskTool lets the model put a labeled question in front of the user.
type AskTool struct{ Parent *Agent }

func (*AskTool) Name() string { return "ask" }
func (*AskTool) Description() string {
	return "Ask the user one or more labeled questions when you cannot reasonably assume the answer: a goal, a trade-off, which of two approaches. Each question has a prompt and two to four options; the user can also type something else (Other). Several questions in one call are shown together as tabs. Do not use this for permission to edit or run a command — that is asked already. Do not use it to narrate; only when a choice changes what you do next."
}
func (*AskTool) Mutating() bool { return false }
func (*AskTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{
"questions":{"type":"array","minItems":1,"items":{"type":"object","properties":{
"prompt":{"type":"string","description":"The question, in a few words"},
"options":{"type":"array","minItems":2,"maxItems":4,"items":{"type":"object","properties":{
"label":{"type":"string"},
"description":{"type":"string"}},
"required":["label"],"additionalProperties":false}}},
"required":["prompt","options"],"additionalProperties":false}}},
"required":["questions"],"additionalProperties":false}`)
}

func (t *AskTool) Run(ctx context.Context, input json.RawMessage) (string, error) {
	var in struct {
		Questions []struct {
			Prompt  string      `json:"prompt"`
			Options []AskOption `json:"options"`
		} `json:"questions"`
	}
	if err := json.Unmarshal(input, &in); err != nil {
		return "", fmt.Errorf("bad input: %w", err)
	}
	if len(in.Questions) == 0 {
		return "", fmt.Errorf("ask needs at least one question")
	}
	qs := make([]Question, 0, len(in.Questions))
	for i, q := range in.Questions {
		prompt := strings.TrimSpace(q.Prompt)
		if prompt == "" {
			return "", fmt.Errorf("question %d has no prompt", i+1)
		}
		var opts []AskOption
		seen := map[string]bool{}
		for _, o := range q.Options {
			lab := strings.TrimSpace(o.Label)
			if lab == "" || strings.EqualFold(lab, OtherLabel) {
				continue
			}
			if seen[strings.ToLower(lab)] {
				continue
			}
			seen[strings.ToLower(lab)] = true
			opts = append(opts, AskOption{Label: lab, Description: strings.TrimSpace(o.Description)})
		}
		if len(opts) < 2 {
			return "", fmt.Errorf("question %d needs at least two options", i+1)
		}
		if len(opts) > 4 {
			opts = opts[:4]
		}
		qs = append(qs, Question{Prompt: prompt, Options: opts})
	}
	if t.Parent == nil || t.Parent.Asker == nil {
		return "", fmt.Errorf("nobody is there to answer: the session is not interactive")
	}
	ans, err := t.Parent.Asker.Ask(ctx, qs)
	if err != nil {
		return "", err
	}
	return FormatAnswers(qs, ans), nil
}

// FormatAnswers is what the model reads back: each prompt with the chosen
// label, and the typed text when the choice was Other.
func FormatAnswers(qs []Question, ans []Answer) string {
	var b strings.Builder
	for i, q := range qs {
		a := Answer{}
		if i < len(ans) {
			a = ans[i]
		}
		label := strings.TrimSpace(a.Label)
		if label == "" {
			label = "skipped"
		}
		fmt.Fprintf(&b, "Q: %s\nA: %s", q.Prompt, label)
		if strings.TrimSpace(a.Reason) != "" {
			fmt.Fprintf(&b, " — %s", strings.TrimSpace(a.Reason))
		}
		b.WriteByte('\n')
	}
	return strings.TrimRight(b.String(), "\n")
}

var _ tool.Tool = (*AskTool)(nil)
