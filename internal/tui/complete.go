package tui

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/xmonader/janetcode/internal/config"
	"github.com/xmonader/janetcode/internal/prompt"
	"github.com/xmonader/janetcode/internal/session"
	"github.com/xmonader/janetcode/internal/textfmt"
)

// completionState is the highlight in the completion menu: which row,
// what the menu is offering, and the input the highlight was set for —
// a keystroke rebuilds the rows, so a highlight that outlives the text it
// belongs to would point at a different command.
type completionState struct {
	idx      int    // highlighted row of the completion menu
	args     bool   // the menu is offering arguments, not commands
	forInput string // input value the highlight belongs to
}

// highlight clamps the highlight for a menu of n rows drawn for input v,
// resetting it whenever the input changed since it was set.
func (c *completionState) highlight(v string, n int) int {
	if v != c.forInput {
		c.forInput, c.idx = v, 0
	}
	if c.idx >= n {
		c.idx = n - 1
	}
	return c.idx
}

// move shifts the highlight by delta around a menu of n rows and pins it
// to the input it now belongs to.
func (c *completionState) move(v string, n, delta, idx int) {
	c.idx = (idx + delta + n) % n
	c.forInput = v
}

// pin puts the highlight back on the first row of the menu for v.
func (c *completionState) pin(v string) {
	c.forInput, c.idx = v, 0
}

func (m *model) completeJobs(partial string) []command {
	p := strings.ToLower(partial)
	var out []command
	if p == "" || strings.HasPrefix("kill", p) {
		out = append(out, command{name: "kill", help: "stop a running job by id"})
	}
	for _, j := range m.overlayJobList() {
		id := strconv.Itoa(j.ID)
		if p != "" && !strings.Contains(strings.ToLower(id+" "+j.Command), p) {
			continue
		}
		out = append(out, command{name: id, help: j.Summary()})
	}
	return out
}

func (m *model) completeAgents(partial string) []command {
	p := strings.ToLower(partial)
	var out []command
	for _, v := range []command{
		{name: "new", help: "start a general subagent"},
		{name: "fork", help: "inherit this conversation"},
		{name: "cancel", help: "stop a running subagent"},
		{name: "steer", help: "inject a note into a running subagent"},
	} {
		if p == "" || strings.HasPrefix(v.name, p) {
			out = append(out, v)
		}
	}
	for _, k := range m.overlayKids() {
		if p != "" && !strings.Contains(strings.ToLower(k.ID+" "+k.Description), p) {
			continue
		}
		help := k.Description
		if !k.Done && k.Last != "" {
			help = k.Last
		}
		out = append(out, command{name: k.ID, help: help})
	}
	return out
}

func (m *model) completeSession(partial string) []command {
	if m.opts.Session == nil {
		return nil
	}
	infos, err := session.List(dirOf(m.opts.Session), m.opts.Cwd)
	if err != nil {
		return nil
	}
	var out []command
	for _, in := range infos {
		if in.Meta.ID == m.opts.Session.Meta.ID || in.Messages == 0 {
			continue
		}
		name := "/resume " + in.Meta.ID
		if partial != "" && !strings.Contains(strings.ToLower(in.Meta.ID+" "+in.Label()), strings.ToLower(partial)) {
			continue
		}
		out = append(out, command{name: name, args: textfmt.Age(time.Since(in.Updated)), help: textfmt.Plural(in.Messages, "msg") + " · " + in.Label()})
		if len(out) >= 20 {
			break
		}
	}
	return out
}

func (m *model) completeModel(partial string) []command {
	if m.opts.Config == nil {
		return nil
	}
	seen := map[string]bool{}
	var out []command
	// The slots come first: they are words, not ids, and a user who has
	// never seen /model strong will not guess it from a list of models.
	for _, s := range config.SlotNames {
		if partial != "" && !strings.HasPrefix(s, strings.ToLower(partial)) {
			continue
		}
		out = append(out, command{name: "/model " + s, help: m.slotLine(s)})
	}
	for _, n := range m.opts.Config.ProviderNames() {
		p := m.opts.Config.Providers[n]
		if p.Model == "" || seen[p.Model] {
			continue
		}
		seen[p.Model] = true
		if partial != "" && !strings.Contains(strings.ToLower(p.Model), strings.ToLower(partial)) {
			continue
		}
		out = append(out, command{name: "/model " + p.Model, help: "configured for " + n})
	}
	return out
}

func (m *model) completeMemory(partial string) []command {
	if m.opts.Memory == nil {
		return nil
	}
	var out []command
	for _, e := range m.opts.Memory.Entries() {
		if partial != "" && !strings.Contains(strings.ToLower(e.Name), strings.ToLower(partial)) {
			continue
		}
		out = append(out, command{name: "/forget " + e.Name, help: e.Description})
	}
	return out
}

func (m *model) completeUndo(partial string) []command {
	out := []command{
		{name: "/undo preview", help: "list what would be reverted"},
		{name: "/undo about", help: "preview only the changes matching a description"},
		{name: "/undo apply", help: "carry out the last /undo about or /undo turn preview"},
		{name: "/undo apply --force", help: "same, even if the preview has mixed changes or a coupling warning"},
	}
	if m.opts.Agent == nil {
		return out
	}
	for _, t := range m.opts.Agent.RestorableTurns() {
		out = append(out, command{name: fmt.Sprintf("/undo turn %d", t), help: "put the work tree and the conversation back to before that turn (--files-only keeps the conversation)"})
	}
	for i, c := range m.opts.Agent.Changes() {
		if c.Undone {
			continue
		}
		out = append(out, command{name: fmt.Sprintf("/undo %d", i+1), help: m.relPath(c.Path)})
	}
	if partial == "" {
		return out
	}
	var hit []command
	for _, c := range out {
		if strings.Contains(c.name+" "+c.help, partial) {
			hit = append(hit, c)
		}
	}
	return hit
}

// completeFile offers paths for the commands that take one.
func (m *model) completeFile(partial string) []command {
	name := "/open "
	if strings.HasPrefix(m.input.Value(), "/diff") {
		name = "/diff "
	}
	var out []command
	for _, p := range prompt.CompletePath(m.opts.Cwd, partial) {
		help := "file"
		if strings.HasSuffix(p, "/") {
			help = "directory"
		}
		out = append(out, command{name: name + p, help: help})
	}
	return out
}
