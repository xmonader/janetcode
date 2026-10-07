// Package egress records what a turn sends off the machine, and decides
// which hosts it may send it to.
//
// Everything a coding agent does leaks something: the model request
// carries the conversation, the files read into it and every tool's
// output; fetch_url and web search name what is being looked at; a remote
// MCP server receives its tool arguments; a shell command may push a
// branch. The Recorder is the per-turn ledger of all of it — which host,
// how many bytes, what went — so "what left my machine just now?" has an
// answer (/egress, and "egress" in headless JSON). It rides on the turn's
// context, so a provider's HTTP transport, the fetch tool and a subagent
// all write into the same turn without being handed anything.
//
// The Policy is the other half: an allow list of hosts, and what to do
// with any other (allow, ask or block).
package egress

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
)

// Kinds of egress.
const (
	KindModel    = "model"    // a request to a model provider
	KindFetch    = "fetch"    // fetch_url
	KindSearch   = "search"   // web_search
	KindMCP      = "mcp"      // a remote MCP server
	KindCommand  = "command"  // a shell command recognised as reaching a host
	KindFile     = "file"     // a file whose contents went into the conversation
	KindTool     = "tool"     // tool output that went back to the model
	KindMemory   = "memory"   // a memory entry attached to the request
	KindSkill    = "skill"    // a skill attached to the request
	KindImage    = "image"    // an image sent with the request
	KindRedacted = "redacted" // secret-shaped values scrubbed before they were sent
)

// Entry is one destination (or one item of content) for the turn, with
// what went there summed.
type Entry struct {
	Kind  string `json:"kind"`
	Host  string `json:"host,omitempty"`
	What  string `json:"what,omitempty"`
	Bytes int64  `json:"bytes,omitempty"`
	Count int    `json:"count"`
	// Blocked marks a request the policy refused: nothing was sent.
	Blocked bool `json:"blocked,omitempty"`
}

// Report is one turn's egress.
type Report struct {
	Turn    int     `json:"turn"`
	Entries []Entry `json:"entries"`
	// CostUSD is what the turn's model calls cost, when known.
	CostUSD float64 `json:"cost_usd,omitempty"`
}

// Recorder is the ledger for the current turn and the one before it. The
// zero value is ready; a nil *Recorder records nothing.
type Recorder struct {
	mu   sync.Mutex
	turn int
	cur  map[string]*Entry
	last Report
	has  bool
}

func key(e Entry) string { return e.Kind + "\x00" + e.Host + "\x00" + e.What }

// Begin closes the current turn's ledger (it becomes Last) and opens one
// for turn n.
func (r *Recorder) Begin(n int) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.cur != nil {
		r.last, r.has = r.snapshotLocked(), true
	}
	r.turn, r.cur = n, map[string]*Entry{}
}

// End closes the turn: what it sent becomes Last.
func (r *Recorder) End(cost float64) Report {
	if r == nil {
		return Report{}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	rep := r.snapshotLocked()
	rep.CostUSD = cost
	r.last, r.has, r.cur = rep, true, nil
	return rep
}

// Add records one item, merging it with an earlier one of the same kind,
// host and description.
func (r *Recorder) Add(e Entry) {
	if r == nil {
		return
	}
	e.Host = strings.ToLower(e.Host)
	if e.Count == 0 {
		e.Count = 1
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.cur == nil {
		r.cur = map[string]*Entry{}
	}
	k := key(e)
	if old, ok := r.cur[k]; ok {
		old.Bytes += e.Bytes
		old.Count += e.Count
		old.Blocked = old.Blocked || e.Blocked
		return
	}
	cp := e
	r.cur[k] = &cp
}

func (r *Recorder) snapshotLocked() Report {
	rep := Report{Turn: r.turn}
	for _, e := range r.cur {
		rep.Entries = append(rep.Entries, *e)
	}
	sort.Slice(rep.Entries, func(i, j int) bool {
		a, b := rep.Entries[i], rep.Entries[j]
		if kindOrder(a.Kind) != kindOrder(b.Kind) {
			return kindOrder(a.Kind) < kindOrder(b.Kind)
		}
		if a.Host != b.Host {
			return a.Host < b.Host
		}
		return a.What < b.What
	})
	return rep
}

// Current is the ledger of the turn in progress.
func (r *Recorder) Current() Report {
	if r == nil {
		return Report{}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.snapshotLocked()
}

// Last is the most recent finished turn's report, and whether there is one.
func (r *Recorder) Last() (Report, bool) {
	if r == nil {
		return Report{}, false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.last, r.has
}

var kinds = []string{KindModel, KindMCP, KindFetch, KindSearch, KindCommand, KindFile, KindImage, KindMemory, KindSkill, KindTool, KindRedacted}

func kindOrder(k string) int {
	for i, x := range kinds {
		if x == k {
			return i
		}
	}
	return len(kinds)
}

// Hosts are the distinct hosts the report names, in order.
func (rep Report) Hosts() []string {
	seen := map[string]bool{}
	var out []string
	for _, e := range rep.Entries {
		if e.Host != "" && !e.Blocked && !seen[e.Host] {
			seen[e.Host] = true
			out = append(out, e.Host)
		}
	}
	return out
}

// Text renders the report for a person.
func (rep Report) Text() string {
	if len(rep.Entries) == 0 {
		return fmt.Sprintf("turn %d sent nothing off this machine", rep.Turn)
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "turn %d sent data to %d host(s): %s\n", rep.Turn, len(rep.Hosts()), strings.Join(rep.Hosts(), ", "))
	section := ""
	for _, e := range rep.Entries {
		title := map[string]string{
			KindModel: "model requests", KindMCP: "remote MCP servers", KindFetch: "fetch_url", KindSearch: "web search",
			KindCommand: "shell commands that reach a host", KindFile: "files whose contents were sent", KindImage: "images",
			KindMemory: "memory entries attached", KindSkill: "skills attached", KindTool: "tool output sent back to the model",
			KindRedacted: "secret-shaped values redacted before sending",
		}[e.Kind]
		if title == "" {
			title = e.Kind
		}
		if title != section {
			section = title
			sb.WriteString(title + "\n")
		}
		var parts []string
		if e.Host != "" {
			parts = append(parts, e.Host)
		}
		if e.What != "" {
			parts = append(parts, e.What)
		}
		line := "  " + strings.Join(parts, " · ")
		if e.Bytes > 0 {
			line += " · " + human(e.Bytes)
		}
		if e.Count > 1 {
			line += fmt.Sprintf(" · %d×", e.Count)
		}
		if e.Blocked {
			line += " · BLOCKED by the egress policy, nothing sent"
		}
		sb.WriteString(line + "\n")
	}
	if rep.CostUSD > 0 {
		fmt.Fprintf(&sb, "model cost this turn: $%.4f\n", rep.CostUSD)
	}
	return strings.TrimRight(sb.String(), "\n")
}

// JSON is the report as one JSON object, for the session log.
func (rep Report) JSON() string {
	b, _ := json.Marshal(rep)
	return string(b)
}

func human(n int64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d B", n)
}

type ctxKey struct{}

// With returns ctx carrying the recorder, for everything the turn does
// under it.
func With(ctx context.Context, r *Recorder) context.Context {
	return context.WithValue(ctx, ctxKey{}, r)
}

// From is the recorder ctx carries, or nil.
func From(ctx context.Context) *Recorder {
	if ctx == nil {
		return nil
	}
	r, _ := ctx.Value(ctxKey{}).(*Recorder)
	return r
}

// Transport wraps rt so every request it sends is recorded as kind, in
// the recorder on the request's context, and refused when the policy on
// that context blocks its host. A nil rt is http.DefaultTransport.
func Transport(rt http.RoundTripper, kind string) http.RoundTripper {
	if rt == nil {
		rt = http.DefaultTransport
	}
	if t, ok := rt.(*transport); ok && t.kind == kind {
		return t
	}
	return &transport{base: rt, kind: kind}
}

type transport struct {
	base http.RoundTripper
	kind string
}

func (t *transport) RoundTrip(req *http.Request) (*http.Response, error) {
	ctx := req.Context()
	host := req.URL.Hostname()
	if p := PolicyFrom(ctx); p != nil && t.kind != KindModel {
		if p.Decide(host) == Block {
			From(ctx).Add(Entry{Kind: t.kind, Host: host, What: req.Method + " " + req.URL.Path, Blocked: true})
			return nil, &BlockedError{Host: host}
		}
	}
	n := req.ContentLength
	if n < 0 {
		n = 0
	}
	what := ""
	if t.kind != KindModel {
		what = req.Method + " " + shortPath(req.URL)
	}
	From(ctx).Add(Entry{Kind: t.kind, Host: host, What: what, Bytes: n})
	return t.base.RoundTrip(req)
}

func shortPath(u *url.URL) string {
	p := u.EscapedPath()
	if len(p) > 60 {
		p = p[:57] + "..."
	}
	if p == "" {
		p = "/"
	}
	return p
}

// BlockedError is a request the egress policy refused.
type BlockedError struct{ Host string }

func (e *BlockedError) Error() string {
	return fmt.Sprintf("refused by the egress policy: %s is not on the allow list (egress.allow in the config)", e.Host)
}
