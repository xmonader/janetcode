package mcptool

import (
	"context"
	"fmt"
	"strings"

	"github.com/xmonader/janetcode/internal/agent"
	"github.com/xmonader/janetcode/internal/mcp"
)

// Labels of the two ways out the form offers on every question.
const (
	DeclineLabel = "Decline request"
	CancelLabel  = "Cancel request"
	yesLabel     = "Yes"
	noLabel      = "No"
	leaveEmpty   = "(leave empty)"
)

// ElicitViaAsker answers a server's elicitation/create through the
// session's question UI (the TUI overlay, the web question card, an ACP
// permission prompt), so every front-end shows the form the way it shows
// the ask tool. Each schema field becomes one question; a typed value is
// the question's "Other" answer. Values are validated against the schema
// and a bad one is asked again (twice at most) with the reason. asker is
// read per request, since a front-end installs its Asker after the
// servers are configured.
func ElicitViaAsker(asker func() agent.Asker) func(ctx context.Context, req mcp.ElicitRequest) mcp.ElicitResponse {
	return func(ctx context.Context, req mcp.ElicitRequest) mcp.ElicitResponse {
		a := asker()
		if a == nil {
			return mcp.ElicitResponse{Action: mcp.ElicitDecline}
		}
		fields, err := mcp.ParseForm(req.Schema)
		if err != nil {
			return mcp.ElicitResponse{Action: mcp.ElicitCancel}
		}
		return RunForm(ctx, a, req.Server, req.Message, fields)
	}
}

// RunForm asks fields one question each and returns the accepted form, or
// decline/cancel (also on ctx ending: the timeout).
func RunForm(ctx context.Context, a agent.Asker, server, message string, fields []mcp.Field) mcp.ElicitResponse {
	content := map[string]any{}
	if len(fields) == 0 {
		// A form with nothing to fill in is a plain confirmation.
		qs := []agent.Question{{Prompt: server + " asks: " + message, Intent: "elicitation", Options: []agent.AskOption{{Label: "Accept"}, {Label: DeclineLabel}, {Label: CancelLabel}}}}
		ans, err := a.Ask(ctx, qs)
		if err != nil || len(ans) == 0 {
			return mcp.ElicitResponse{Action: mcp.ElicitCancel}
		}
		return outcome(ans[0], mcp.ElicitResponse{Action: mcp.ElicitAccept, Content: content})
	}
	pending := fields
	errs := map[string]string{}
	for attempt := 0; attempt < 3 && len(pending) > 0; attempt++ {
		qs := make([]agent.Question, len(pending))
		for i, f := range pending {
			qs[i] = Question(server, message, f, errs[f.Name])
		}
		ans, err := a.Ask(ctx, qs)
		if err != nil || len(ans) != len(qs) {
			return mcp.ElicitResponse{Action: mcp.ElicitCancel}
		}
		var next []mcp.Field
		errs = map[string]string{}
		for i, f := range pending {
			if r := outcome(ans[i], mcp.ElicitResponse{}); r.Action != "" {
				return r
			}
			v, present, verr := f.Validate(AnswerText(ans[i], f))
			switch {
			case verr != nil:
				errs[f.Name] = verr.Error()
				next = append(next, f)
			case present:
				content[f.Name] = v
			}
		}
		pending = next
	}
	if len(pending) > 0 {
		return mcp.ElicitResponse{Action: mcp.ElicitCancel}
	}
	return mcp.ElicitResponse{Action: mcp.ElicitAccept, Content: content}
}

// outcome maps the two exit options (and a dismissed card) to their
// protocol action; def is returned for any other answer.
func outcome(a agent.Answer, def mcp.ElicitResponse) mcp.ElicitResponse {
	switch a.Label {
	case DeclineLabel:
		return mcp.ElicitResponse{Action: mcp.ElicitDecline}
	case CancelLabel, "skipped":
		return mcp.ElicitResponse{Action: mcp.ElicitCancel}
	}
	return def
}

// AnswerText is what the user entered for f: the typed text of an Other
// answer, else the chosen option's label.
func AnswerText(a agent.Answer, f mcp.Field) string {
	if a.Label == agent.OtherLabel {
		return a.Reason
	}
	if a.Label == leaveEmpty {
		return ""
	}
	return a.Label
}

// Question renders one field as a question for the ask UI.
func Question(server, message string, f mcp.Field, problem string) agent.Question {
	q := agent.Question{ID: f.Name, Intent: "elicitation"}
	q.Prompt = f.Title
	if f.Required {
		q.Prompt += " (required)"
	}
	var detail []string
	if message != "" {
		detail = append(detail, server+": "+message)
	}
	if f.Description != "" {
		detail = append(detail, f.Description)
	}
	switch f.Kind {
	case mcp.KindSelect:
		for _, o := range f.Options {
			q.Options = append(q.Options, agent.AskOption{Label: o.Label, Description: descIfDiffers(o)})
		}
	case mcp.KindBoolean:
		q.Options = []agent.AskOption{{Label: yesLabel}, {Label: noLabel}}
	case mcp.KindMulti:
		var labels []string
		for _, o := range f.Options {
			labels = append(labels, o.Label)
		}
		detail = append(detail, "choose any, comma-separated, with Other: "+strings.Join(labels, ", "))
	case mcp.KindString, mcp.KindNumber, mcp.KindInteger:
		if f.Default != "" {
			q.Options = []agent.AskOption{{Label: f.Default, Description: "the default"}}
		}
		if hint := fieldHint(f); hint != "" {
			detail = append(detail, hint)
		}
		detail = append(detail, "choose Other and type the value")
	}
	if !f.Required {
		q.Options = append(q.Options, agent.AskOption{Label: leaveEmpty, Description: "optional"})
	}
	if problem != "" {
		detail = append(detail, "Not accepted: "+problem)
	}
	q.Detail = strings.Join(detail, "\n")
	q.Options = append(q.Options, agent.AskOption{Label: DeclineLabel, Description: "send nothing, refuse"}, agent.AskOption{Label: CancelLabel, Description: "dismiss without answering"})
	return q
}

func descIfDiffers(o mcp.Option) string {
	if o.Value != o.Label {
		return o.Value
	}
	return ""
}

func fieldHint(f mcp.Field) string {
	var h []string
	switch f.Kind {
	case mcp.KindInteger:
		h = append(h, "an integer")
	case mcp.KindNumber:
		h = append(h, "a number")
	}
	if f.Format != "" {
		h = append(h, f.Format)
	}
	if f.Min != nil {
		h = append(h, fmt.Sprintf("min %g", *f.Min))
	}
	if f.Max != nil {
		h = append(h, fmt.Sprintf("max %g", *f.Max))
	}
	if f.MinLength != nil {
		h = append(h, fmt.Sprintf("at least %d characters", *f.MinLength))
	}
	if f.MaxLength != nil {
		h = append(h, fmt.Sprintf("at most %d characters", *f.MaxLength))
	}
	return strings.Join(h, ", ")
}
