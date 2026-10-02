package headless

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/xmonader/janetcode/internal/agent"
	"github.com/xmonader/janetcode/internal/session"
)

// BgAsker is the asker of a background run: the model's question goes in
// the status sidecar, JANETCODE_NOTIFY is run, and the answer comes from
// janetcode --answer <id> <choice>. The ask tool decides how long the
// turn waits (agent.AskWaitFor, unattended); when it goes on with the
// recommendation the question stays in the sidecar, marked proceeded,
// and an answer given then is delivered to the model as a late one.
type BgAsker struct {
	Inbox *InboxApprover // the run's status, its lock and its notifier
	Poll  time.Duration

	mu sync.Mutex
	n  int
}

// Ask waits for janetcode --answer to answer the question, or for ctx.
func (b *BgAsker) Ask(ctx context.Context, qs []agent.Question) ([]agent.Answer, error) {
	b.mu.Lock()
	b.n++
	n := b.n
	b.mu.Unlock()
	a := b.Inbox
	q := &session.BgQuestion{
		ID:    fmt.Sprintf("q%d-%d", n, time.Now().UnixNano()%1e6),
		Asked: time.Now(),
	}
	for _, x := range qs {
		item := session.BgQuestionItem{Prompt: x.Prompt, Recommended: x.Recommended}
		for _, o := range x.Options {
			item.Options = append(item.Options, o.Label)
		}
		q.Items = append(q.Items, item)
		if !x.Deadline.IsZero() {
			q.Deadline = x.Deadline
		}
	}
	if err := a.Update(func(st *session.BgStatus) { st.State, st.Question = session.BgWaiting, q }); err != nil {
		return nil, fmt.Errorf("the question could not be recorded for an answer: %w", err)
	}
	answer := "janetcode --answer " + a.SessionID
	fmt.Fprintf(a.Log, "[question] waiting: %s — answer with: %s <option number or label>\n", qs[0].Prompt, answer)
	a.notify(ctx, "question: "+qs[0].Prompt, answer+" <choice>")

	poll := b.Poll
	if poll <= 0 {
		poll = 500 * time.Millisecond
	}
	t := time.NewTicker(poll)
	defer t.Stop()
	clear := func() {
		_ = a.Update(func(st *session.BgStatus) {
			if st.Question != nil && st.Question.ID == q.ID {
				st.Question = nil
				if st.State == session.BgWaiting && st.Ask == nil {
					st.State = session.BgRunning
				}
			}
		})
	}
	for {
		if raw, ok := session.TakeBgReply(a.SessionPath, q.ID); ok {
			clear()
			ans := ParseBgAnswers(qs, raw)
			fmt.Fprintf(a.Log, "[question] answered: %s\n", strings.ReplaceAll(agent.FormatAnswers(qs, ans), "\n", " · "))
			return ans, nil
		}
		select {
		case <-ctx.Done():
			clear()
			return nil, ctx.Err()
		case <-t.C:
		}
	}
}

// AskTimedOut is agent.AskTimeoutNotifier: the run went on with the
// recommendation, so it is running again, and the question stays in the
// sidecar marked proceeded for a late answer.
func (b *BgAsker) AskTimedOut(qs []agent.Question, _ []agent.Answer) {
	a := b.Inbox
	_ = a.Update(func(st *session.BgStatus) {
		if st.Question != nil {
			st.Question.Proceeded = true
		}
		if st.State == session.BgWaiting && st.Ask == nil {
			st.State = session.BgRunning
		}
	})
	if len(qs) > 0 {
		fmt.Fprintf(a.Log, "[question] no answer in time: went on with %q; a late answer is still taken\n", qs[0].Recommended)
	}
}

// ParseBgAnswers reads --answer's choices, one per question: an option's
// number (1-based) or label, matched case-insensitively, else free text
// taken as an "Other" answer. A question with no choice given is skipped.
func ParseBgAnswers(qs []agent.Question, raw []string) []agent.Answer {
	out := make([]agent.Answer, len(qs))
	for i, q := range qs {
		if i >= len(raw) || strings.TrimSpace(raw[i]) == "" {
			out[i] = agent.Answer{Label: "skipped"}
			continue
		}
		v := strings.TrimSpace(raw[i])
		if n, err := strconv.Atoi(v); err == nil && n >= 1 && n <= len(q.Options) {
			out[i] = agent.Answer{Label: q.Options[n-1].Label}
			continue
		}
		matched := false
		for _, o := range q.Options {
			if strings.EqualFold(o.Label, v) {
				out[i], matched = agent.Answer{Label: o.Label}, true
				break
			}
		}
		if !matched {
			out[i] = agent.Answer{Label: agent.OtherLabel, Reason: v}
		}
	}
	return out
}

var (
	_ agent.Asker              = (*BgAsker)(nil)
	_ agent.AskTimeoutNotifier = (*BgAsker)(nil)
)
