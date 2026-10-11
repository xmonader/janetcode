package agent

import (
	"os"

	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/tool"
	"github.com/xmonader/janetcode/internal/typosquat"
)

// The typosquat tripwire (package typosquat): an edit that adds an
// import to Go, Python or JavaScript/TypeScript source, or a requirement
// to go.mod, package.json or a requirements file, has each new name
// compared with the names this machine already knows — the project's
// own dependencies, its node_modules, the Go module cache and a short
// list of well-known packages — and a confusable near-miss of one
// (reqeusts, github.com/Sirupsen/logrus, nodemailer-js) gets a line on
// the edit's result and a notice. It is offline and deterministic, it
// never refuses an edit, and a name that is itself known is never
// flagged. It complements the registry lookup (depgate.go), which cannot
// see a squat that is registered. JANETCODE_OFF=typosquat turns it off.

// squatMaxFile is the largest file whose imports are compared: a
// generated bundle is not where a model adds an import by hand.
const squatMaxFile = 1 << 20

// squatFile is a file an edit is about to change, with its content
// before.
type squatFile struct {
	path   string
	before []byte
}

// squatBefore reads the source files and manifests an edit touches,
// before it runs.
func (a *Agent) squatBefore(t tool.Tool, u llm.Block) []squatFile {
	if a.Off["typosquat"] || !editTool(u.Name) || a.DryRunOn() {
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
	var out []squatFile
	for _, p := range paths {
		if !typosquat.Source(p) {
			continue
		}
		if st, err := os.Stat(p); err == nil && st.Size() > squatMaxFile {
			continue
		}
		before, _ := os.ReadFile(p) // a new file has no before
		out = append(out, squatFile{path: p, before: before})
	}
	return out
}

// squatCheck compares what the edit added and returns the note for its
// result, "" when nothing resembles a name it is not.
func (a *Agent) squatCheck(fs []squatFile, emit func(Event)) string {
	var out string
	for _, f := range fs {
		after, err := os.ReadFile(f.path)
		if err != nil || len(after) > squatMaxFile {
			continue
		}
		added := typosquat.Added(f.path, f.before, after)
		if len(added) == 0 {
			continue
		}
		known := typosquat.Load(a.displayRoot(), f.path)
		var found []typosquat.Finding
		for _, n := range added {
			if fd, ok := typosquat.Check(known, n); ok {
				found = append(found, fd)
			}
		}
		note := typosquat.Note(f.path, found)
		if note == "" {
			continue
		}
		emit(Event{Kind: EvNotice, Text: note})
		out += "\n\n" + note
	}
	return out
}
