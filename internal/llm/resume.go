package llm

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
)

// A stream that dies mid-reply is retried by every provider as one fresh
// request (Truncated): the partial text is thrown away and the model
// writes the whole reply again, paying for every token twice and taking
// as long again. Two providers can do better. Anthropic continues an
// assistant message the request ends with (prefill), and DeepSeek does
// the same for a last assistant message marked "prefix" (its beta
// endpoint). Resume sends the partial text back that way, so the model
// picks up mid-sentence and only the rest is generated.
//
// Only a reply that is still text is resumed: a tool call cut off mid
// arguments is not something a prefix can continue, and a call whose
// reasoning would be lost (Anthropic refuses a prefill when thinking is
// on) is regenerated as before. A provider that refuses the prefill
// costs one request: the reply is then regenerated, and the wrapper stops
// trying for the rest of its life.
//
// It is opt-in (config stream_resume): it sends the model its own words
// as the start of a reply, which no provider documents as equivalent to
// the uninterrupted reply.

const (
	// DefaultResumeMax is how many times one call may be resumed.
	DefaultResumeMax = 2
	// DefaultResumeMinChars is the shortest partial worth resuming: a
	// few words cost nothing to regenerate, and a prefill that short is
	// more likely to steer the reply than to save it.
	DefaultResumeMinChars = 200
	// resumeMinTokens is the least output budget a resumed call is
	// given, whatever the partial is estimated to have spent.
	resumeMinTokens = 1024
)

// ResumeConfig sets when a broken stream is resumed.
type ResumeConfig struct {
	// Max is how many resumes one call may make; zero is DefaultResumeMax.
	Max int
	// MinChars is the shortest partial resumed; zero is
	// DefaultResumeMinChars.
	MinChars int
	// KeepsThinking says the provider accepts a prefill after the model
	// has reasoned (DeepSeek's prefix completion drops the reasoning and
	// continues the text). Anthropic does not: a call that streamed
	// thinking is regenerated.
	KeepsThinking bool
}

// ResumeStats counts what resuming did over a provider's life.
type ResumeStats struct {
	// Resumed is how many broken streams were continued; SavedChars is
	// the text they kept instead of regenerating. Refused counts a
	// provider rejecting the prefill.
	Resumed, SavedChars, Refused int
}

type resumer struct {
	hedgeBase
	cfg ResumeConfig

	refused                    *atomic.Bool
	resumed, saved, refusedCnt *atomic.Int64
}

// Resume returns p with broken streams continued from their partial text.
func Resume(p Provider, cfg ResumeConfig) Provider {
	if p == nil {
		return nil
	}
	if cfg.Max <= 0 {
		cfg.Max = DefaultResumeMax
	}
	if cfg.MinChars <= 0 {
		cfg.MinChars = DefaultResumeMinChars
	}
	return &resumer{hedgeBase: hedgeBase{inner: p}, cfg: cfg, refused: &atomic.Bool{},
		resumed: &atomic.Int64{}, saved: &atomic.Int64{}, refusedCnt: &atomic.Int64{}}
}

// ForTask implements PerTask, sharing what the route has taught.
func (r *resumer) ForTask() Provider {
	c := *r
	c.inner = ForTaskOr(r.inner)
	return &c
}

// ResumeStats reports the counts so far.
func (r *resumer) ResumeStats() ResumeStats {
	return ResumeStats{Resumed: int(r.resumed.Load()), SavedChars: int(r.saved.Load()), Refused: int(r.refusedCnt.Load())}
}

// Stream implements Provider.
func (r *resumer) Stream(ctx context.Context, req Request, emit func(Event)) (*Response, error) {
	if emit == nil {
		emit = func(Event) {}
	}
	var (
		text     strings.Builder // the reply's text so far, across resumes
		prefix   string          // what the current attempt continues
		thinking bool
		tool     bool
		resumes  int
	)
	cur := req
	for {
		actx, cancel := context.WithCancel(ctx)
		broke, spoke := false, false
		resp, err := r.inner.Stream(actx, cur, func(ev Event) {
			if broke {
				return
			}
			switch ev.Type {
			case EventStreamDiscard:
				if r.resumable(text.Len(), resumes, thinking, tool) {
					// The partial stays on screen: it is about to be
					// continued, not replaced. Cancelling stops the
					// provider's own fresh retry.
					broke = true
					cancel()
					return
				}
				// The provider retries this attempt fresh. When the
				// attempt was itself a continuation, its retry still
				// starts from the prefix, so the screen is given that back
				// after the discard.
				emit(ev)
				text.Reset()
				text.WriteString(prefix)
				thinking, tool = false, false
				if prefix != "" {
					emit(Event{Type: EventTextDelta, Text: prefix})
				}
				return
			case EventTextDelta:
				text.WriteString(ev.Text)
				spoke = true
			case EventThinkingDelta:
				thinking = true
				spoke = true
			case EventToolUseStart, EventToolUseDelta, EventToolUseEnd:
				tool = true
				spoke = true
			}
			emit(ev)
		})
		cancel()
		if !broke {
			if err != nil && prefix != "" && !spoke && ctx.Err() == nil && !Retryable(err) {
				// The provider would not continue the prefill. Regenerate
				// the reply the way it would have been without resume, and
				// stop asking this provider.
				r.refused.Store(true)
				r.refusedCnt.Add(1)
				emit(Event{Type: EventStreamDiscard})
				emit(Event{Type: EventStatus, Text: "the provider would not continue the reply (" + RetryReason(err) + "); regenerating it"})
				text.Reset()
				prefix, thinking, tool = "", false, false
				cur = req
				continue
			}
			if err == nil && prefix != "" {
				stitchResumed(resp, prefix)
			}
			return resp, err
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		// Continue from what arrived. Trailing whitespace goes: Anthropic
		// rejects a prefill that ends with it, and the model writes its
		// own space before the next word anyway.
		resumes++
		prefix = strings.TrimRight(text.String(), " \t\r\n")
		text.Reset()
		text.WriteString(prefix)
		r.resumed.Add(1)
		r.saved.Add(int64(len(prefix)))
		emit(Event{Type: EventStatus, Text: fmt.Sprintf("stream broke after %d characters; continuing the reply from them", len(prefix))})
		cur = resumeRequest(req, prefix)
	}
}

// resumable reports whether a broken attempt can be continued.
func (r *resumer) resumable(chars, resumes int, thinking, tool bool) bool {
	switch {
	case r.refused.Load(), resumes >= r.cfg.Max, tool, chars < r.cfg.MinChars:
		return false
	case thinking && !r.cfg.KeepsThinking:
		return false
	}
	return true
}

// resumeRequest is req with prefix as the start of the reply. The output
// budget shrinks by what the partial is estimated to have spent (four
// characters a token), so a resumed reply is not allowed to run on to
// twice the length the caller asked for.
func resumeRequest(req Request, prefix string) Request {
	out := req
	out.Messages = append(append([]Message(nil), req.Messages...), TextMessage(RoleAssistant, prefix))
	out.Prefill = true
	if out.MaxTokens > 0 {
		out.MaxTokens = max(out.MaxTokens-len(prefix)/4, min(resumeMinTokens, out.MaxTokens))
	}
	return out
}

// stitchResumed puts the prefix back in front of the continuation, so the
// caller sees one reply, as if the stream had never broken.
func stitchResumed(resp *Response, prefix string) {
	if resp == nil {
		return
	}
	for i, b := range resp.Message.Blocks {
		if b.Type == BlockText {
			resp.Message.Blocks[i].Text = prefix + b.Text
			return
		}
	}
	// After any reasoning, which a provider expects first.
	at := 0
	for at < len(resp.Message.Blocks) && resp.Message.Blocks[at].Type == BlockThinking {
		at++
	}
	blocks := append([]Block(nil), resp.Message.Blocks[:at]...)
	blocks = append(blocks, Block{Type: BlockText, Text: prefix})
	resp.Message.Blocks = append(blocks, resp.Message.Blocks[at:]...)
	if resp.Message.Role == "" {
		resp.Message.Role = RoleAssistant
	}
}
