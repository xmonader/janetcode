package acp

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xmonader/janetcode/internal/agent"
	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/session"
	"github.com/xmonader/janetcode/internal/sessionproto"
)

// orderRunner records the prompts it is given, in the order turns start,
// and holds each turn until released.
type orderRunner struct {
	mu      sync.Mutex
	texts   []string
	started chan string
	release chan struct{}
}

func (o *orderRunner) RunBlocks(ctx context.Context, blocks []llm.Block, _ func(agent.Event)) error {
	text := ""
	if len(blocks) > 0 {
		text = blocks[0].Text
	}
	o.mu.Lock()
	o.texts = append(o.texts, text)
	o.mu.Unlock()
	o.started <- text
	select {
	case <-o.release:
	case <-ctx.Done():
		return ctx.Err()
	}
	return nil
}
func (o *orderRunner) MessagesSnapshot() []llm.Message { return nil }
func (o *orderRunner) SetApprover(agent.Approver)      {}

// Prompts a client sends while a turn runs wait their turn in the order
// they were sent, never overtaking each other.
func TestPromptsRunInTheOrderTheyWereSent(t *testing.T) {
	o := &orderRunner{started: make(chan string, 8), release: make(chan struct{})}
	dir, cwd := t.TempDir(), t.TempDir()
	c, stop := newClient(t, Options{Version: "test", NewAgent: func(_, _ string) (TurnRunner, *session.Session, error) {
		s, err := session.New(dir, cwd, "fake", "m")
		return o, s, err
	}})
	defer stop()
	id := newSessionID(t, c)
	words := []string{"one", "two", "three", "four", "five"}
	for _, w := range words {
		c.call("session/prompt", promptParams{SessionID: id, Prompt: []contentBlock{{Type: "text", Text: w}}})
	}
	for i, want := range words {
		select {
		case got := <-o.started:
			if got != want {
				t.Fatalf("turn %d ran %q, want %q (order %v)", i, got, want, o.texts)
			}
		case <-time.After(10 * time.Second):
			t.Fatalf("turn %d (%q) never started", i, want)
		}
		o.release <- struct{}{}
	}
	for range words {
		c.readUntil(isResponse)
	}
}

func TestInitializeSaysWhichSessionProtocolItSpeaks(t *testing.T) {
	opts, _ := testOptions(t)
	c, stop := newClient(t, opts)
	defer stop()
	c.call("initialize", initializeParams{ProtocolVersion: 1})
	m := c.read()
	var res struct {
		Meta struct {
			Janetcode struct {
				SessionProtocol int `json:"sessionProtocol"`
			} `json:"janetcode"`
		} `json:"_meta"`
	}
	if err := json.Unmarshal(m.Result, &res); err != nil {
		t.Fatal(err)
	}
	if res.Meta.Janetcode.SessionProtocol != sessionproto.Version {
		t.Fatalf("initialize result %s lacks _meta.janetcode.sessionProtocol = %d", m.Result, sessionproto.Version)
	}
}

// A session another janetcode runs is refused with an error a client can
// act on without parsing the message: its own code, and data naming the
// holder and the ways on.
func TestLeasedSessionIsAStructuredConflict(t *testing.T) {
	held := &session.LeasedError{ID: "20260101-000000-abcdef", Info: session.LeaseInfo{PID: 4121, Host: "box", Holder: "the terminal interface", Since: time.Unix(1_700_000_000, 0)}}
	opts := Options{Version: "test", NewAgent: func(_, resume string) (TurnRunner, *session.Session, error) {
		return nil, nil, held
	}}
	c, stop := newClient(t, opts)
	defer stop()
	c.call("initialize", initializeParams{ProtocolVersion: 1})
	c.read()
	for _, call := range []struct {
		method string
		params any
	}{
		{"session/load", loadSessionParams{SessionID: held.ID, Cwd: t.TempDir()}},
		{"session/new", newSessionParams{Cwd: t.TempDir()}},
	} {
		c.call(call.method, call.params)
		m := c.read()
		if m.Error == nil || m.Error.Code != codeSessionLeased {
			t.Fatalf("%s: error = %+v, want code %d", call.method, m.Error, codeSessionLeased)
		}
		raw, _ := json.Marshal(m.Error.Data)
		var data struct {
			Protocol int
			Code     string
			Session  string
			Holder   session.LeaseInfo
			Options  []string
		}
		if err := json.Unmarshal(raw, &data); err != nil {
			t.Fatal(err)
		}
		if data.Protocol != sessionproto.Version || data.Code != "session_leased" || data.Session != held.ID ||
			data.Holder.PID != 4121 || data.Holder.Holder != "the terminal interface" || strings.Join(data.Options, ",") != "fork,follow" {
			t.Fatalf("%s: conflict data = %s", call.method, raw)
		}
	}
}

// Any other failure to open a session stays a plain error.
func TestOtherOpenFailuresAreNotLeaseConflicts(t *testing.T) {
	opts := Options{Version: "test", NewAgent: func(_, _ string) (TurnRunner, *session.Session, error) {
		return nil, nil, context.DeadlineExceeded
	}}
	c, stop := newClient(t, opts)
	defer stop()
	c.call("initialize", initializeParams{ProtocolVersion: 1})
	c.read()
	c.call("session/new", newSessionParams{Cwd: t.TempDir()})
	if m := c.read(); m.Error == nil || m.Error.Code == codeSessionLeased || m.Error.Data != nil {
		t.Fatalf("error = %+v", m.Error)
	}
}
