package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/xmonader/janetcode/internal/egress"
	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/session"
	"github.com/xmonader/janetcode/internal/tool"
)

// The egress report is kept per turn: the recorder rides on the turn's
// context, so the provider's transport (every model call, model slots
// and advisor included), fetch_url, web search, a remote MCP server and a
// subagent's calls all land in the turn that caused them. The agent adds
// what only it knows: which files and tool output went back into the
// conversation, and how many secret-shaped values were scrubbed first.

// Egressor is a tool that can say which hosts a call would send data to,
// before it runs, so the egress policy can ask or refuse.
type Egressor interface {
	EgressHosts(input json.RawMessage) []string
}

// EgressRecorder is the session's recorder, created on first use.
func (a *Agent) EgressRecorder() *egress.Recorder {
	a.egressMu.Lock()
	defer a.egressMu.Unlock()
	if a.egressRec == nil {
		a.egressRec = &egress.Recorder{}
	}
	return a.egressRec
}

// EgressLast is the last finished turn's report.
func (a *Agent) EgressLast() (egress.Report, bool) {
	return a.EgressRecorder().Last()
}

// EgressSummary is /egress: the policy, then what the turn in progress
// has sent so far or, between turns, what the last one sent.
func (a *Agent) EgressSummary() string {
	rec := a.EgressRecorder()
	var body string
	if a.running.Load() {
		body = rec.Current().Text() + "\n(the turn is still running)"
	} else if rep, ok := rec.Last(); ok {
		body = rep.Text()
	} else {
		body = "no turn has run yet in this session"
	}
	return a.EgressPolicy.Describe() + a.egressBashNote() + "\n\n" + body + "\n\nEvery turn's report is in the session log (egress/turn) and in headless JSON (\"egress\")."
}

// egressBashNote says how far the policy reaches bash, "" with no policy.
func (a *Agent) egressBashNote() string {
	if !a.EgressPolicy.Active() {
		return ""
	}
	if note := a.Sandbox.EgressNote(); note != "" {
		return "\nbash: " + note
	}
	return "\nbash: no sandbox routes it through the egress proxy, so the policy reaches bash only through the hosts a command spells out"
}

// StartEgressProxy starts the filtering proxy a sandboxed bash command is
// narrowed to while the egress policy is active (see egress.Proxy and
// shell.Sandbox.SetEgressProxy), and returns it for the caller to close.
// Nil, nil when there is no policy or no sandbox to route.
func (a *Agent) StartEgressProxy() (*egress.Proxy, error) {
	if !a.EgressPolicy.Active() || a.Sandbox == nil {
		return nil, nil
	}
	px := &egress.Proxy{Policy: a.EgressPolicy, Recorder: a.EgressRecorder, Ask: a.askEgressHost}
	if err := px.Start(); err != nil {
		return nil, err
	}
	a.Sandbox.SetEgressProxy(px)
	return px, nil
}

// askEgressHost puts a host the policy wants asked about to the person,
// as a bash call: a sandboxed command is trying to reach it through the
// proxy. No approver (a headless run with no one to ask) refuses.
func (a *Agent) askEgressHost(host string) bool {
	if a.Approver == nil {
		return false
	}
	in, _ := json.Marshal(map[string]string{"command": "network access to " + host + " (through the egress proxy)"})
	d, err := a.Approver.Approve(context.Background(), ToolCall{
		ID: "egress-" + host, Name: "bash", Input: in,
		Warning: "a sandboxed command wants to connect to " + host + ", which the egress policy does not allow without asking",
	})
	return err == nil && d != Deny
}

// egressTurn opens the turn's ledger on ctx. A subagent's turn runs under
// its parent's context and so already carries the parent's recorder: it
// records into the parent's turn and does not open one of its own.
func (a *Agent) egressTurn(ctx context.Context, blocks []llm.Block) (context.Context, func()) {
	if egress.From(ctx) != nil {
		return ctx, func() {}
	}
	rec := a.EgressRecorder()
	rec.Begin(a.turnN + 1)
	ctx = egress.With(ctx, rec)
	if p := a.EgressPolicy; p.Active() {
		ctx = egress.WithPolicy(ctx, p)
	}
	for _, b := range blocks {
		if b.Type == llm.BlockImage {
			rec.Add(egress.Entry{Kind: egress.KindImage, What: "attached to the prompt", Bytes: int64(len(b.Data))})
		}
	}
	before, _, _, _ := a.SessionCost()
	return ctx, func() {
		after, _, _, _ := a.SessionCost()
		rep := rec.End(after - before)
		a.logEvent(session.Event{Type: session.TypeEgress, Turn: rep.Turn, Output: rep.JSON()})
	}
}

// noteToolEgress records what a tool call sent back to the model: its
// output, and for the reading tools the files whose contents that was.
func (a *Agent) noteToolEgress(ctx context.Context, name string, input json.RawMessage, out string) {
	rec := egress.From(ctx)
	if rec == nil {
		return
	}
	rec.Add(egress.Entry{Kind: egress.KindTool, What: name, Bytes: int64(len(out))})
	switch name {
	case "read_file", "read_many_files", "view_image":
		var in struct {
			Path  string   `json:"path"`
			Paths []string `json:"paths"`
		}
		if json.Unmarshal(input, &in) == nil {
			for _, p := range append([]string{in.Path}, in.Paths...) {
				if p != "" {
					rec.Add(egress.Entry{Kind: egress.KindFile, What: p})
				}
			}
		}
	}
}

// egressGate is the policy's word on one tool call before it runs: the
// hosts it would reach, and for each one not allowed either a refusal
// (block) or a reason the call must be asked about (ask).
func (a *Agent) egressGate(ctx context.Context, t tool.Tool, u llm.Block) (refusal, ask string) {
	p := a.EgressPolicy
	if !p.Active() {
		return "", ""
	}
	e, ok := t.(Egressor)
	if !ok {
		return "", ""
	}
	var asks []string
	for _, h := range e.EgressHosts(u.Input) {
		switch p.Decide(h) {
		case egress.Block:
			egress.From(ctx).Add(egress.Entry{Kind: egressKind(u.Name), Host: h, What: u.Name, Blocked: true})
			return fmt.Sprintf("Refused by the egress policy: this call would send data to %s, which is not on the allow list (egress.allow). Do not retry it through another tool; tell the user which host you need.", h), ""
		case egress.Ask:
			asks = append(asks, h)
		}
	}
	if len(asks) > 0 {
		return "", "sends data to " + strings.Join(asks, ", ") + ", which the egress policy does not allow without asking"
	}
	return "", ""
}

func egressKind(name string) string {
	switch {
	case name == "fetch_url":
		return egress.KindFetch
	case name == "web_search":
		return egress.KindSearch
	case strings.HasPrefix(name, "mcp__"):
		return egress.KindMCP
	}
	return egress.KindCommand
}
