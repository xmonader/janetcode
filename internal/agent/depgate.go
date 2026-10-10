package agent

import (
	"context"
	"net/http"
	"os"
	"time"

	"github.com/xmonader/janetcode/internal/depcheck"
	"github.com/xmonader/janetcode/internal/egress"
	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/tool"
)

// The dependency check (package depcheck): an edit that adds a name to
// go.mod, package.json or a requirements file has each new name looked up
// in its public registry, and a name that does not exist or was first
// published under 30 days ago gets a line on the edit's result and a
// notice. It never refuses an edit, and every lookup fails open — no
// network, a private module, an egress policy that refuses the registry
// — so the worst it costs is depBudget on an edit to a manifest.
// JANETCODE_OFFLINE=1 skips the lookups; JANETCODE_OFF=depgate turns the
// check off.

// depBudget is how long one edit's lookups may take, all of them.
const depBudget = 5 * time.Second

// manifest is a dependency manifest an edit is about to change, with its
// content before.
type manifest struct {
	path, eco string
	before    []byte
}

// manifestsBefore reads the manifests an edit touches, before it runs.
func (a *Agent) manifestsBefore(t tool.Tool, u llm.Block) []manifest {
	if a.Off["depgate"] || !editTool(u.Name) || os.Getenv("JANETCODE_OFFLINE") != "" || a.DryRunOn() {
		return nil
	}
	tc, ok := t.(tool.Toucher)
	if !ok {
		return nil
	}
	paths, err := tc.Touches(u.Input)
	if err != nil {
		return nil
	}
	var out []manifest
	for _, p := range paths {
		if eco := depcheck.Ecosystem(p); eco != "" {
			before, _ := os.ReadFile(p) // a new manifest has no before
			out = append(out, manifest{path: p, eco: eco, before: before})
		}
	}
	return out
}

// checkDeps looks up what the edit added and returns the note for its
// result, "" when nothing is worth one.
func (a *Agent) checkDeps(ctx context.Context, ms []manifest, emit func(Event)) string {
	if len(ms) == 0 {
		return ""
	}
	ctx, cancel := context.WithTimeout(ctx, depBudget)
	defer cancel()
	reg := a.DepRegistry
	var out string
	for _, m := range ms {
		after, err := os.ReadFile(m.path)
		if err != nil {
			continue
		}
		added := depcheck.Added(m.eco, m.before, after)
		if len(added) == 0 {
			continue
		}
		if reg == nil {
			reg = defaultRegistry()
		}
		skip := func(string) bool { return false }
		if m.eco == depcheck.Go {
			if depcheck.GoProxyURL(os.Getenv) == "off" {
				continue // the go command itself is told not to ask a proxy
			}
			skip = func(mod string) bool { return depcheck.GoPrivate(os.Getenv, mod) }
		}
		note := depcheck.Note(m.path, depcheck.Check(ctx, reg, m.eco, added, time.Now(), skip))
		if note == "" {
			continue
		}
		emit(Event{Kind: EvNotice, Text: note})
		out += "\n\n" + note
	}
	return out
}

// defaultRegistry asks the public registries (or the proxy GOPROXY
// names) through the egress transport, so the lookups are in the turn's
// egress report and a host the policy blocks is never contacted.
func defaultRegistry() *depcheck.HTTPRegistry {
	r := &depcheck.HTTPRegistry{Client: &http.Client{Transport: egress.Transport(nil, egress.KindRegistry), Timeout: depBudget}}
	if p := depcheck.GoProxyURL(os.Getenv); p != "off" {
		r.GoProxy = p
	}
	return r
}
