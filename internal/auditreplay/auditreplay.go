// Package auditreplay reconstructs a recorded session offline and checks
// it against itself.
//
// A session log is written under one rule: whatever the model was shown
// is in the log. Messages are surface events; the system prompt and the
// tool schemas are in model/request events (session.ModelRequest); every
// request also names its inputs by digest. Replay walks the log, rebuilds
// each request from the events before it, and compares the rebuilt
// request with the digests recorded when it was sent. A block the model
// saw that no event accounts for, an event edited after the fact, a tool
// call whose arguments differ from what the model asked for, a reply with
// no result: each is reported as a divergence.
//
// Replay never contacts a model, runs a tool, opens a network connection
// or touches the workspace. It reads the log and nothing else, so it
// needs no provider, no credentials and no checkout. The recorded
// replies stand in for the model (see internal/replay for the provider
// that serves them to a live agent) and the recorded tool results stand
// in for the tools.
package auditreplay

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/session"
)

// Report is what a replay reconstructed and what did not add up.
type Report struct {
	Steps []Step
	// Problems are the divergences, each naming the event seq at fault.
	Problems []string
	// Mutations and Checks count the file changes and verification gates
	// the replay linked to a turn.
	Mutations, Checks int
}

// OK reports whether the replay found no divergence.
func (r Report) OK() bool { return len(r.Problems) == 0 }

// Step is one model call: the request as it was reconstructed, the reply
// and what became of each tool call in it.
type Step struct {
	Seq      int // the request event
	Turn     int
	Model    string
	Provider string
	// System, Tools and Messages are the model-visible request, rebuilt.
	System   string
	Tools    []llm.ToolDef
	Messages []llm.Message
	// Reply is the model's text; Calls what it asked for.
	Reply string
	Calls []Call
}

// Call is one tool call with everything the log says about it.
type Call struct {
	ID    string
	Name  string
	Input string
	// InputSHA names the arguments the model sent.
	InputSHA string
	// Authorized is what let it run ("rule bash(go test *)", "mode: yolo",
	// "the user: deny", ...); "read-only" when the call could change
	// nothing and so needed no record.
	Authorized string
	Denied     bool
	// Result is the logged output, ResultSHA its digest, Outcome one of
	// ok / failed / not run.
	Result    string
	ResultSHA string
	Outcome   string
	// Seen is the text of the tool-result block the model was next shown
	// for this call, harness notes included; SeenSHA its digest.
	Seen    string
	SeenSHA string
	// Changes are the files the call changed, as "<path> <before>-><after>".
	Changes []Change
}

// Change is one file a call changed.
type Change struct {
	Path    string
	Digests string
}

// Replay rebuilds the session from events alone.
func Replay(events []session.Event) Report {
	var rep Report
	r := replayer{rep: &rep, events: events}
	r.run()
	return rep
}

type replayer struct {
	rep    *Report
	events []session.Event

	system    string
	hasSystem bool
	tools     []llm.ToolDef
	hasTools  bool

	calls   map[string]session.Event // tool/call by call id
	results map[string]session.Event // log-only tool/result by call id
	perms   map[string]session.Event
	changes map[string][]session.Event
}

func (r *replayer) problem(seq int, format string, args ...any) {
	r.rep.Problems = append(r.rep.Problems, fmt.Sprintf("seq %d: ", seq)+fmt.Sprintf(format, args...))
}

func (r *replayer) index() {
	r.calls = map[string]session.Event{}
	r.results = map[string]session.Event{}
	r.perms = map[string]session.Event{}
	r.changes = map[string][]session.Event{}
	for _, e := range r.events {
		if e.CallID == "" {
			continue
		}
		switch e.Type {
		case session.TypeToolCall:
			r.calls[e.CallID] = e
		case session.TypeToolResult:
			if !e.Surface && e.Name != "" {
				r.results[e.CallID] = e
			}
		case session.TypePermission:
			r.perms[e.CallID] = e
		case session.TypeFileChange:
			r.changes[e.CallID] = append(r.changes[e.CallID], e)
		}
	}
}

func (r *replayer) run() {
	r.index()
	for i, e := range r.events {
		switch e.Type {
		case session.TypeRequest:
			r.request(i, e)
		case session.TypeGate:
			r.rep.Checks++
		case session.TypeFileChange:
			r.rep.Mutations++
		}
	}
	if len(r.rep.Steps) == 0 {
		r.rep.Problems = append(r.rep.Problems, "the session records no model request: it was recorded before requests were logged, so what the model saw cannot be reconstructed")
	}
}

// request rebuilds the request event i carries and checks it.
func (r *replayer) request(i int, e session.Event) {
	req, err := session.RequestOf(e)
	if err != nil {
		r.problem(e.Seq, "model/request is not readable: %v", err)
		return
	}
	if req.System != "" || !r.hasSystem {
		r.system, r.hasSystem = req.System, true
	}
	if req.Tools != nil || !r.hasTools {
		r.tools, r.hasTools = req.Tools, true
	}
	msgs := session.FoldSurface(r.events[:i])
	step := Step{Seq: e.Seq, Turn: e.Turn, Model: req.Model, Provider: req.Provider,
		System: r.system, Tools: r.tools, Messages: msgs}

	if got := session.DigestText(r.system); got != req.SystemSHA {
		r.problem(e.Seq, "the system prompt on record does not match the one the model was sent (recorded %s, rebuilt %s)", short(req.SystemSHA), short(got))
	}
	if got := session.DigestTools(r.tools); got != req.ToolsSHA {
		r.problem(e.Seq, "the tool schemas on record do not match the ones the model was sent (recorded %s, rebuilt %s)", short(req.ToolsSHA), short(got))
	}
	if got := session.DigestMessages(msgs); got != req.MessagesSHA {
		r.problem(e.Seq, "the messages the model was shown are not what the log holds: it was sent %d (digest %s), the log rebuilds %d (digest %s); a model-visible block has no logged event, or an event was altered", req.Messages, short(req.MessagesSHA), len(msgs), short(got))
	}

	reply, ok := r.replyTo(i)
	if !ok {
		// The last request of a turn that was cancelled or failed has no
		// reply; that is the run's end, not a divergence.
		r.rep.Steps = append(r.rep.Steps, step)
		return
	}
	step.Reply = reply.Message.Text()
	for _, u := range reply.Message.ToolUses() {
		step.Calls = append(step.Calls, r.call(reply, u))
	}
	r.rep.Steps = append(r.rep.Steps, step)
}

// replyTo is the assistant message the request at index i was answered
// with: the next one before another request.
func (r *replayer) replyTo(i int) (session.Event, bool) {
	for _, e := range r.events[i+1:] {
		switch e.Type {
		case session.TypeRequest:
			return session.Event{}, false
		case session.TypeAssistant:
			if e.Message != nil && e.SurfaceOp != session.SurfaceOpReplace {
				return e, true
			}
		}
	}
	return session.Event{}, false
}

// call joins one tool_use block with its logged call, authorization,
// result and file changes, and reports what is missing or contradictory.
func (r *replayer) call(reply session.Event, u llm.Block) Call {
	c := Call{ID: u.ID, Name: u.Name, Input: string(u.Input), InputSHA: session.DigestText(string(u.Input)), Authorized: "read-only"}
	logged, hasCall := r.calls[u.ID]
	result, hasResult := r.results[u.ID]
	perm, hasPerm := r.perms[u.ID]
	if hasPerm {
		c.Authorized, c.Denied = perm.Reason, perm.Denied
	}
	switch {
	case hasCall && !sameJSON(logged.Arguments, string(u.Input)):
		r.problem(logged.Seq, "tool call %s (%s): the logged arguments differ from the ones the model sent (logged %s, sent %s)", u.ID, u.Name, short(session.DigestText(logged.Arguments)), short(c.InputSHA))
	case !hasCall && !hasResult:
		r.problem(reply.Seq, "tool call %s (%s) has no logged call and no logged result", u.ID, u.Name)
	}
	if hasResult && !sameJSON(result.Arguments, string(u.Input)) {
		r.problem(result.Seq, "tool call %s (%s): its result was logged for different arguments than the model sent", u.ID, u.Name)
	}
	if hasResult {
		c.Result, c.ResultSHA = result.Output, session.DigestText(result.Output)
		switch {
		case result.Denied:
			c.Outcome = "not run"
		case result.IsError:
			c.Outcome = "failed"
		default:
			c.Outcome = "ok"
		}
		if hasCall && !hasPerm && result.Denied {
			r.problem(result.Seq, "tool call %s (%s) was refused with no permission record", u.ID, u.Name)
		}
	} else if hasCall {
		r.problem(logged.Seq, "tool call %s (%s) has no logged result", u.ID, u.Name)
	}
	for _, ch := range r.changes[u.ID] {
		c.Changes = append(c.Changes, Change{Path: ch.Name, Digests: ch.Arguments})
		if c.Outcome != "ok" {
			r.problem(ch.Seq, "file %s is recorded as changed by call %s, whose outcome was %q", ch.Name, u.ID, c.Outcome)
		}
	}
	c.Seen, c.SeenSHA = r.seen(reply.Seq, u)
	if c.Outcome != "" && c.Seen == "" && !r.answered(reply.Seq, u) {
		r.problem(reply.Seq, "tool call %s (%s) has a logged result but the model was never shown one", u.ID, u.Name)
	}
	return c
}

// seen is the tool-result block that answers u: the first one after reply
// whose ToolUseID is the id the model was told (the server's own when the
// harness renamed the call).
func (r *replayer) seen(replySeq int, u llm.Block) (text, sha string) {
	b, ok := r.answerBlock(replySeq, u)
	if !ok {
		return "", ""
	}
	return b.Text, session.DigestText(b.Text)
}

func (r *replayer) answered(replySeq int, u llm.Block) bool {
	_, ok := r.answerBlock(replySeq, u)
	return ok
}

func (r *replayer) answerBlock(replySeq int, u llm.Block) (llm.Block, bool) {
	id := u.ID
	if u.ReplyID != "" {
		id = u.ReplyID
	}
	after := false
	for _, e := range r.events {
		if e.Seq == replySeq {
			after = true
			continue
		}
		if !after || !e.Surface || e.Message == nil || e.Type != session.TypeToolResult {
			continue
		}
		for _, b := range e.Message.Blocks {
			if b.Type == llm.BlockToolResult && b.ToolUseID == id {
				return b, true
			}
		}
	}
	return llm.Block{}, false
}

// sameJSON compares two JSON documents by value, so formatting a log line
// went through does not read as divergence; non-JSON compares as text.
func sameJSON(a, b string) bool {
	if a == b {
		return true
	}
	var ca, cb bytes.Buffer
	if json.Compact(&ca, []byte(a)) != nil || json.Compact(&cb, []byte(b)) != nil {
		return false
	}
	return ca.String() == cb.String()
}

func short(d string) string {
	if len(d) > 12 {
		return d[:12]
	}
	return d
}

// Text renders the replay as a readable record: for each model call the
// request's size and digests, then each tool call with what authorized it,
// how it ended and which files it changed.
func (r Report) Text() string {
	var sb strings.Builder
	turn := -1
	for _, s := range r.Steps {
		if s.Turn != turn {
			turn = s.Turn
			fmt.Fprintf(&sb, "turn %d\n", turn)
		}
		fmt.Fprintf(&sb, "  request #%d  %s  system %d bytes (%s), %d tool schema(s) (%s), %d message(s) (%s)\n",
			s.Seq, s.Model, len(s.System), short(session.DigestText(s.System)), len(s.Tools), short(session.DigestTools(s.Tools)), len(s.Messages), short(session.DigestMessages(s.Messages)))
		if s.Reply != "" {
			fmt.Fprintf(&sb, "    reply    %s\n", clip(s.Reply, 100))
		}
		for _, c := range s.Calls {
			fmt.Fprintf(&sb, "    call     %s %s  [%s] authorized by %s; input %s\n", c.Name, clip(c.Input, 80), c.Outcome, c.Authorized, short(c.InputSHA))
			if c.Result != "" {
				fmt.Fprintf(&sb, "    result   %s (%s)\n", clip(c.Result, 80), short(c.ResultSHA))
			}
			for _, ch := range c.Changes {
				fmt.Fprintf(&sb, "    changed  %s %s\n", ch.Path, ch.Digests)
			}
		}
	}
	fmt.Fprintf(&sb, "\n%d model call(s), %d file change(s), %d check(s) reconstructed\n", len(r.Steps), r.Mutations, r.Checks)
	if r.OK() {
		sb.WriteString("replay: the recorded run reproduces: every request rebuilds from the log exactly as it was sent\n")
	} else {
		fmt.Fprintf(&sb, "replay: DIVERGED, %d problem(s)\n", len(r.Problems))
		for _, p := range r.Problems {
			sb.WriteString("  " + p + "\n")
		}
	}
	return sb.String()
}

func clip(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
