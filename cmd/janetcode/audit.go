package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/xmonader/janetcode/internal/perm"
	"github.com/xmonader/janetcode/internal/session"
	"github.com/xmonader/janetcode/internal/textfmt"
	"github.com/xmonader/janetcode/internal/whatif"
)

// errAuditBroken is --audit's exit when the chain or the session do not
// agree, so a script can gate on it.
var errAuditBroken = errors.New("audit: the session and its audit log do not agree")

// auditMode is --audit: check a session's audit log against the session,
// then print what the session did, turn by turn, from the record alone —
// the prompts, each model call with its model and cost, each tool call
// with what authorized it and how it ended, each file it changed, and
// what the turn demonstrated. Nothing is run: no model, no tool, no
// network.
func auditMode(w io.Writer, arg, denyList string) error {
	deny, err := perm.ParseAll(perm.SplitList(denyList))
	if err != nil {
		return fmt.Errorf("invalid --deny rule: %w", err)
	}
	path, dir, id, err := whatif.ResolveSession(arg)
	if err != nil {
		return err
	}
	rep, err := session.VerifyAudit(path)
	if err != nil {
		return err
	}
	s, _, err := session.Open(dir, id)
	if err != nil {
		return err
	}
	fmt.Fprintf(w, "session %s  %s\n", s.Meta.ID, s.Label())
	fmt.Fprintf(w, "directory %s · %s/%s\n\n", s.Meta.Cwd, s.Meta.Provider, s.Meta.Model)
	if len(deny) == 0 {
		printAuditTimeline(w, s.Events())
	} else if rep.OK() {
		printAuditDenyPreview(w, s.Events(), rep.From, s.Meta.Cwd, perm.Rules{Deny: deny})
	}
	fmt.Fprintln(w)
	fmt.Fprint(w, auditSummary(rep))
	if !rep.OK() {
		return errAuditBroken
	}
	return nil
}

// The tool call event records the effective arguments after authorization,
// including any hook rewrite. Calls refused at the time have no such event.
func printAuditDenyPreview(w io.Writer, events []session.Event, from int, root string, rules perm.Rules) {
	fmt.Fprintln(w, "Deny rule preview using recorded arguments and the session’s starting directory")
	var checked, matched int
	for _, e := range events {
		if e.Type != session.TypeToolCall || e.Seq < from {
			continue
		}
		checked++
		input := json.RawMessage(e.Arguments)
		call := perm.Call{Tool: e.Name, Input: input, Root: root, Subject: perm.SubjectIn(e.Name, input, root)}
		verdict, rule, hard := rules.DecideCall(call)
		if verdict == perm.Denied && hard == "" {
			matched++
			fmt.Fprintf(w, "  turn %d · event %d · %s · %s\n", e.Turn, e.Seq, oneLineOf(e.Name), rule)
		}
	}
	fmt.Fprintf(w, "%d of %d audited tool calls matched the proposed deny rules\n", matched, checked)
}

// auditSummary is the verdict on the chain, last so it is what a reader
// sees first at the bottom of a terminal.
func auditSummary(rep session.AuditReport) string {
	var sb strings.Builder
	if rep.OK() {
		fmt.Fprintf(&sb, "audit: intact — %s vouched for by %s", textfmt.Plural(rep.Events, "event"), textfmt.Plural(rep.Records, "chained record"))
	} else {
		fmt.Fprintf(&sb, "audit: BROKEN — %s", textfmt.Plural(len(rep.Problems), "problem"))
	}
	if rep.Before > 0 {
		fmt.Fprintf(&sb, " · %s recorded before the audit log began", textfmt.Plural(rep.Before, "earlier event"))
	}
	sb.WriteString("\n")
	for _, p := range rep.Problems {
		sb.WriteString("  " + p + "\n")
	}
	if rep.Head != "" {
		fmt.Fprintf(&sb, "head %s (keep it to show later that nothing was cut off the end)\n", rep.Head)
	}
	return sb.String()
}

// printAuditTimeline writes the session's events as a readable record.
func printAuditTimeline(w io.Writer, events []session.Event) {
	turn := -1
	for _, e := range events {
		if e.Turn != turn && e.Turn > 0 {
			turn = e.Turn
			fmt.Fprintf(w, "turn %d\n", turn)
		}
		switch e.Type {
		case session.TypeUserMessage:
			if e.Message != nil {
				fmt.Fprintf(w, "  user       %s\n", textfmt.Clip(oneLineOf(e.Message.Text()), 100))
			}
		case session.TypeAssistant:
			model := e.Model
			if model == "" {
				model = "model"
			}
			var parts []string
			if e.Provider != "" {
				parts = append(parts, e.Provider)
			}
			if e.Route != "" {
				parts = append(parts, e.Route+": "+e.RouteWhy)
			}
			if e.Usage != nil {
				parts = append(parts, fmt.Sprintf("in %d out %d", e.Usage.InputTokens, e.Usage.OutputTokens))
				if e.Usage.CostUSD > 0 {
					parts = append(parts, fmt.Sprintf("$%.4f", e.Usage.CostUSD))
				}
			}
			line := ""
			if e.Message != nil {
				line = textfmt.Clip(oneLineOf(e.Message.Text()), 80)
			}
			model = model[strings.LastIndex(model, "/")+1:]
			if line != "" {
				line = ": " + line
			}
			fmt.Fprintf(w, "  model      %s [%s]%s\n", model, strings.Join(parts, ", "), line)
		case session.TypePermission:
			verdict := "allowed"
			if e.Denied {
				verdict = "REFUSED"
			}
			fmt.Fprintf(w, "  permission %s %s by %s\n", e.Name, verdict, e.Reason)
		case session.TypeToolCall:
			fmt.Fprintf(w, "  call       %s %s\n", e.Name, textfmt.Clip(oneLineOf(e.Arguments), 90))
		case session.TypeToolResult:
			if e.Name == "" {
				continue // the result as the model saw it; the named one is the record
			}
			how := "ok"
			switch {
			case e.Denied:
				how = "not run"
			case e.IsError:
				how = "failed"
			}
			fmt.Fprintf(w, "  result     %s %s: %s\n", e.Name, how, textfmt.Clip(oneLineOf(e.Output), 80))
		case session.TypeFileChange:
			fmt.Fprintf(w, "  changed    %s %s\n", e.Name, shortDigests(e.Arguments))
		case session.TypeGate:
			how := "passed"
			if e.IsError {
				how = "failed"
			}
			fmt.Fprintf(w, "  check      %s %s\n", e.Name, how)
		case session.TypeVerdict:
			fmt.Fprintf(w, "  verdict    %s\n", textfmt.Clip(oneLineOf(e.Name+": "+e.Output), 100))
		}
	}
}

// shortDigests shortens "<sha256>-><sha256>" to twelve characters a side.
func shortDigests(s string) string {
	before, after, ok := strings.Cut(s, "->")
	if !ok {
		return s
	}
	short := func(d string) string {
		if len(d) > 12 {
			return d[:12]
		}
		return d
	}
	return short(before) + " → " + short(after)
}

func oneLineOf(s string) string {
	return strings.Join(strings.Fields(s), " ")
}
