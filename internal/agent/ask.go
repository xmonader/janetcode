package agent

import "context"

// AskOption is one labeled choice.
type AskOption struct {
	Label       string
	Description string
}

const (
	IntentPlanReview = "plan-review"
)

// Question is a structured prompt for the human, not a free-text box.
type Question struct {
	ID      string
	Prompt  string
	Detail  string
	Options []AskOption
	Intent  string // IntentPlanReview or empty
	Approve string // option label that means yes, when Intent is plan-review
}

// Answer is the chosen option label and optional reason.
type Answer struct {
	Label  string
	Reason string
}

// Asker asks the user a labeled question and blocks until they pick.
type Asker interface {
	Ask(ctx context.Context, q Question) (Answer, error)
}

// AskFunc adapts a function to Asker.
type AskFunc func(ctx context.Context, q Question) (Answer, error)

func (f AskFunc) Ask(ctx context.Context, q Question) (Answer, error) { return f(ctx, q) }
