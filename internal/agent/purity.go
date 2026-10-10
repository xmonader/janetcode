package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"sync"

	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/purity"
	"github.com/xmonader/janetcode/internal/tool"
)

// Auto-approval from observed purity (package purity): with the
// observed_purity setting on, every foreground bash command that runs on
// this machine runs under strace, and the trace says whether it changed
// anything. A command line seen to change nothing in purity.PromoteAfter
// runs in this repository, and that runs only programs which do not run
// code they read, stops asking: mustPrompt lets it through like a saved
// allow rule would. It goes on being watched, and the first run that
// changes something demotes it for good and says so.
//
// Promotion is the last shortcut, never a way past a guard: a deny or
// ask rule, the protected paths, the taint, egress and test gates all
// decide first and still ask. A tainted session neither promotes nor
// records — what untrusted content asked for is not evidence of what
// the user's commands do. With no usable strace nothing is recorded or
// promoted (fail closed). Opt-in, because it changes who answers a
// prompt and slows every watched command by the trace; JANETCODE_OFF=
// purity turns it off for a run.

// purityWatch is the bash call being watched: its trace file, and
// whether the bash tool actually ran it under the monitor (a background
// job or a cached result is never handed to it).
type purityWatch struct {
	mu      sync.Mutex
	callID  string
	command string
	root    string
	log     string
	wrapped bool
}

// SetObservedPurity turns observed purity on or off for the calls that
// follow.
func (a *Agent) SetObservedPurity(on bool) {
	a.featMu.Lock()
	a.ObservedPurity = on
	a.featMu.Unlock()
}

// ObservedPurityOn is ObservedPurity, safe to read while a turn runs, and
// off with no store or when JANETCODE_OFF names purity.
func (a *Agent) ObservedPurityOn() bool {
	a.featMu.RLock()
	defer a.featMu.RUnlock()
	return a.ObservedPurity && a.Purity != nil && !a.Off["purity"]
}

// purityOn reports whether purity is watching this call: on, and the
// session neither tainted nor a dry run.
func (a *Agent) purityOn() bool {
	return a.ObservedPurityOn() && !a.Tainted() && !a.DryRunOn()
}

// purityForeground reads a bash call's input and reports its command
// when the call runs in the foreground on this machine — the only kind
// the bash tool hands to a monitor.
func (a *Agent) purityForeground(u llm.Block) (string, bool) {
	if u.Name != "bash" || a.Tools == nil {
		return "", false
	}
	var in struct {
		Command     string `json:"command"`
		Background  bool   `json:"background"`
		Interactive bool   `json:"interactive"`
	}
	if json.Unmarshal(u.Input, &in) != nil || in.Background || in.Interactive {
		return "", false
	}
	t, ok := a.Tools.Get("bash")
	if !ok {
		return "", false
	}
	b, ok := t.(tool.Bash)
	// A sandboxed command is not watched: the trace would see the
	// sandbox's own setup (bwrap's mounts, Landlock's re-exec) and
	// judge the command by it.
	if !ok || (b.WS != nil && !b.WS.Local()) || b.Sandbox.Enabled() {
		return "", false
	}
	return purity.Key(in.Command), in.Command != ""
}

// purityAllows reports whether a bash call runs without asking because
// its command has been seen read-only often enough in this repository.
func (a *Agent) purityAllows(u llm.Block) bool {
	if !a.purityOn() || purity.Detect() == nil {
		return false
	}
	cmd, ok := a.purityForeground(u)
	return ok && a.Purity.Promoted(a.displayRoot(), cmd)
}

// purityCtx returns the context to run a call on: for a bash call
// purity watches, one carrying the monitor, with the trace file made.
func (a *Agent) purityCtx(ctx context.Context, u llm.Block) context.Context {
	if !a.purityOn() {
		return ctx
	}
	cmd, ok := a.purityForeground(u)
	mon := purity.Detect()
	if !ok || mon == nil {
		return ctx
	}
	f, err := os.CreateTemp("", "janetcode-purity-*.trace")
	if err != nil {
		return ctx
	}
	f.Close()
	w := &a.purityRun
	w.mu.Lock()
	if w.log != "" {
		os.Remove(w.log)
	}
	w.callID, w.command, w.root, w.log, w.wrapped = u.ID, cmd, a.displayRoot(), f.Name(), false
	w.mu.Unlock()
	return tool.WithCommandWrap(ctx, func(c *exec.Cmd) {
		mon.Wrap(c, f.Name())
		w.mu.Lock()
		w.wrapped = true
		w.mu.Unlock()
	})
}

// purityAfter reads the trace of the call that just ran, records it, and
// says so when the command was just promoted, or when a promoted one
// wrote and is demoted.
func (a *Agent) purityAfter(u llm.Block, err error, emit func(Event)) {
	w := &a.purityRun
	w.mu.Lock()
	callID, cmd, root, log, wrapped := w.callID, w.command, w.root, w.log, w.wrapped
	if callID == u.ID {
		w.callID, w.log, w.wrapped = "", "", false
	}
	w.mu.Unlock()
	if callID != u.ID || log == "" {
		return
	}
	defer os.Remove(log)
	// A tool error (cancelled, refused to start) is not a run, and a
	// session that turned tainted during the call no longer records.
	if !wrapped || err != nil || !a.purityOn() {
		return
	}
	before, after := a.Purity.Observe(root, cmd, purity.ReadTrace(log))
	var msg string
	switch {
	case before.Promoted() && !after.Promoted() && after.Impure != "":
		msg = fmt.Sprintf("observed purity: %q ran without asking because it had only read before, but this run %s; it asks again from now on", purityClip(cmd), after.Impure)
	case !before.Promoted() && after.Promoted():
		msg = fmt.Sprintf("observed purity: %q has changed nothing in %d watched runs here; it runs without asking in this repository from now on (JANETCODE_OFF=purity to stop)", purityClip(cmd), after.Pure)
	}
	if msg != "" && emit != nil {
		emit(Event{Kind: EvNotice, Text: msg})
	}
}

func purityClip(cmd string) string {
	if len(cmd) > 80 {
		return cmd[:79] + "…"
	}
	return cmd
}
