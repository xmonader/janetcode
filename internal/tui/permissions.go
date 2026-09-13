package tui

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/xmonader/janetcode/internal/agent"
	"github.com/xmonader/janetcode/internal/mcp"
	"github.com/xmonader/janetcode/internal/perm"
)

// permissionsCommand lists rules, adds one, or trusts the project file.
func (m *model) permissionsCommand(args []string) {
	o := m.opts.Perms
	ag := m.opts.Agent
	if len(args) >= 2 && (args[0] == "allow" || args[0] == "deny") {
		r, err := perm.Parse(strings.Join(args[1:], " "))
		if err != nil {
			m.transcript.errorf("%v", err)
			return
		}
		v := perm.Allowed
		if args[0] == "deny" {
			v = perm.Denied
		}
		ag.Rules.Add(v, r)
		if o != nil && o.Save != nil {
			if err := o.Save(args[0], r); err != nil {
				m.transcript.errorf("save rule: %v", err)
				return
			}
		}
		m.note("rule saved: " + args[0] + " " + r.String())
		return
	}
	if len(args) > 0 && args[0] == "trust" {
		switch {
		case o == nil || len(o.Project.Allow) == 0 && len(o.Project.Deny) == 0:
			m.transcript.errorf("no %s in %s", perm.ProjectFile, shortPath(m.opts.Cwd))
		case o.ProjectTrusted:
			m.note("project rules are already trusted")
		default:
			if err := o.Trust.Trust(mcp.PermissionsTrustKey(m.opts.Cwd), o.ProjectHash); err != nil {
				m.transcript.errorf("record trust: %v", err)
				return
			}
			o.ProjectTrusted = true
			ag.Rules = perm.Merge(ag.Rules, o.Project)
			m.note("trusted " + perm.ProjectFile + " · its allow rules are active")
		}
		return
	}
	var sb strings.Builder
	if len(ag.Rules.Allow) == 0 && len(ag.Rules.Deny) == 0 {
		sb.WriteString("no permission rules yet\n")
	}
	for _, r := range ag.Rules.Deny {
		sb.WriteString("deny   " + r.String() + "\n")
	}
	for _, r := range ag.Rules.Allow {
		sb.WriteString("allow  " + r.String() + "\n")
	}
	if o != nil && len(o.Project.Allow) > 0 && !o.ProjectTrusted {
		sb.WriteString(perm.ProjectFile + " has allow rules that are not trusted yet; run /permissions trust to enable them\n")
	}
	if lines := perm.CandidateLines(ag.Ledger, ag.Rules); len(lines) > 0 {
		sb.WriteString("approved often, not saved:\n")
		for _, l := range lines {
			sb.WriteString("  " + l + "\n")
		}
	}
	sb.WriteString("/permissions allow bash(go test *) adds a rule (deny likewise); press r at a prompt to save one for that call")
	m.transcript.info(strings.TrimRight(sb.String(), "\n"))
}

type permissionMsg struct {
	call  agent.ToolCall
	reply chan verdict
}

// verdict is the user's answer to a permission prompt.
type verdict struct {
	d      agent.Decision
	reason string
}

// tuiApprover bridges agent permission requests into the UI loop.
type tuiApprover struct{ send func(tea.Msg) }

func (a *tuiApprover) Approve(ctx context.Context, call agent.ToolCall) (agent.Decision, error) {
	d, _, err := a.ApproveWithReason(ctx, call)
	return d, err
}

func (a *tuiApprover) ApproveWithReason(ctx context.Context, call agent.ToolCall) (agent.Decision, string, error) {
	reply := make(chan verdict, 1)
	a.send(permissionMsg{call: call, reply: reply})
	select {
	case v := <-reply:
		return v.d, v.reason, nil
	case <-ctx.Done():
		return agent.Deny, "", ctx.Err()
	}
}

func (m *model) answer(d agent.Decision, reason string) {
	if m.pending == nil {
		return
	}
	m.pending.reply <- verdict{d: d, reason: reason}
	m.transcript.permissionAnswered(m.pending.call, d)
	call := m.pending.call
	m.pending = nil
	m.pendingClosedAt = time.Now()
	m.denying = false
	m.setPhase("thinking")
	// Named by the rule the r key would save, because that is the shape
	// both hints below are about.
	shape := perm.Suggest(call.Name, perm.Subject(call.Name, call.Input)).String()
	switch d {
	case agent.Deny:
		m.observeDenial(shape)
	case agent.Allow:
		m.observeApproval(shape)
	}
	m.refresh(false)
}

// permissionKeys is the key line of the permission box, in the longest
// wording that fits on one line: the box has a fixed height in the
// layout, so a wrapped key line would run over the input. expandable says
// whether there is more of the call to see: without that, the line used to
// offer ctrl+o even for a two-line diff that was already shown whole.
func (m *model) permissionKeys(tool, rule, learned string, expandable bool) string {
	return m.formatPermissionKeys(tool, rule, learned, expandable, true)
}

// learnedHeadline shortens the ledger's note ("approved 4× before, never
// refused") to what fits on the key line: the count.
func learnedHeadline(s string) string {
	if i := strings.Index(s, ","); i >= 0 {
		s = s[:i]
	}
	return strings.TrimSuffix(s, " before")
}

func (m *model) formatPermissionKeys(tool, rule, learned string, expandable, color bool) string {
	type seg struct{ key, text string }
	// Box width minus its border and padding, with two columns to spare
	// so a line measured at the limit never wraps inside the box.
	room := m.width - 8
	render := func(segs []seg, sep string) (string, int) {
		var sb, plain strings.Builder
		for i, s := range segs {
			if i > 0 {
				if color {
					sb.WriteString(styleDim.Render(sep))
				} else {
					sb.WriteString(sep)
				}
				plain.WriteString(sep)
			}
			if color {
				sb.WriteString(styleKey.Render(s.key) + styleDim.Render(s.text))
			} else {
				sb.WriteString(s.key + s.text)
			}
			plain.WriteString(s.key + s.text)
		}
		out := sb.String()
		if !color {
			out = plain.String()
		}
		return out, lipgloss.Width(plain.String())
	}
	// The learned note rides along with the rule in the two widest
	// layouts and is the first thing dropped when narrowing: it explains
	// why r is worth pressing, but the rule itself is what makes the key
	// useful at all.
	note := ""
	if learned != "" {
		note = " · " + learnedHeadline(learned)
	}
	long := []seg{{"y", " allow once"}, {"a", " always for " + tool + " this session"}, {"r", " save rule " + rule + note}, {"n", " deny (and say why)"}, {"c", " cancel the turn"}}
	if expandable {
		long = append(long, seg{"", "(ctrl+o expands)"})
	}
	if s, w := render(long, "   "); w <= room {
		return s
	}
	// The rule is the only part of the line the user cannot guess, so it
	// is the last thing to go. Shorten it the way the rest of the
	// interface shortens a path first, then spend the remaining width by
	// dropping the descriptions of the keys that need one least: "y once"
	// and "a always for edit_file" say nothing "y" and "a always" do not.
	// Cutting the rule first is what left an 80-column terminal showing
	// "r rule" with no rule at all, which is the one wording that makes
	// the key useless.
	rule = shortenPaths(m.opts.Cwd, rule)
	medium := []seg{{"y", " once"}, {"a", " always for " + tool}, {"r", " rule " + rule + note}, {"n", " deny"}, {"c", " cancel"}}
	if s, w := render(medium, " · "); w <= room {
		return s
	}
	narrow := []seg{{"y", ""}, {"a", " always"}, {"r", " rule " + rule}, {"n", " deny"}, {"c", " cancel"}}
	if s, w := render(narrow, " · "); w <= room {
		return s
	}
	// Only now is the rule itself cut, to whatever the fixed part leaves.
	fixed := lipgloss.Width("y · a always · r rule  · n deny · c cancel")
	if left := room - fixed; left >= 6 && lipgloss.Width(rule) > left {
		narrow[2].text = " rule " + truncateLine(rule, left)
		if s, w := render(narrow, " · "); w <= room {
			return s
		}
	}
	s, _ := render([]seg{{"y", ""}, {"a", " always"}, {"r", " rule"}, {"n", " deny"}, {"c", " cancel"}}, " · ")
	return s
}

// suggestedRule is the rule r would save, written the way the rest of
// the interface writes a path: relative to the working directory, or
// with the home directory as ~. The rule saved to disk stays absolute;
// only the prompt is shortened, because an absolute path truncated in
// the middle told the user nothing about what they were about to allow.
func (m *model) suggestedRule(name string, input json.RawMessage) string {
	r := perm.Suggest(name, perm.Subject(name, input))
	if dir, ok := strings.CutSuffix(r.Pattern, "/*"); ok && filepath.IsAbs(dir) {
		r.Pattern = relTo(m.opts.Cwd, dir) + "/*"
	}
	return r.String()
}

// permissionNotice is the reserved row during a permission prompt: the
// tool, a warning if any, fitted on one line so a warning never grows
// the footer.
func (m *model) permissionNotice() string {
	if m.pending == nil {
		return ""
	}
	c := m.pending.call
	room := m.width - 4
	// Everything here came from the model or the repository; none of it
	// may carry control characters into the terminal.
	line := styleWarn.Bold(true).Render("allow ") + styleTool.Bold(true).Render(sanitize(c.Name)) + " " + styleDim.Render(sanitize(summarizeInput(c.Name, string(c.Input), m.width-len(c.Name)-16, m.opts.Cwd)))
	if c.Warning != "" {
		line += styleErr.Bold(true).Render(" · ⚠ " + sanitize(c.Warning))
	}
	return fitLine("  "+line, room)
}

func (m *model) permissionView() string {
	if m.pending == nil {
		return ""
	}
	return m.permissionNotice() + "\n" + m.permissionKeys(m.pending.call.Name, m.suggestedRule(m.pending.call.Name, m.pending.call.Input), m.pending.call.Learned, pendingExpandable(m.pending.call))
}
