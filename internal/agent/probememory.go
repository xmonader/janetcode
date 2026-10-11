package agent

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/xmonader/janetcode/internal/janetref"
	"github.com/xmonader/janetcode/internal/session"
	"github.com/xmonader/janetcode/internal/textfmt"
)

// Executable memory (ProbeMemory): every probe the probe ledger freezes
// as discriminating — it failed on the code before the change and passes
// after it, so it checks what the fix was for — is also written to the
// repository's probe memory (janetref.ProbesRef). The first gate of a
// later session arms the remembered probes and the ledger replays them
// like its own, so a fix made in an earlier session is held by the check
// that showed it worked. Prose memory caught 4 of 9 lessons in the
// sessions that hit the trap: this one does not depend on the model
// choosing to remember, and a probe is a command, never text the model
// is asked to follow.
//
// A remembered probe is armed only if it passes on the tree as it was
// before this turn's first change: one that fails there already (the
// code moved on, the rule changed, the environment differs) says nothing
// about this turn and is left out rather than blamed on it. One the model
// leaves failing after being sent back is retired from the memory, the
// way the ledger retires its own.
//
// Opt-in (config probe_memory, which turns the probe ledger on with it):
// it writes a ref the user did not ask for and can send a turn back for
// a check from another session. JANETCODE_OFF=probememory turns it off;
// JANETCODE_OFF=probes turns off the ledger and with it this. A probe
// from the memory runs exactly like the ledger's own — through the bash
// tool, under the sandbox, and only when the permission rules let that
// call run without asking — and the ref is local until pushed and only
// ever fetched by an explicit `janetcode --session-ref fetch`.

// probeMemArmMax bounds how many remembered probes one session arms (two
// runs each at its first gate); the ledger's own cap still applies.
const probeMemArmMax = 8

// probeMemTimeout bounds one read or write of the memory ref.
const probeMemTimeout = 5 * time.Second

// probeMemState is the session's view of the probe memory.
type probeMemState struct {
	mu sync.Mutex
	// considered says the memory was read and armed this session.
	considered bool
	// stored maps a ledger key to its entry in the memory: the armed
	// probes, and the ones this session added (own).
	stored map[string]janetref.StoredProbe
	own    map[string]bool
}

// SetProbeMemory turns the probe memory on or off for the turns that
// follow.
func (a *Agent) SetProbeMemory(on bool) {
	a.featMu.Lock()
	a.ProbeMemory = on
	a.featMu.Unlock()
}

// ProbeMemoryOn is ProbeMemory, safe to read while a turn runs, and off
// when JANETCODE_OFF names probememory or probes.
func (a *Agent) ProbeMemoryOn() bool {
	a.featMu.RLock()
	defer a.featMu.RUnlock()
	return a.ProbeMemory && !a.Off["probememory"] && !a.Off["probes"]
}

// probeMemRepo is the repository the memory lives in: the user's own,
// never a shadow store.
func (a *Agent) probeMemRepo() string {
	if a.probeMemRepoDir != "" {
		return a.probeMemRepoDir
	}
	if r := a.userRepo(); r != nil {
		return r.Root
	}
	return ""
}

// probeMemArm reads the memory once a session and freezes into the
// ledger each remembered probe that passes on the pre-edit tree.
func (a *Agent) probeMemArm(ctx context.Context, led *probeLedger, emit func(Event)) {
	st := &a.probeMem
	st.mu.Lock()
	if st.considered || !a.ProbeMemoryOn() {
		st.mu.Unlock()
		return
	}
	st.considered = true
	st.mu.Unlock()
	repo := a.probeMemRepo()
	if repo == "" {
		return
	}
	rctx, cancel := context.WithTimeout(ctx, probeMemTimeout)
	stored, _, err := janetref.LoadProbes(rctx, repo)
	cancel()
	if err != nil || len(stored) == 0 {
		return
	}
	// Newest first: a recent fix is likelier to be near this turn's work.
	var candidates []janetref.StoredProbe
	for i := len(stored) - 1; i >= 0 && len(candidates) < probeMemArmMax; i-- {
		if !led.seen[stored[i].Cmd] {
			candidates = append(candidates, stored[i])
		}
	}
	pre := a.preEditTree(ctx)
	if pre == "" {
		return
	}
	defer os.RemoveAll(pre)
	armed, stale := 0, 0
	for _, p := range candidates {
		if ctx.Err() != nil {
			return
		}
		led.seen[p.Cmd] = true
		if a.runProbe(ctx, p.Cmd, pre).outcome != probePass {
			stale++
			continue
		}
		led.freeze(Probe{Cmd: p.Cmd, Key: p.Cmd})
		st.mu.Lock()
		if st.stored == nil {
			st.stored = map[string]janetref.StoredProbe{}
		}
		st.stored[p.Cmd] = p
		st.mu.Unlock()
		armed++
	}
	summary := fmt.Sprintf("%d remembered probe(s) armed, %d left out (they fail on the code as it was before this turn)", armed, stale)
	a.logEvent(session.Event{Type: session.TypeNotice, Turn: a.turnN, Name: "probes/memory", Output: summary})
	emit(Event{Kind: EvNotice, Text: "probe memory: " + summary})
}

// probeMemRemember writes a probe the ledger just froze as
// discriminating to the memory.
func (a *Agent) probeMemRemember(ctx context.Context, p Probe) {
	if !a.ProbeMemoryOn() {
		return
	}
	repo := a.probeMemRepo()
	if repo == "" {
		return
	}
	sp := janetref.StoredProbe{
		Cmd:   janetref.Relativize(p.Key, a.displayRoot()),
		Why:   a.probeMemWhy(),
		Added: time.Now().UTC(),
	}
	if sess, ok := a.Log.(*session.Session); ok {
		sp.Session = sess.Meta.ID
	}
	rctx, cancel := context.WithTimeout(ctx, probeMemTimeout)
	defer cancel()
	if _, err := janetref.AddProbes(rctx, repo, []janetref.StoredProbe{sp}); err != nil {
		return
	}
	st := &a.probeMem
	st.mu.Lock()
	if st.stored == nil {
		st.stored = map[string]janetref.StoredProbe{}
	}
	if st.own == nil {
		st.own = map[string]bool{}
	}
	st.stored[p.Key], st.own[p.Key] = sp, true
	st.mu.Unlock()
}

// probeMemWhy is the running turn's latest stated intent for a change,
// what a remembered probe says it guards.
func (a *Agent) probeMemWhy() string {
	cs := a.Changes()
	for i := len(cs) - 1; i >= 0; i-- {
		if cs[i].Turn == a.turnN && cs[i].Tool != "undo" && strings.TrimSpace(cs[i].Intent) != "" {
			return textfmt.Clip(cs[i].Intent, 120)
		}
	}
	return ""
}

// probeMemNote prefixes a failing remembered probe's output with where it
// came from, or is "" for a probe of this session's own.
func (a *Agent) probeMemNote(key string) string {
	st := &a.probeMem
	st.mu.Lock()
	p, ok := st.stored[key]
	own := st.own[key]
	st.mu.Unlock()
	if !ok || own {
		return ""
	}
	note := "(remembered from an earlier session"
	if p.Session != "" {
		note += " " + p.Session
	}
	if p.Why != "" {
		note += ", which made it pass: " + p.Why
	}
	return note + ")\n"
}

// probeMemRetire takes a probe the model declined to make pass out of the
// memory, so it is not armed again.
func (a *Agent) probeMemRetire(ctx context.Context, key string) {
	st := &a.probeMem
	st.mu.Lock()
	p, ok := st.stored[key]
	delete(st.stored, key)
	delete(st.own, key)
	st.mu.Unlock()
	repo := a.probeMemRepo()
	if !ok || repo == "" || !a.ProbeMemoryOn() {
		return
	}
	rctx, cancel := context.WithTimeout(ctx, probeMemTimeout)
	defer cancel()
	_, _ = janetref.DropProbes(rctx, repo, []string{p.Cmd})
}
