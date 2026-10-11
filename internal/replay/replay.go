// Package replay turns a recorded session into a provider.
//
// The session log already contains every assistant message the model sent,
// in order, so it is a cassette as well as a record: a session can be run
// again with the model's part read from disk instead of bought. That makes
// the whole agent loop — tool dispatch, permissions, verdicts, compaction,
// the shape of the history — testable offline, deterministically, and
// without an API key.
//
// It replays the model's decisions, not the wire. A provider bug in how
// streamed chunks are assembled is not visible here; those are covered by
// the provider's own tests. What is visible is everything the harness does
// with a response once it has one, which is where a change is most likely
// to alter behaviour without anybody noticing.
package replay

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/session"
)

// Provider answers each request with the next recorded assistant message.
type Provider struct {
	responses []*llm.Response
	// turns is the user turn each response in responses answered, same
	// index for same index, so a caller (Until) can find where one turn's
	// model calls end and the next one's begin without re-parsing the log.
	turns []int
	at    int
}

// Remaining is how many recorded responses have not been used.
func (p *Provider) Remaining() int { return len(p.responses) - p.at }

func (p *Provider) Name() string { return "replay" }

// Free clears the usage the recording carries, so the responses it serves
// count as what they cost this time: nothing. --what-if keeps it, to say
// what the whole session would have cost; --ab-replay clears it, since
// its result object reports what the run bought.
func (p *Provider) Free() {
	for _, r := range p.responses {
		r.Usage = llm.Usage{}
	}
}

// Stream returns the next recorded response. Running out means the code
// under test asked for more model calls than the recording holds, which is
// a behaviour change worth failing on rather than papering over.
func (p *Provider) Stream(ctx context.Context, req llm.Request, emit func(llm.Event)) (*llm.Response, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if p.at >= len(p.responses) {
		return nil, fmt.Errorf("replay: the recording holds %d model call(s) and a %dth was asked for", len(p.responses), p.at+1)
	}
	r := p.responses[p.at]
	p.at++
	if emit != nil {
		for _, b := range r.Message.Blocks {
			switch b.Type {
			case llm.BlockText:
				emit(llm.Event{Type: llm.EventTextDelta, Text: b.Text})
			case llm.BlockThinking:
				emit(llm.Event{Type: llm.EventThinkingDelta, Text: b.Text})
			case llm.BlockToolUse:
				bb := b
				emit(llm.Event{Type: llm.EventToolUseStart, Block: &bb})
				emit(llm.Event{Type: llm.EventToolUseEnd, Block: &bb})
			}
		}
	}
	return r, nil
}

// Load reads a session file written by the session log.
func Load(path string) (*Provider, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return Read(f)
}

// Read parses a session log from r.
func Read(r io.Reader) (*Provider, error) {
	p := &Provider{}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 1024*1024), 64*1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || !strings.HasPrefix(line, "{") {
			continue
		}
		var rec struct {
			Type  string `json:"type"`
			Event struct {
				Type      string       `json:"t"`
				Turn      int          `json:"turn"`
				SurfaceOp string       `json:"surface_op"`
				Message   *llm.Message `json:"message"`
				Usage     *llm.Usage   `json:"usage"`
			}
		}
		// The log writes the event's fields alongside "type":"event", so
		// the envelope and the event are decoded from the same object.
		if json.Unmarshal([]byte(line), &rec) != nil {
			continue
		}
		if json.Unmarshal([]byte(line), &rec.Event) != nil {
			continue
		}
		switch rec.Event.Type {
		case session.TypeAssistant:
			// A replacement is the harness rewriting a message the model
			// already sent — compaction dropping old thinking blocks — not
			// a model call. Served as one, it answered a later request
			// with a stale earlier message and put every reply after it
			// out of step.
			if rec.Event.Message == nil || rec.Event.SurfaceOp == session.SurfaceOpReplace {
				continue
			}
			resp := &llm.Response{Message: *rec.Event.Message, StopReason: stopOf(*rec.Event.Message)}
			if rec.Event.Usage != nil {
				resp.Usage = *rec.Event.Usage
			}
			p.responses = append(p.responses, resp)
			p.turns = append(p.turns, rec.Event.Turn)
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if len(p.responses) == 0 {
		return nil, errors.New("replay: the recording holds no assistant messages")
	}
	return p, nil
}

// Until wraps recorded so it answers with the recording through the last
// model call of turn-1, then delegates every call from turn onward to
// live. It is --what-if's (internal/whatif) core mechanism: turns before
// turn re-execute for free against the model's actual recorded replies,
// so the tools they drive put the tree back exactly where the session had
// it, and turn onward talks to a real model — possibly a different one,
// with different tools, ablations or a rewritten prompt.
//
// The boundary is decided from the recording itself, not counted turns of
// Stream calls: recorded's turns (set by Read from each assistant event's
// own Turn field) name which user turn each recorded response answered,
// and Until switches at the first response numbered turn or later. A
// recording with no call that late — turn is beyond what was recorded —
// switches on the very first call, i.e. behaves as pure live from the
// start; a recording with only earlier turns never switches at all, i.e.
// behaves as recorded alone (Remaining still reads straight off it). The
// caller drives the turns themselves from the session log (session.Turns).
func Until(recorded *Provider, turn int, live llm.Provider) llm.Provider {
	cutoff := len(recorded.responses)
	for i, t := range recorded.turns {
		if t >= turn {
			cutoff = i
			break
		}
	}
	return &untilProvider{recorded: recorded, cutoff: cutoff, live: live}
}

// UntilCall is Until with the boundary at a model call instead of a user
// turn: it answers with the first calls recorded responses, then
// delegates every later call to live. It is --ab-replay's mechanism: a
// headless run is one user turn, so the point a branch starts from lies
// inside that turn.
//
// served, when set, runs once, right after the last recorded response has
// been handed out and before the tool calls it carries run: the moment
// for the caller to queue what the live part should be told (the agent
// delivers queued harness text with those tool results, before the first
// live call) and to restore any guardrail it held off while replaying. A
// calls of zero or less is pure live from the first call, and served
// never runs; calls beyond the recording serve all of it and then go
// live.
func UntilCall(recorded *Provider, calls int, live llm.Provider, served func()) llm.Provider {
	cutoff := min(max(calls, 0), len(recorded.responses))
	return &untilProvider{recorded: recorded, cutoff: cutoff, live: live, served: served}
}

// untilProvider is Until's provider. It never switches back to recorded
// once live has answered a call: from the moment turn N is reached, the
// rest of the run is live, even for the model calls of a turn already
// under way.
type untilProvider struct {
	recorded *Provider
	cutoff   int
	live     llm.Provider
	switched bool
	// served runs once the last recorded response has been handed out
	// (see UntilCall); nil for Until.
	served func()
}

func (p *untilProvider) Name() string {
	if p.switched {
		return p.live.Name()
	}
	return p.recorded.Name()
}

func (p *untilProvider) Stream(ctx context.Context, req llm.Request, emit func(llm.Event)) (*llm.Response, error) {
	if !p.switched && p.recorded.at < p.cutoff {
		r, err := p.recorded.Stream(ctx, req, emit)
		if err == nil && p.recorded.at == p.cutoff && p.served != nil {
			p.served()
			p.served = nil
		}
		return r, err
	}
	p.switched = true
	return p.live.Stream(ctx, req, emit)
}

// stopOf recovers why a response ended. A message carrying tool calls
// ended to make them; anything else ended the turn.
func stopOf(m llm.Message) llm.StopReason {
	if len(m.ToolUses()) > 0 {
		return llm.StopToolUse
	}
	return llm.StopEndTurn
}
