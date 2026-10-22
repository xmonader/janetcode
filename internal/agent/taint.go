package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/xmonader/janetcode/internal/egress"
	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/session"
	"github.com/xmonader/janetcode/internal/shell"
	"github.com/xmonader/janetcode/internal/taint"
	"github.com/xmonader/janetcode/internal/tool"
)

// Taint-labelled context (package taint): every tool result is labelled
// trusted or untrusted by where it came from, and once untrusted content
// is in the context three kinds of call ask first, whatever the mode —
// a bash command that can reach the network, a change to CI, credential
// or janetcode's own control files, and a call that sends data to a host
// outside egress.allow. The question says where the argument came from
// when it can be traced. A clean repository never trips any of it: the
// user's prompt and files are trusted, so a session that reads only
// those asks nothing new. JANETCODE_OFF=taint turns the labels and the
// gate off.
//
// Two cheap additions ride on it. Honeytokens (JANETCODE_OFF=honeytoken)
// are environment variables planted in every model-run command whose
// values exist nowhere else: a tool call, or a request through the
// egress proxy, that carries one stops the run. And the session's
// lethal-trifecta state — secrets a command can read, untrusted content
// in context, egress open — is shown as a badge, with egress narrowed to
// the allow list (asking for anything else) from the moment the second
// leg goes up.

// taintTracker is the session's tracker, created on first use.
func (a *Agent) taintTracker() *taint.Tracker {
	a.taintMu.Lock()
	defer a.taintMu.Unlock()
	if a.taint == nil {
		a.taint = &taint.Tracker{}
	}
	return a.taint
}

// shareTaint gives a subagent the parent's label and switches. One
// context, one label: what a subagent reads reaches the parent through
// its report, and what tainted the parent is in its brief.
func (a *Agent) shareTaint(child *Agent) {
	child.taint = a.taintTracker()
	for _, k := range []string{"taint", "honeytoken", "depgate"} {
		if a.Off[k] {
			if child.Off == nil {
				child.Off = map[string]bool{}
			}
			child.Off[k] = true
		}
	}
}

// Tainted reports whether untrusted content is in the session's context.
// Always false with the taint switch off.
func (a *Agent) Tainted() bool {
	return !a.Off["taint"] && a.taintTracker().Tainted()
}

// taintTurn puts the tracker on the turn's context (for the memory tool)
// and plants the honeytokens before anything runs.
func (a *Agent) taintTurn(ctx context.Context) context.Context {
	a.plantCanaries()
	if a.Off["taint"] {
		return ctx
	}
	return taint.With(ctx, a.taintTracker())
}

// retaint rebuilds the label from a conversation being opened: a resumed
// session that had fetched a page is as tainted as it was. An empty
// history (a new session, /clear) is clean.
func (a *Agent) retaint(history []llm.Message) {
	tr := a.taintTracker()
	tr.Reset()
	a.taintMu.Lock()
	a.taintHosts = nil
	a.taintMu.Unlock()
	uses := map[string]llm.Block{}
	for _, m := range history {
		for _, b := range m.Blocks {
			switch b.Type {
			case llm.BlockToolUse:
				uses[b.ID] = b
			case llm.BlockToolResult:
				if u, ok := uses[b.ToolUseID]; ok {
					if src, bad := taint.Untrusted(u.Name, u.Input, b.Text); bad {
						tr.Add(src, b.Text)
					}
				}
			}
		}
	}
}

// noteTaint labels one tool result, and says so the first time the
// session turns tainted.
func (a *Agent) noteTaint(name string, input json.RawMessage, out string, emit func(Event)) {
	if a.Off["taint"] {
		return
	}
	src, bad := taint.Untrusted(name, input, out)
	if !bad {
		return
	}
	tr := a.taintTracker()
	first := !tr.Tainted()
	tr.Add(src, out)
	if !first || emit == nil {
		return
	}
	msg := "untrusted content entered the context (" + src + "): from now on network commands, changes to CI, credential or janetcode settings files, and data sent to hosts outside egress.allow ask first"
	if secrets, _, open := a.trifectaLegs(); secrets && open {
		msg += " · lethal trifecta: secrets readable, untrusted content, egress open — egress is narrowed to the allow list"
	}
	emit(Event{Kind: EvNotice, Text: msg})
}

// taintGate is the tainted turn's word on one call: why it must be asked
// about, or "", and the hosts an approval lets the rest of the session
// reach.
func (a *Agent) taintGate(t tool.Tool, u llm.Block) (why string, hosts []string) {
	if a.Off["taint"] || !a.taintTracker().Tainted() {
		return "", nil
	}
	tr := a.taintTracker()
	root := a.displayRoot()
	switch cmd, isShell := shellLine(u); {
	case isShell:
		if needles, ok := taint.EgressCommand(cmd); ok {
			// The hosts the command spells out are remembered on approval
			// (taintApprove) and not asked about again this session, so a
			// tainted turn does not prompt once per curl to the same
			// host. A command that names no host — gh api, an ssh alias —
			// has nothing to cache and keeps asking each time.
			var fresh []string
			for _, h := range shellEgressHosts(cmd) {
				if !a.EgressPolicy.Listed(h) && !a.taintAllowed(h) {
					fresh = append(fresh, h)
				}
			}
			if len(fresh) == 0 {
				return "", nil
			}
			return tr.Explain("a command that can reach the network", needles), fresh
		}
		for _, target := range shell.WriteTargets(cmd, root) {
			if what := a.sensitivePath(root, target); what != "" {
				return tr.Explain("a command that writes "+what+" ("+a.relPath(root, target)+")", taint.Needles(cmd)), nil
			}
		}
	case editTool(u.Name):
		var in struct {
			Path, Content string
			New           string `json:"new_string"`
		}
		_ = json.Unmarshal(u.Input, &in)
		if in.Path == "" {
			return "", nil
		}
		if what := a.sensitivePath(root, in.Path); what != "" {
			return tr.Explain("an edit to "+what+" ("+a.relPath(root, in.Path)+")", taint.Needles(in.Content+"\n"+in.New)), nil
		}
	default:
		e, ok := t.(Egressor)
		if !ok {
			return "", nil
		}
		var fresh []string
		for _, h := range e.EgressHosts(u.Input) {
			if !a.EgressPolicy.Listed(h) && !a.taintAllowed(h) {
				fresh = append(fresh, h)
			}
		}
		if len(fresh) == 0 {
			return "", nil
		}
		needles := append([]string(nil), fresh...)
		if url := urlField(u.Input); url != "" {
			needles = append([]string{url}, needles...)
		}
		return tr.Explain("sends data to "+strings.Join(fresh, ", ")+", outside egress.allow,", needles), fresh
	}
	return "", nil
}

// shellLine is the command line a call hands to a shell, and whether it
// is one: bash's command, monitor's (which runs in the same shell), and
// the line terminal's send or job's stdin types into a running program. Only bash used to be
// read, so a tainted turn's curl went out unasked by the other two.
func shellLine(u llm.Block) (string, bool) {
	switch u.Name {
	case "bash", "monitor":
		return shell.CommandOf(string(u.Input)), true
	case "terminal", "job":
		// terminal's send and job's stdin both type a line into a
		// running program, which is often a shell.
		var in struct{ Action, Text string }
		if json.Unmarshal(u.Input, &in) == nil && (u.Name == "terminal" && in.Action == "send" || u.Name == "job" && in.Action == "stdin") {
			return strings.TrimSpace(in.Text), true
		}
	}
	return "", false
}

// shellEgressHosts are the non-loopback hosts a shell command line spells
// out: what an approval of it can safely be remembered for. It is
// recognition, not proof — a command can reach a host it does not spell —
// so only a spelled-out host is ever cached, never the whole command.
func shellEgressHosts(cmd string) []string {
	var out []string
	for _, h := range egress.HostsInCommand(cmd) {
		if h == "localhost" || strings.HasPrefix(h, "127.") || h == "::1" || h == "0.0.0.0" {
			continue
		}
		out = append(out, h)
	}
	return out
}

// sensitivePath names what a path is when a tainted turn must not change
// it unasked: CI configuration, a credential file, a protected path, or
// one of janetcode's own control files (a write there is how a session
// widens its own sandbox and permissions).
func (a *Agent) sensitivePath(root, p string) string {
	abs := p
	if !filepath.IsAbs(abs) {
		abs = filepath.Join(root, abs)
	}
	if what := taint.SensitivePath(a.relPath(root, abs)); what != "" {
		return what
	}
	if a.Protected != nil {
		if _, ok := a.Protected.MatchPath(abs); ok {
			return "a protected path"
		}
	}
	if what := tool.SelfPath(root, abs); what != "" {
		return what
	}
	return ""
}

// relPath is p relative to root when it is inside it.
func (a *Agent) relPath(root, p string) string {
	if !filepath.IsAbs(p) || root == "" {
		return filepath.ToSlash(p)
	}
	if rel, err := filepath.Rel(root, p); err == nil && !strings.HasPrefix(rel, "..") {
		return filepath.ToSlash(rel)
	}
	return filepath.ToSlash(p)
}

func urlField(input json.RawMessage) string {
	var in struct {
		URL string `json:"url"`
	}
	_ = json.Unmarshal(input, &in)
	return in.URL
}

// taintAllowed reports whether the user let a tainted turn reach host.
func (a *Agent) taintAllowed(host string) bool {
	a.taintMu.Lock()
	defer a.taintMu.Unlock()
	return a.taintHosts[strings.ToLower(host)]
}

// taintApprove remembers hosts the user approved in a tainted turn, so
// the same host is asked about once a session rather than every call.
func (a *Agent) taintApprove(hosts []string) {
	if len(hosts) == 0 {
		return
	}
	a.taintMu.Lock()
	defer a.taintMu.Unlock()
	if a.taintHosts == nil {
		a.taintHosts = map[string]bool{}
	}
	for _, h := range hosts {
		a.taintHosts[strings.ToLower(h)] = true
	}
}

// trifectaLegs are the three legs of the lethal trifecta for this
// session: secrets a model-run command can read (a credential-shaped
// variable it keeps, or no sandbox between it and the home directory),
// untrusted content in context, and egress open (no egress policy).
func (a *Agent) trifectaLegs() (secrets, untrusted, open bool) {
	secrets = !a.Sandbox.Enabled() || len(shell.ExposedSecrets(os.Environ())) > 0
	return secrets, a.Tainted(), !a.EgressPolicy.Active()
}

// TaintBadge is the status line's word on it: "" while the context is
// clean, "tainted" once it is not, and "trifecta" when the other two
// legs are up as well and egress has been narrowed to the allow list.
func (a *Agent) TaintBadge() string {
	// The status line asks on every frame: a clean session answers
	// before the environment is read.
	if !a.Tainted() {
		return ""
	}
	if secrets, _, open := a.trifectaLegs(); secrets && open {
		return "trifecta: egress asks"
	}
	return "tainted"
}

// TaintSummary is the /egress paragraph on it.
func (a *Agent) TaintSummary() string {
	if a.Off["taint"] {
		return "untrusted-content labels: off (JANETCODE_OFF=taint)"
	}
	src := a.taintTracker().Sources()
	if len(src) == 0 {
		return "untrusted-content labels: the context holds only your prompt and your repository's files"
	}
	secrets, _, open := a.trifectaLegs()
	legs := fmt.Sprintf("secrets readable: %v · untrusted content: yes · egress open: %v", secrets, open)
	return "untrusted-content labels: tainted by " + strings.Join(src, ", ") + "\n" + legs +
		"\nnetwork commands, CI/credential/settings edits and hosts outside egress.allow ask until the session is cleared"
}

// canaries are the process's honeytokens, made once: every agent in the
// process plants the same ones, since the environment they go into is
// the process's.
var canaries struct {
	once sync.Once
	list []egress.Canary
}

// plantCanaries plants the honeytokens unless the switch is off.
func (a *Agent) plantCanaries() {
	if a.Off["honeytoken"] {
		return
	}
	canaries.once.Do(func() {
		cs, err := egress.NewCanaries(nil)
		if err != nil {
			return
		}
		canaries.list = cs
		var env []string
		for _, c := range cs {
			env = append(env, c.Env())
		}
		shell.Plant(env)
	})
}

// plantedCanaries is what plantCanaries planted, nil with the switch off.
func (a *Agent) plantedCanaries() []egress.Canary {
	if a.Off["honeytoken"] {
		return nil
	}
	return canaries.list
}

// canaryCall reports a call whose input carries a honeytoken: the model
// is about to send the environment it read somewhere. It is logged and
// the run stops with egress.ErrCanary.
func (a *Agent) canaryCall(u llm.Block, emit func(Event)) error {
	c, hit := egress.MatchCanary(a.plantedCanaries(), string(u.Input))
	if !hit {
		return nil
	}
	return a.tripCanary(u.Name+" call", c.Name, emit)
}

// tripCanary records the hit and returns the error that ends the run.
func (a *Agent) tripCanary(where, name string, emit func(Event)) error {
	msg := fmt.Sprintf("honeytoken %s was about to leave the machine through %s: the run is stopped", name, where)
	a.logEvent(session.Event{Type: session.TypeNotice, Turn: a.turnN, Step: a.stepN, Output: msg})
	if emit != nil {
		emit(Event{Kind: EvNotice, Text: msg})
	}
	return egress.ErrCanary
}

// onProxyCanary is the egress proxy telling the agent a sandboxed command
// tried to send a honeytoken to host. The command is already refused;
// the call that ran it ends the run when it returns (proxyCanaryHit).
func (a *Agent) onProxyCanary(host string) {
	where := "a sandboxed command's request to " + host
	a.canaryHit.Store(&where)
}

// proxyCanaryHit ends the run after a call during which the proxy caught
// a honeytoken.
func (a *Agent) proxyCanaryHit(emit func(Event)) error {
	h := a.canaryHit.Swap(nil)
	if h == nil {
		return nil
	}
	return a.tripCanary(*h, "(planted)", emit)
}
