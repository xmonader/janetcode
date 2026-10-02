package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

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
	// Recommended is the option label the model would pick itself, which
	// the turn goes on with when nobody answers in time (see askwait.go).
	// Empty when the model gave none; such a question is never answered
	// on the user's behalf.
	Recommended string `json:"recommended,omitempty"`
	// Deadline is when the turn stops waiting and goes on with
	// Recommended; zero while it waits as long as it takes. A front-end
	// shows the countdown from it.
	Deadline time.Time `json:"deadline,omitzero"`
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

// askShape is appended to every rejection: a tool error that says only
// what is wrong gets the same call again, one that shows the right call
// gets a fixed one.
const askShape = `Every entry in "questions" is one question with its own choices nested inside it: ` +
	`{"questions":[{"prompt":"Ready to implement?","options":[{"label":"Go ahead","description":"..."},{"label":"Tweak something","description":"..."}]}]}`

// AskTool lets the model put a labeled question in front of the user.
type AskTool struct{ Parent *Agent }

func (*AskTool) Name() string { return "ask" }
func (*AskTool) Description() string {
	return "Ask the user one or more labeled questions when you cannot reasonably assume the answer: a goal, a trade-off, which of two approaches. Each question has a prompt and two to four options; the user can also type something else (Other). Several questions in one call are shown together as tabs. " +
		"Give each question a recommended option (its exact label) whenever one is a sensible default: if nobody answers in time — an unattended run, a goal round — the turn goes on with it, and a later answer still reaches you. Leave it out only when guessing would do harm. " +
		"Do not use this for permission to edit or run a command — that is asked already. Do not use it to narrate; only when a choice changes what you do next."
}
func (*AskTool) Mutating() bool { return false }
func (*AskTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{
"questions":{"type":"array","minItems":1,"items":{"type":"object","properties":{
"prompt":{"type":"string","description":"The question, in a few words"},
"recommended":{"type":"string","description":"the label of the option you would pick yourself; used if nobody answers in time"},
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
			Prompt string `json:"prompt"`
			// Label is not part of the schema; it is here to recognise the
			// call that puts the options themselves in "questions", so the
			// error can show the shape instead of only what is missing.
			Label       string      `json:"label"`
			Options     []AskOption `json:"options"`
			Recommended string      `json:"recommended"`
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
			if strings.TrimSpace(q.Label) != "" {
				return "", fmt.Errorf("question %d is an option, not a question: %q belongs in that question's \"options\". %s",
					i+1, strings.TrimSpace(q.Label), askShape)
			}
			return "", fmt.Errorf("question %d has no prompt. %s", i+1, askShape)
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
			return "", fmt.Errorf("question %d needs at least two options. %s", i+1, askShape)
		}
		if len(opts) > 4 {
			opts = opts[:4]
		}
		rec, err := matchRecommended(opts, q.Recommended)
		if err != nil {
			return "", fmt.Errorf("question %d: %v", i+1, err)
		}
		qs = append(qs, Question{Prompt: prompt, Options: opts, Recommended: rec})
	}
	_, canProceed := recommended(qs)
	if t.Parent == nil || t.Parent.Asker == nil {
		if canProceed {
			return formatProceeded(qs, "Nobody can answer in this session (it is not interactive).", false), nil
		}
		return "", fmt.Errorf("nobody is there to answer: the session is not interactive. Decide yourself and say what you assumed, or give each question a recommended option")
	}
	wait, _ := t.Parent.AskWaitFor()
	asked := time.Now()
	var (
		ans       []Answer
		proceeded bool
		err       error
	)
	switch {
	case wait == AskWaitForever || !canProceed:
		// Without a recommendation there is nothing to go on with, so
		// the question waits as it always has: answering for the user
		// would be inventing their choice.
		ans, err = t.Parent.Asker.Ask(ctx, qs)
	default:
		for i := range qs {
			qs[i].Deadline = asked.Add(wait)
		}
		ans, proceeded, err = t.Parent.askBounded(ctx, t.Parent.Asker, qs, wait)
	}
	t.Parent.waited(ctx, time.Since(asked))
	if err != nil {
		return "", err
	}
	if proceeded {
		why := fmt.Sprintf("No answer within %s.", wait.Round(time.Second))
		if wait == 0 {
			why = "This turn does not wait for answers (it is unattended)."
		}
		return formatProceeded(qs, why, true), nil
	}
	return FormatAnswers(qs, ans), nil
}

// matchRecommended resolves the model's recommended choice to one of the
// option labels, case-insensitively, so the turn never goes on with a
// choice the user was not offered. Empty stays empty.
func matchRecommended(opts []AskOption, rec string) (string, error) {
	rec = strings.TrimSpace(rec)
	if rec == "" {
		return "", nil
	}
	for _, o := range opts {
		if strings.EqualFold(o.Label, rec) {
			return o.Label, nil
		}
	}
	labels := make([]string, len(opts))
	for i, o := range opts {
		labels[i] = fmt.Sprintf("%q", o.Label)
	}
	return "", fmt.Errorf("recommended %q is not one of its options (%s); use an option's exact label", rec, strings.Join(labels, ", "))
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
