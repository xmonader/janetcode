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
	prompts   []string
	at        int
}

// Prompts are the user messages of the recorded session, in order, so a
// caller can drive the same turns that were recorded.
func (p *Provider) Prompts() []string { return p.prompts }

// Remaining is how many recorded responses have not been used.
func (p *Provider) Remaining() int { return len(p.responses) - p.at }

func (p *Provider) Name() string { return "replay" }

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
				Type    string       `json:"t"`
				Message *llm.Message `json:"message"`
				Usage   *llm.Usage   `json:"usage"`
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
		case session.TypeUserMessage:
			if rec.Event.Message != nil {
				if t := strings.TrimSpace(rec.Event.Message.Text()); t != "" {
					p.prompts = append(p.prompts, t)
				}
			}
		case session.TypeAssistant:
			if rec.Event.Message == nil {
				continue
			}
			resp := &llm.Response{Message: *rec.Event.Message, StopReason: stopOf(*rec.Event.Message)}
			if rec.Event.Usage != nil {
				resp.Usage = *rec.Event.Usage
			}
			p.responses = append(p.responses, resp)
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if len(p.responses) == 0 {
		return nil, fmt.Errorf("replay: %s holds no assistant messages", "the recording")
	}
	return p, nil
}

// stopOf recovers why a response ended. A message carrying tool calls
// ended to make them; anything else ended the turn.
func stopOf(m llm.Message) llm.StopReason {
	if len(m.ToolUses()) > 0 {
		return llm.StopToolUse
	}
	return llm.StopEndTurn
}
