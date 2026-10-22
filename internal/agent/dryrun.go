package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/xmonader/janetcode/internal/cmdresult"
	"github.com/xmonader/janetcode/internal/diff"
	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/perm"
	"github.com/xmonader/janetcode/internal/session"
	"github.com/xmonader/janetcode/internal/shell"
	"github.com/xmonader/janetcode/internal/textfmt"
	"github.com/xmonader/janetcode/internal/tool"
)

// A dry run (--dry-run, /dryrun) lets the model reason, read and plan the
// whole change while nothing it does reaches the workspace or the world:
//
//   - the file tools write into an overlay (tool.Overlay) the file tools
//     themselves read back, so a dry-run turn can edit a file twice and
//     see its own work, and the preview is the real diff;
//   - a shell command runs only when it is a known-safe read (the
//     allowlist auto mode uses) and not in the background; any other is
//     recorded as a command that would run;
//   - an MCP tool the server does not mark read-only, a job's stdin or
//     kill, a script, an editing subagent, the terminal and the computer
//     are recorded, never called;
//   - fetch_url and web_search run (they read) and their hosts are listed.
//
// Nothing that is recorded asks for approval: a preview is not an
// approval, and the user decides at the end — /dryrun apply writes the
// previewed files (as an undoable checkpoint), and every command the
// model planned then goes through the ordinary permission path when the
// model runs it for real.

// DryAction is one call a dry run previewed.
type DryAction struct {
	N    int    `json:"n"`
	Turn int    `json:"turn"`
	Tool string `json:"tool"`
	// Kind is "file" (written to the overlay), "command" (a shell
	// command that was not run), "mcp" (an MCP tool that was not
	// called), "network" (a read that did run, and the host it went
	// to), "subagent" or "other".
	Kind    string `json:"kind"`
	Summary string `json:"summary"`
	Path    string `json:"path,omitempty"`
	Host    string `json:"host,omitempty"`
	// Would is what the ordinary permission path would have done with
	// the call: "ask", "allow" or "deny (rule)", and why when a guard
	// insists on asking.
	Would string `json:"would,omitempty"`
}

// dryRun is a session's dry-run state. A fork shares its parent's (the
// fork writes into the same workspace, so into the same overlay).
type dryRun struct {
	mu      sync.Mutex
	on      bool
	overlay *tool.Overlay
	actions []DryAction
	seq     int
}

const dryRunSystem = "\n\n# Dry run\nThis session is a dry run. Plan and carry out the change as you normally would, but know what happens to each call: write_file and edit_file write to a preview the user reviews, and read_file, grep and glob see that preview, so continue editing on top of your own changes. Shell commands run only when they merely read (ls, cat, grep, git status/diff/log …), and they read the disk, not the preview; any other command, MCP tool or background job is recorded for the user and not run, and its result says so — do not retry it, and do not claim it succeeded or report output it never produced. When the change is complete, stop and summarise what the preview contains and which commands you would run to verify it. The user applies the preview with /dryrun apply."

// dryState is the session's dry-run state, made on first use. A fork
// uses its parent's.
func (a *Agent) dryState() *dryRun {
	if a.UndoOwner != nil {
		return a.UndoOwner.dryState()
	}
	a.dryOnce.Do(func() {
		if a.dry == nil {
			a.dry = &dryRun{}
		}
	})
	return a.dry
}

// dryPending is how many paths the dry run's overlay holds: what a
// read-only shell command, which reads the disk, did not see.
func (a *Agent) dryPending() int {
	d := a.dryState()
	d.mu.Lock()
	o := d.overlay
	d.mu.Unlock()
	if o == nil {
		return 0
	}
	return o.Len()
}

// dryShellNote is appended to a read-only shell command's output in a
// dry run with pending changes: the shell reads the disk, so what it
// printed is the tree before the preview, and a model that trusted it
// would conclude its own edits never happened.
func (a *Agent) dryShellNote(name string) string {
	if name != "bash" || !a.DryRunOn() {
		return ""
	}
	n := a.dryPending()
	if n == 0 {
		return ""
	}
	files := "files have"
	if n == 1 {
		files = "file has"
	}
	return fmt.Sprintf("\n[dry run: this command read the disk, not the preview — %d %s pending changes it did not see; read_file, grep and glob see them]", n, files)
}

// DryRunOn reports whether the session is a dry run.
func (a *Agent) DryRunOn() bool {
	d := a.dryState()
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.on
}

// workspaces are the distinct workspaces the session's file tools write
// through: where a dry run's overlay goes.
func (a *Agent) workspaces() []*tool.Workspace {
	var out []*tool.Workspace
	seen := map[*tool.Workspace]bool{}
	if a.Tools == nil {
		return nil
	}
	for _, d := range a.Tools.Defs() {
		t, ok := a.Tools.Get(d.Name)
		if !ok {
			continue
		}
		if wb, ok := t.(tool.WorkspaceBound); ok {
			if ws := wb.Workspace(); ws != nil && !seen[ws] {
				seen[ws] = true
				out = append(out, ws)
			}
		}
	}
	return out
}

// SetDryRun turns the dry run on or off. Turning it off discards a
// preview that was not applied; the returned count says how many previewed
// actions went with it, so a front-end can say so.
func (a *Agent) SetDryRun(on bool) (discarded int) {
	d := a.dryState()
	d.mu.Lock()
	defer d.mu.Unlock()
	if on == d.on {
		return 0
	}
	d.on = on
	discarded = len(d.actions)
	d.actions, d.seq = nil, 0
	d.overlay = nil
	if on {
		d.overlay = tool.NewOverlay()
		discarded = 0
	}
	for _, ws := range a.workspaces() {
		ws.SetOverlay(d.overlay)
	}
	a.logEvent(session.Event{Type: session.TypeNotice, Turn: a.turnN, Output: "dry run " + onOffWord(on)})
	return discarded
}

func onOffWord(on bool) string {
	if on {
		return "on"
	}
	return "off"
}

// DryRunActions lists what the dry run has previewed so far.
func (a *Agent) DryRunActions() []DryAction {
	d := a.dryState()
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]DryAction(nil), d.actions...)
}

func (a *Agent) recordDry(act DryAction) {
	d := a.dryState()
	d.mu.Lock()
	defer d.mu.Unlock()
	d.seq++
	act.N, act.Turn = d.seq, a.turnN
	d.actions = append(d.actions, act)
}

// dryMutating reports whether a call is one a dry run must not simply
// carry out: every mutating call, and a task that would start an editing
// subagent (Task is not a mutating tool, because its default subagent
// only reads).
func dryMutating(t tool.Tool, input json.RawMessage) bool {
	if tool.MutatingCall(t, input) {
		return true
	}
	if t.Name() == "task" {
		var in struct {
			Edit bool `json:"edit"`
		}
		_ = json.Unmarshal(input, &in)
		return in.Edit
	}
	return false
}

// dryIntercept is the dry run's one decision about a call, made in
// executeTool and runNested right after the mode gate: handled false lets
// the call run as usual (it only reads); handled true means the call was
// written to the overlay or recorded, out is what the model is told, and
// the call's events are already emitted and logged.
func (a *Agent) dryIntercept(ctx context.Context, t tool.Tool, u *llm.Block, call *ToolCall, intent string, emit func(Event)) (out string, handled bool) {
	if !a.DryRunOn() || !dryMutating(t, u.Input) {
		return "", false
	}
	would := a.dryWould(ctx, t, u)
	switch name := t.Name(); {
	case name == "fetch_url" || name == "web_search":
		// A read. It runs, through the ordinary permission path, and the
		// preview lists where it went.
		a.recordDry(DryAction{Tool: name, Kind: "network", Host: dryHost(u.Input), Summary: dryArgs(u.Input), Would: would})
		return "", false
	case name == "bash" && dryReadOnlyCommand(u.Input):
		return "", false
	}
	emit(Event{Kind: EvToolRun, Call: call})
	a.logEvent(session.Event{Type: session.TypeToolCall, CallID: u.ID, Name: u.Name, Arguments: string(u.Input), Intent: intent})
	finish := func(out string, isErr bool) (string, bool) {
		emit(Event{Kind: EvToolResult, Call: call, Output: out, IsError: isErr})
		a.logEvent(session.Event{Type: session.TypeToolResult, CallID: u.ID, Name: u.Name, Arguments: string(u.Input), Output: out, IsError: isErr})
		return out, true
	}
	if wb, ok := t.(tool.WorkspaceBound); ok && wb.Workspace() != nil && wb.Workspace().Overlay() != nil {
		res, _, err := a.runSafely(ctx, t, u.Input)
		if err != nil {
			return finish("Error: "+err.Error(), true)
		}
		path := ""
		if tc, ok := t.(tool.Toucher); ok {
			if ps, err := tc.Touches(u.Input); err == nil && len(ps) > 0 {
				path = ps[0]
			}
		}
		a.recordDry(DryAction{Tool: t.Name(), Kind: "file", Path: path, Summary: textfmt.RelTo(a.displayRoot(), path), Would: would})
		return finish(res+"\n[dry run: this went to the preview, not the disk; read_file shows it]", false)
	}
	act := DryAction{Tool: t.Name(), Kind: "other", Summary: dryArgs(u.Input), Would: would}
	switch {
	case t.Name() == "bash":
		act.Kind, act.Summary = "command", shell.CommandOf(string(u.Input))
	case t.Name() == "task":
		act.Kind = "subagent"
	case strings.HasPrefix(t.Name(), "mcp__"):
		act.Kind = "mcp"
	}
	a.recordDry(act)
	return finish(fmt.Sprintf("[dry run] %s was not run: this session is a dry run, so the call was recorded for the user's review instead. There is no output. Do not retry it; carry on planning as if it would succeed, and say in your summary that it still has to be run.", t.Name()), false)
}

// dryWould is what the ordinary permission path would do with the call,
// for the preview: a deny rule, a guard that always asks, or the mode's
// own answer. Nothing is asked.
func (a *Agent) dryWould(ctx context.Context, t tool.Tool, u *llm.Block) string {
	_, verdict, rule, hard := a.ruleDecision(u)
	switch {
	case hard != "":
		return "deny (" + hard + ")"
	case verdict == perm.Denied:
		return "deny (" + rule.String() + ")"
	}
	var c ToolCall
	if a.describeCall(ctx, t, u, &c) {
		if c.Warning != "" {
			return "ask (" + c.Warning + ")"
		}
		return "ask"
	}
	if a.mustPrompt(t, u, false, verdict, false) {
		return "ask"
	}
	return "allow"
}

// dryReadOnlyCommand reports whether a bash call may run in a dry run: a
// known-safe read in the foreground.
func dryReadOnlyCommand(input json.RawMessage) bool {
	var in struct {
		Command    string `json:"command"`
		Background bool   `json:"background"`
	}
	if json.Unmarshal(input, &in) != nil || in.Background {
		return false
	}
	return shell.SafeRead(in.Command)
}

// dryHost is the host a fetch_url call reaches, or the query of a search.
func dryHost(input json.RawMessage) string {
	var in struct {
		URL   string `json:"url"`
		Query string `json:"query"`
	}
	_ = json.Unmarshal(input, &in)
	if u, err := url.Parse(in.URL); err == nil && u.Host != "" {
		return u.Host
	}
	return in.Query
}

// dryArgs is a call's arguments on one clipped line.
func dryArgs(input json.RawMessage) string {
	return textfmt.Clip(strings.Join(strings.Fields(string(input)), " "), 160)
}

// DryFile is one path the overlay holds, as the preview shows it: the
// diff against the disk and what applying it would do.
type DryFile struct {
	Path string `json:"path"`
	Diff string `json:"diff"`
	Add  int    `json:"added"`
	Del  int    `json:"removed"`
	Gone bool   `json:"delete,omitempty"`
	New  bool   `json:"create,omitempty"`
}

// DryRunFiles are the files the preview would change, numbered from 1 in
// the order /dryrun apply's numbers use.
func (a *Agent) DryRunFiles() []DryFile { return a.dryFiles() }

// dryFiles are the overlay's paths with their diffs against the disk, in
// the order they were first written.
func (a *Agent) dryFiles() []DryFile {
	d := a.dryState()
	d.mu.Lock()
	o := d.overlay
	d.mu.Unlock()
	if o == nil {
		return nil
	}
	root := a.displayRoot()
	var out []DryFile
	for _, c := range o.Changes() {
		before, err := os.ReadFile(c.Path)
		f := DryFile{Path: c.Path, Gone: c.Gone, New: err != nil}
		after := string(c.Data)
		if c.Gone {
			after = ""
		}
		if !f.New && !c.Gone && string(before) == after {
			continue // written back to what it was
		}
		f.Diff = diff.Unified(textfmt.RelTo(root, c.Path), string(before), after, 3)
		// The ---/+++ header is the lines before the first @@; past it a
		// removed "-- note" or an added "++x" is a changed line like any
		// other.
		inHunk := false
		for _, l := range strings.Split(f.Diff, "\n") {
			switch {
			case strings.HasPrefix(l, "@@"):
				inHunk = true
			case !inHunk:
			case strings.HasPrefix(l, "+"):
				f.Add++
			case strings.HasPrefix(l, "-"):
				f.Del++
			}
		}
		out = append(out, f)
	}
	return out
}

// DryRunReport is the whole preview: the files that would change with
// their diffs (when full), the commands and tools that were not run, the
// hosts reached, and how the ordinary path would treat each.
func (a *Agent) DryRunReport(full bool) string {
	var sb strings.Builder
	files := a.dryFiles()
	acts := a.DryRunActions()
	root := a.displayRoot()
	fmt.Fprintf(&sb, "dry run · nothing has been changed on disk\n")
	s := a.Settings()
	sandbox := "off"
	if a.Sandbox.Enabled() {
		sandbox = a.Sandbox.ProfileName()
	}
	fmt.Fprintf(&sb, "mode %s · sandbox %s\n", s.ModeName(), sandbox)
	if len(files) == 0 && len(acts) == 0 {
		sb.WriteString("\nnothing previewed yet\n")
		return sb.String()
	}
	if len(files) > 0 {
		fmt.Fprintf(&sb, "\nfiles that would change (%d)\n", len(files))
		for i, f := range files {
			verb := "edit"
			switch {
			case f.Gone:
				verb = "delete"
			case f.New:
				verb = "create"
			}
			fmt.Fprintf(&sb, "  %d  %-6s %s  +%d -%d\n", i+1, verb, textfmt.RelTo(root, f.Path), f.Add, f.Del)
		}
	}
	section := func(kind, title string) {
		var rows []DryAction
		for _, x := range acts {
			if x.Kind == kind {
				rows = append(rows, x)
			}
		}
		if len(rows) == 0 {
			return
		}
		fmt.Fprintf(&sb, "\n%s (%d)\n", title, len(rows))
		for _, x := range rows {
			line := x.Summary
			if x.Host != "" {
				line = x.Host
			}
			fmt.Fprintf(&sb, "  %-10s %s", x.Tool, line)
			if x.Would != "" && kind != "network" {
				fmt.Fprintf(&sb, "   · would %s", x.Would)
			}
			sb.WriteString("\n")
		}
	}
	section("command", "commands not run")
	section("mcp", "MCP tools not called")
	section("subagent", "editing subagents not started")
	section("other", "other calls not made")
	section("network", "network reads that ran")
	if a.Verify.Enabled() {
		fmt.Fprintf(&sb, "\nafter applying, the verify gate runs: %s\n", strings.Join(a.Verify.CommandNames(), ", "))
	}
	if full {
		for _, f := range files {
			sb.WriteString("\n" + f.Diff)
			if !strings.HasSuffix(f.Diff, "\n") {
				sb.WriteString("\n")
			}
		}
	}
	sb.WriteString("\n/dryrun apply writes every file · /dryrun apply 1,3 writes some · /dryrun show adds the diffs · /dryrun discard drops the preview · /dryrun off ends the dry run")
	return sb.String()
}

// ErrDryRunOff is returned for a dry-run action outside a dry run.
var ErrDryRunOff = errors.New("this session is not a dry run: /dryrun on starts one")

// ApplyDryRun writes the previewed files — all of them, or the ones
// numbered in pick (1-based, as DryRunReport lists them) — to the disk as
// one undoable checkpoint, ends the dry run, and tells the model what was
// applied and which of its planned commands never ran.
func (a *Agent) ApplyDryRun(ctx context.Context, pick []int) (cmdresult.Result, error) {
	if a.running.Load() {
		return cmdresult.Result{}, ErrTurnRunning
	}
	if !a.DryRunOn() {
		return cmdresult.Result{}, ErrDryRunOff
	}
	files := a.dryFiles()
	chosen := files
	if len(pick) > 0 {
		chosen = nil
		for _, n := range pick {
			if n < 1 || n > len(files) {
				return cmdresult.Result{}, fmt.Errorf("no previewed file %d: the preview lists %d", n, len(files))
			}
			chosen = append(chosen, files[n-1])
		}
	}
	d := a.dryState()
	d.mu.Lock()
	o := d.overlay
	d.mu.Unlock()
	content := map[string]tool.OverlayChange{}
	if o != nil {
		for _, c := range o.Changes() {
			content[c.Path] = c
		}
	}
	commands := a.DryRunActions()
	// Off first: the writes below must reach the disk, not the overlay.
	a.SetDryRun(false)
	var paths []string
	for _, f := range chosen {
		paths = append(paths, f.Path)
	}
	root := a.displayRoot()
	res := cmdresult.Result{SheetTitle: "dry run applied"}
	if len(paths) > 0 {
		a.cp.beginTurn(a.turnN, len(a.Messages))
		at := dryApplyTool{paths: paths}
		a.snapshotBefore(ctx, at, nil, nil)
		pend := a.before(at, nil)
		var errs []error
		for _, p := range paths {
			c := content[p]
			var err error
			if c.Gone {
				if err = os.Remove(p); errors.Is(err, os.ErrNotExist) {
					err = nil
				}
			} else {
				err = a.writeReal(ctx, p, c.Data)
			}
			if err != nil {
				errs = append(errs, fmt.Errorf("%s: %w", textfmt.RelTo(root, p), err))
			}
		}
		a.after(llm.Block{ID: "dry-run-apply", Name: "dry_run_apply"}, "apply the dry-run preview", pend)
		a.endTurn(ctx)
		if len(errs) > 0 {
			res.Error = errors.Join(errs...).Error()
		}
	}
	var sb strings.Builder
	for _, p := range paths {
		fmt.Fprintf(&sb, "wrote %s\n", textfmt.RelTo(root, p))
	}
	var notRun []string
	for _, x := range commands {
		if x.Kind != "file" && x.Kind != "network" {
			notRun = append(notRun, x.Tool+": "+x.Summary)
		}
	}
	if len(notRun) > 0 {
		sb.WriteString("\nnot run — the model will run these for real, each through the permission prompt:\n")
		for _, c := range notRun {
			sb.WriteString("  " + c + "\n")
		}
	}
	if len(paths) == 0 {
		sb.WriteString("no files were written\n")
	}
	res.Sheet = strings.TrimRight(sb.String(), "\n")
	res.Notes = []string{"dry run off · /undo takes the applied files back"}
	note := "[harness] The user applied the dry-run preview and ended the dry run."
	if len(paths) > 0 {
		rel := make([]string, len(paths))
		for i, p := range paths {
			rel[i] = textfmt.RelTo(root, p)
		}
		note += " These files were written to disk as previewed: " + strings.Join(rel, ", ") + "."
	}
	if skipped := len(files) - len(chosen); skipped > 0 {
		note += fmt.Sprintf(" %d previewed file(s) were NOT applied and are as they were on disk.", skipped)
	}
	if len(notRun) > 0 {
		note += " None of the commands or tools the dry run recorded were run: " + strings.Join(notRun, "; ") + ". Run the ones still needed now; each goes through the normal permission path."
	}
	a.appendHarnessText(note)
	return res, nil
}

// writeReal writes a previewed file through the workspace's real host
// (a remote one included), or to the local disk without one.
func (a *Agent) writeReal(ctx context.Context, p string, data []byte) error {
	for _, ws := range a.workspaces() {
		if h := ws.RealHost(); h != nil {
			return h.WriteFile(ctx, p, data, 0o644)
		}
	}
	return os.WriteFile(p, data, 0o644)
}

// dryApplyTool stands for /dryrun apply on the checkpoint path: it names
// the files the apply is about to write so they are copied first and the
// apply is one /undo away.
type dryApplyTool struct{ paths []string }

func (dryApplyTool) Name() string                                         { return "dry_run_apply" }
func (dryApplyTool) Description() string                                  { return "applies a dry-run preview" }
func (dryApplyTool) Schema() json.RawMessage                              { return json.RawMessage(`{"type":"object"}`) }
func (dryApplyTool) Mutating() bool                                       { return true }
func (dryApplyTool) Run(context.Context, json.RawMessage) (string, error) { return "", nil }
func (t dryApplyTool) Touches(json.RawMessage) ([]string, error)          { return t.paths, nil }

// DryRunCommand runs "/dryrun [on|off|show|apply [n,m…]|discard]" for
// every front-end.
func (a *Agent) DryRunCommand(ctx context.Context, arg string) (cmdresult.Result, error) {
	fields := strings.Fields(arg)
	sub := ""
	if len(fields) > 0 {
		sub = strings.ToLower(fields[0])
	}
	switch sub {
	case "":
		if a.DryRunOn() {
			return a.dryOff(), nil
		}
		return a.dryOn(), nil
	case "on":
		return a.dryOn(), nil
	case "off":
		return a.dryOff(), nil
	case "status", "list", "preview":
		if !a.DryRunOn() {
			return cmdresult.Result{}, ErrDryRunOff
		}
		return cmdresult.Result{Sheet: a.DryRunReport(false), SheetTitle: "dry run"}, nil
	case "show", "diff":
		if !a.DryRunOn() {
			return cmdresult.Result{}, ErrDryRunOff
		}
		return cmdresult.Result{Sheet: a.DryRunReport(true), SheetTitle: "dry run"}, nil
	case "discard":
		if !a.DryRunOn() {
			return cmdresult.Result{}, ErrDryRunOff
		}
		n := a.SetDryRun(false)
		a.SetDryRun(true)
		a.appendHarnessText("[harness] The user discarded the dry-run preview: none of the previewed changes exist any more, and the files are as they are on disk. The session is still a dry run.")
		return cmdresult.Result{Notes: []string{fmt.Sprintf("preview discarded (%d action(s)) · still a dry run", n)}}, nil
	case "apply":
		pick, err := parsePick(strings.Join(fields[1:], " "))
		if err != nil {
			return cmdresult.Result{}, err
		}
		return a.ApplyDryRun(ctx, pick)
	}
	return cmdresult.Result{}, fmt.Errorf("usage: /dryrun [on|off|show|apply [n,m…]|discard]")
}

func (a *Agent) dryOn() cmdresult.Result {
	a.SetDryRun(true)
	return cmdresult.Result{Notes: []string{"dry run on · edits go to a preview, commands that change things are recorded, not run · /dryrun show reviews it"}}
}

func (a *Agent) dryOff() cmdresult.Result {
	n := a.SetDryRun(false)
	note := "dry run off"
	if n > 0 {
		note += fmt.Sprintf(" · the preview (%d action(s)) was discarded; /dryrun apply would have written it", n)
		a.appendHarnessText("[harness] The dry run ended without applying its preview: none of the previewed file changes were written, and none of the recorded commands ran. The files are as they are on disk.")
	}
	return cmdresult.Result{Notes: []string{note}}
}

// parsePick reads "1,3 4" into [1 3 4].
func parsePick(s string) ([]int, error) {
	var out []int
	for _, f := range strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ' ' }) {
		n, err := strconv.Atoi(f)
		if err != nil {
			return nil, fmt.Errorf("apply takes file numbers from /dryrun show, like 1,3: %q is not one", f)
		}
		out = append(out, n)
	}
	sort.Ints(out)
	return out, nil
}
