package tui

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/xmonader/janetcode/internal/agent"
	"github.com/xmonader/janetcode/internal/i18n"
	"github.com/xmonader/janetcode/internal/keymap"
	"github.com/xmonader/janetcode/internal/perm"
	"github.com/xmonader/janetcode/internal/present"
	"github.com/xmonader/janetcode/internal/trust"
)

// permissionsCommand lists rules, adds one, or trusts the project file.
func (m *model) permissionsCommand(args []string) {
	o := m.opts.Perms
	ag := m.opts.Agent
	if len(args) == 1 && args[0] == "why" {
		m.transcript.info(ag.WhyLastAsk())
		return
	}
	if len(args) >= 2 && args[0] == "check" {
		m.transcript.info(ag.PermissionCheck(args[1:]))
		return
	}
	if len(args) >= 2 && (args[0] == "allow" || args[0] == "deny" || args[0] == "ask") {
		r, err := perm.Parse(strings.Join(args[1:], " "))
		if err != nil {
			m.transcript.errorf("%v", err)
			return
		}
		v := perm.Allowed
		switch args[0] {
		case "deny":
			v = perm.Denied
		case "ask":
			v = perm.Ask
		}
		if !ag.AddRule(v, r) && v == perm.Allowed && ag.Policy != nil && ag.Policy.ManagedRulesOnly {
			m.transcript.errorf("%v", ag.Policy.Refusal("a saved allow rule"))
			return
		}
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
		case m.execTrustBlocked():
		default:
			if err := o.Trust.Trust(trust.PermissionsKey(m.opts.Cwd), o.ProjectHash); err != nil {
				m.transcript.errorf("record trust: %v", err)
				return
			}
			o.ProjectTrusted = true
			ag.MergeRules(o.Project)
			m.note("trusted " + perm.ProjectFile + " · its allow rules are active")
		}
		return
	}
	var sb strings.Builder
	rules := ag.RulesSnapshot()
	sb.WriteString(present.PermissionSummary(ag.Permissions(), "") + "\n\n")
	if o != nil && len(o.Project.Allow) > 0 && !o.ProjectTrusted {
		sb.WriteString(perm.ProjectFile + " has allow rules that are not trusted yet; run /permissions trust to enable them\n")
	}
	if lines := perm.CandidateLines(ag.Ledger, rules); len(lines) > 0 {
		sb.WriteString("approved often, not saved:\n")
		for _, l := range lines {
			sb.WriteString("  " + l + "\n")
		}
	}
	sb.WriteString("shift+tab cycles ask → plan → auto → yolo · r at a prompt saves a rule\n/permissions allow bash(cmd:go test *) adds one (deny and ask likewise) · /permissions check <command> says which rule decides it")
	m.transcript.info(strings.TrimRight(sb.String(), "\n"))
}

type permissionMsg struct {
	// ctx is the asking caller's: once it is done nobody reads reply.
	ctx   context.Context
	call  agent.ToolCall
	reply chan verdict
	// draft is what was in the composer when the prompt came up. The
	// prompt's key line is drawn only in an empty composer, and n turns
	// the composer into the denial reason, so a message being typed when
	// the prompt arrived hid the keys and was then wiped by n. It is put
	// aside for the prompt and put back when the prompt closes.
	draft string
}

// waitedOn reports whether the caller is known to be waiting for this
// answer still. A prompt without a context is taken to belong to the turn.
func (p *permissionMsg) waitedOn() bool { return p.ctx != nil && p.ctx.Err() == nil }

// showPermission puts a prompt up.
func (m *model) showPermission(msg permissionMsg) {
	// The ask dialog owns the composer while it is up (an "Other" answer
	// is typed there) and keeps a draft of its own; this prompt leaves it
	// be.
	if m.asking == nil {
		msg.draft = m.input.Value()
		if msg.draft != "" {
			m.setInput("")
		}
	}
	m.pending = &msg
	m.rung = msg.call.RuleIndex
	m.turn.setPhase("waiting for your approval")
	m.term.announce("janetcode", "approval needed: "+msg.call.Name)
	m.layout() // an impact line claims a footer row
	m.refresh(false)
}

// nextPermission puts up the prompt that was waiting behind the one just
// closed, skipping any whose caller has stopped waiting.
func (m *model) nextPermission() {
	for m.pending == nil && len(m.waitingPerms) > 0 {
		next := m.waitingPerms[0]
		m.waitingPerms = m.waitingPerms[1:]
		if next.ctx != nil && next.ctx.Err() != nil {
			continue
		}
		m.showPermission(next)
	}
}

// verdict is the user's answer to a permission prompt.
type verdict struct {
	d      agent.Decision
	reason string
	// rule is which rung of the call's ladder an "always" answer chose,
	// "" for the one the prompt started on (see agent.ToolCall.Rungs).
	rule string
}

// tuiApprover bridges agent permission requests into the UI loop.
type tuiApprover struct{ send func(tea.Msg) }

func (a *tuiApprover) Approve(ctx context.Context, call agent.ToolCall) (agent.Decision, error) {
	d, _, err := a.ApproveWithReason(ctx, call)
	return d, err
}

func (a *tuiApprover) ApproveWithReason(ctx context.Context, call agent.ToolCall) (agent.Decision, string, error) {
	d, reason, _, err := a.ApproveWithRule(ctx, call)
	return d, reason, err
}

func (a *tuiApprover) ApproveWithRule(ctx context.Context, call agent.ToolCall) (agent.Decision, string, string, error) {
	if a == nil || a.send == nil {
		// Nothing can be asked, so nothing is allowed. The asker says the
		// same thing the same way; an approver with no window used to be
		// a nil dereference inside the turn goroutine.
		return agent.Deny, "", "", errors.New("approve: no UI")
	}
	reply := make(chan verdict, 1)
	a.send(permissionMsg{ctx: ctx, call: call, reply: reply})
	select {
	case v := <-reply:
		return v.d, v.reason, v.rule, nil
	case <-ctx.Done():
		return agent.Deny, "", "", ctx.Err()
	}
}

// closePending takes the permission prompt down. It is the only way the
// prompt closes, because the prompt is two fields and they have to move
// together: denying is the prompt's second state, not a mode of its own,
// and a turn that ended while the user was typing a denial reason used
// to clear pending and leave denying set. The next call's prompt then
// came up already collecting a reason — y and n went into the textarea,
// the status line said "denied <the previous call>", and the only way
// out was to guess that enter would deny.
func (m *model) closePending() {
	hadImpact := m.pending != nil && m.pending.call.Impact != ""
	draft := ""
	if m.pending != nil {
		draft = m.pending.draft
	}
	wasDenying := m.denying
	if wasDenying {
		m.input.Reset() // the reason went with the answer, or nowhere
	}
	m.pending = nil
	m.denying = false
	m.pendingClosedAt = time.Now()
	switch {
	case draft != "":
		m.setInput(draft) // lays out too
	case hadImpact || wasDenying:
		m.layout() // give the impact row, and the reason's rows, back
	}
}

func (m *model) answer(d agent.Decision, reason string) {
	if m.pending == nil {
		return
	}
	m.pending.reply <- verdict{d: d, reason: reason, rule: m.rungRule()}
	m.transcript.permissionAnswered(m.pending.call, d)
	call := m.pending.call
	if d != agent.Deny && m.diffSeenWhole(call) {
		m.opts.Agent.MarkReviewed(call.ID)
	}
	m.closePending()
	m.turn.setPhase("thinking")
	// A cancelling answer moves the queue on only once the cancel has
	// been sent (see the c key): the prompts behind it from the same
	// turn are about to be abandoned, and one put up now would ask about
	// a call that will never run.
	if reason != agent.CancelledReason {
		defer m.nextPermission()
	}
	// Named by the rule the r key would save, because that is the shape
	// both hints below are about — the highlighted rung, if the user
	// moved along the ladder before answering.
	shape := call.Rule
	if d == agent.AllowAlways {
		if r := rungAt(call, m.rung); r.Rule != "" {
			shape = r.Rule
		}
	}
	switch d {
	case agent.Deny:
		m.observeDenial(shape)
	case agent.Allow:
		m.observeApproval(shape)
	}
	m.refresh(false)
}

// diffSeenWhole says the prompt being answered had the call's whole diff
// on screen: it fit unexpanded, or the reader expanded it (ctrl+o, or
// /verbose for everything). Approving such a call is approving lines that
// were read, so its changes are not comprehension debt (see package
// tour); one approved with its diff clipped is.
func (m *model) diffSeenWhole(c agent.ToolCall) bool {
	if !c.PreviewShown() {
		return false
	}
	if !pendingExpandable(c) || m.verbose {
		return true
	}
	e := m.transcript.findTool(c.ID)
	return e != nil && e.expanded
}

// learnedHeadline shortens the ledger's note ("approved 4× before, never
// refused") to what fits on the key line: the count.
func learnedHeadline(s string) string {
	if i := strings.Index(s, ","); i >= 0 {
		s = s[:i]
	}
	return strings.TrimSuffix(s, " before")
}

// pendingKeys is the key line for the pending permission prompt, shown as
// the input box's placeholder (see placeholder).
func (m *model) pendingKeys() string {
	c := m.pending.call
	rule, learned := c.Rule, c.Learned
	if r := rungAt(c, m.rung); r.Rule != "" {
		rule, learned = r.Rule, r.Learned
	}
	return m.permissionKeysAt(c.Name, m.suggestedRule(rule), learned, pendingExpandable(c), m.rung, len(c.Rungs))
}

// rungRule is the rule the highlighted rung of the pending prompt would
// save, "" when the call has no ladder.
func (m *model) rungRule() string {
	if m.pending == nil {
		return ""
	}
	return rungAt(m.pending.call, m.rung).Rule
}

// rungAt is the i-th rung of a call's ladder, the zero rung when there
// is none there: a call whose front-end never moved, or none at all.
func rungAt(c agent.ToolCall, i int) agent.RuleRung {
	if i < 0 || i >= len(c.Rungs) {
		return agent.RuleRung{}
	}
	return c.Rungs[i]
}

// moveRung slides the highlight along the pending call's ladder, towards
// a wider rule for d > 0 and a narrower one for d < 0. It stops at both
// ends rather than wrapping: the ends are the floor and the ceiling of
// what may be saved, and wrapping from one to the other at a permission
// prompt is how a narrow rule becomes the widest by one keystroke.
func (m *model) moveRung(d int) {
	if m.pending == nil || len(m.pending.call.Rungs) < 2 {
		return
	}
	m.rung = min(max(m.rung+d, 0), len(m.pending.call.Rungs)-1)
}

// permissionKeys is the key line of a permission prompt, in the longest
// wording that fits on one line: it is the input box's placeholder, so a
// wrapped key line would run over the box. expandable says whether there
// is more of the call to see: without that, the line used to offer ctrl+o
// even for a two-line diff that was already shown whole. It is plain
// text; the placeholder style colours it.
func (m *model) permissionKeys(tool, rule, learned string, expandable bool) string {
	return m.permissionKeysAt(tool, rule, learned, expandable, 0, 1)
}

// permissionKeysAt is permissionKeys for one rung of a ladder of rungs:
// the hint that ← and → move along it rides with the rule, and is gone
// when there is only one rung to stand on.
func (m *model) permissionKeysAt(tool, rule, learned string, expandable bool, rung, rungs int) string {
	type seg struct{ key, text string }
	// Box width minus its border and padding, with two columns to spare
	// so a line measured at the limit never wraps inside the box.
	room := m.width - 8
	render := func(segs []seg, sep string) (string, int) {
		var sb strings.Builder
		for _, s := range segs {
			if s.key == "r" && rule == "" {
				continue // nothing for r to save: see perm.Suggest
			}
			if sb.Len() > 0 {
				sb.WriteString(sep)
			}
			sb.WriteString(s.key + s.text)
		}
		return sb.String(), lipgloss.Width(sb.String())
	}
	// The learned note rides along with the rule in the two widest
	// layouts and is the first thing dropped when narrowing: it explains
	// why r is worth pressing, but the rule itself is what makes the key
	// useful at all.
	note := ""
	if learned != "" {
		note = " · " + learnedHeadline(learned)
	}
	// The ladder hint says both that the rule can be moved and where on
	// the ladder it stands, so a rule that is not the default does not
	// look like the only one there was. It goes with the note when the
	// line narrows: a prompt with one rung has neither.
	ladder := ""
	if rungs > 1 {
		ladder = fmt.Sprintf(" ←→ %d/%d", rung+1, rungs)
	}
	long := []seg{{"y", " " + i18n.T("allow once")}, {"a", " " + i18n.Tf("always for %s this session", tool)}, {"r", " " + i18n.Tf("save rule %s", rule+ladder+note)}, {"n", " " + i18n.T("deny (and say why)")}, {"c", " " + i18n.T("cancel the turn")}}
	if k := m.keys.First(keymap.Expand); expandable && k != "" {
		long = append(long, seg{"", "(" + k + " expands)"})
	}
	if s, w := render(long, "   "); w <= room {
		return s
	}
	// The rule is the only part of the line the user cannot guess, so it
	// is the last thing to go. Shorten it the way the rest of the
	// interface shortens a path first, then spend the remaining width by
	// dropping the descriptions of the keys that need one least: "y once"
	// and "a all edit_file this session" say little "y" and "a session"
	// do not.
	// Cutting the rule first is what left an 80-column terminal showing
	// "r rule" with no rule at all, which is the one wording that makes
	// the key useless.
	rule = shortenPaths(m.opts.Cwd, rule)
	// "a always for bash" read as a permanent grant; a is this session
	// only, and r is the one that lasts, so the short forms say so.
	medium := []seg{{"y", " " + i18n.T("once")}, {"a", " " + i18n.Tf("all %s this session", tool)}, {"r", " " + i18n.Tf("save %s", rule+ladder+note)}, {"n", " " + i18n.T("deny")}, {"c", " " + i18n.T("cancel")}}
	if s, w := render(medium, " · "); w <= room {
		return s
	}
	// The hint outlives the learned note here: the note explains why r is
	// worth pressing, but without the hint a widened rule reads as the
	// only rule there ever was.
	session, save, deny, cancel := " "+i18n.T("session"), " "+i18n.T("save %s"), " "+i18n.T("deny"), " "+i18n.T("cancel")
	narrow := []seg{{"y", ""}, {"a", session}, {"r", fmt.Sprintf(save, rule+ladder)}, {"n", deny}, {"c", cancel}}
	if s, w := render(narrow, "  "); w <= room {
		return s
	}
	// Only now is the rule itself cut, to whatever the fixed part leaves.
	fixed := lipgloss.Width("y  a" + session + "  r" + fmt.Sprintf(save, "") + "  n" + deny + "  c" + cancel)
	if left := room - fixed; left >= 6 && lipgloss.Width(rule) > left {
		narrow[2].text = fmt.Sprintf(save, truncateLine(rule, left))
		if s, w := render(narrow, "  "); w <= room {
			return s
		}
	}
	s, w := render([]seg{{"y", ""}, {"a", session}, {"r", " " + i18n.T("save rule")}, {"n", deny}, {"c", cancel}}, " · ")
	if w > room {
		// Only a language whose words run longer than English's gets
		// here: the keys alone, which /help spells out.
		s, _ = render([]seg{{"y", ""}, {"a", ""}, {"r", ""}, {"n", ""}, {"c", ""}}, " · ")
	}
	return s
}

// suggestedRule is the rule r would save, written the way the rest of
// the interface writes a path: relative to the working directory, or
// with the home directory as ~. The rule saved to disk stays absolute;
// only the prompt is shortened, because an absolute path truncated in
// the middle told the user nothing about what they were about to allow.
func (m *model) suggestedRule(rule string) string {
	r, err := perm.Parse(rule)
	if err != nil {
		return rule
	}
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
	line := m.styles.warn.Bold(true).Render("allow ") + m.styles.tool.Bold(true).Render(sanitize(c.Name)) + " " + m.styles.dim.Render(sanitize(present.ToolSummary(c.Name, string(c.Input), m.width-len(c.Name)-16, m.opts.Cwd)))
	if c.Warning != "" {
		line += m.styles.err.Bold(true).Render(" · ⚠ " + sanitize(c.Warning))
	}
	return fitLine("  "+line, room)
}

// permissionWhy is the dim row "Asked because …; to stop: …": which layer
// asked and how to stop it asking. One line, like permissionImpact.
func (m *model) permissionWhy() string {
	if m.pending == nil || m.pending.call.Why == "" {
		return ""
	}
	return fitLine("  "+m.styles.dim.Render(sanitize(m.pending.call.Explain())), m.width-4)
}

// permissionImpact is the extra dim row shown under the notice while a
// mutating edit is pending: the reach of the change, computed by
// package impact and carried on the call as ToolCall.Impact. It is fit to
// one line and never wraps — footerHeight only reserves the one extra row
// this claims when it is non-empty (see layout.go), so a wrapped line
// would run into the input box below it.
func (m *model) permissionImpact() string {
	if m.pending == nil || m.pending.call.Impact == "" {
		return ""
	}
	room := m.width - 4
	return fitLine("  "+m.styles.dim.Render(sanitize(m.pending.call.Impact)), room)
}
