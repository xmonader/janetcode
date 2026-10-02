package main

import (
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/xmonader/janetcode/internal/headless"
	"github.com/xmonader/janetcode/internal/session"
)

// answerQuestion shows the background run's open question, or answers
// it: one choice per question, separated by ";", each an option's number,
// its label, or free text. A run that already went on with the
// recommendation gets the answer as a late one.
func answerQuestion(w io.Writer, path, id string, q *session.BgQuestion, answer string) error {
	if strings.TrimSpace(answer) == "" {
		for _, it := range q.Items {
			fmt.Fprintf(w, "%s asks: %s\n", id, it.Prompt)
			for i, o := range it.Options {
				rec := ""
				if o == it.Recommended {
					rec = " (recommended)"
				}
				fmt.Fprintf(w, "  %d. %s%s\n", i+1, o, rec)
			}
		}
		switch {
		case q.Proceeded:
			fmt.Fprintf(w, "it went on with the recommendation; an answer now reaches the model as a late answer\n")
		case !q.Deadline.IsZero():
			if left := time.Until(q.Deadline).Round(time.Second); left > 0 {
				fmt.Fprintf(w, "if nobody answers in %s it goes on with the recommendation\n", left)
			}
		}
		more := ""
		if len(q.Items) > 1 {
			more = " (one per question, separated by ;)"
		}
		fmt.Fprintf(w, "answer with: janetcode --answer %s <number, label or your own words>%s\n", id, more)
		return nil
	}
	raw := strings.Split(answer, ";")
	if len(raw) > len(q.Items) {
		return headless.BadInput("%d answers for %d question(s): separate one answer per question with ;", len(raw), len(q.Items))
	}
	for i := range raw {
		raw[i] = strings.TrimSpace(raw[i])
	}
	if err := session.WriteBgReply(path, session.BgReply{ID: q.ID, Answers: raw}); err != nil {
		return err
	}
	for i, it := range q.Items {
		got := "(skipped)"
		if i < len(raw) && raw[i] != "" {
			got = resolveChoice(it, raw[i])
		}
		fmt.Fprintf(w, "%s: %s → %s\n", id, it.Prompt, got)
	}
	if q.Proceeded {
		fmt.Fprintln(w, "sent as a late answer: the model reads it before its next step")
	}
	return nil
}

// resolveChoice is what an answer will be read as, for the confirmation:
// the option a number or label names, or the text itself.
func resolveChoice(it session.BgQuestionItem, v string) string {
	if n, err := strconv.Atoi(v); err == nil && n >= 1 && n <= len(it.Options) {
		return it.Options[n-1]
	}
	for _, o := range it.Options {
		if strings.EqualFold(o, v) {
			return o
		}
	}
	return "Other: " + v
}
