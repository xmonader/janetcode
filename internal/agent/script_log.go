package agent

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/xmonader/janetcode/internal/session"
	"github.com/xmonader/janetcode/internal/textfmt"
	"github.com/xmonader/janetcode/internal/tool"
)

// PTCRun is one execute_code or run_code invocation folded from the
// session log, including the nested tool calls it made.
type PTCRun struct {
	N      int
	Tool   string
	Code   string
	Result string
	Failed bool
	Calls  []PTCCall
}

// PTCCall is one nested tool call made from a PTC run.
type PTCCall struct {
	Name   string
	Input  string
	Output string
	Failed bool
}

// FoldPTC reads execute_code and run_code runs from the session log.
func FoldPTC(events []session.Event) []PTCRun {
	var runs []PTCRun
	cur := -1
	for _, e := range events {
		switch {
		case e.Type == session.TypeToolCall && (e.Name == "execute_code" || e.Name == "run_code"):
			runs = append(runs, PTCRun{N: len(runs) + 1, Tool: e.Name, Code: ptcCode(e)})
			cur = len(runs) - 1
		case e.Type == session.TypeToolResult && (e.Name == "execute_code" || e.Name == "run_code"):
			if cur >= 0 && runs[cur].Tool == e.Name {
				runs[cur].Result, runs[cur].Failed = e.Output, e.IsError
				cur = -1
			}
		case e.Type == session.TypeToolCall && (strings.HasPrefix(e.CallID, "script-") || strings.HasPrefix(e.CallID, "runcode-")):
			if cur < 0 {
				name := "run_code"
				if strings.HasPrefix(e.CallID, "script-") {
					name = "execute_code"
				}
				runs = append(runs, PTCRun{N: len(runs) + 1, Tool: name})
				cur = len(runs) - 1
			}
			runs[cur].Calls = append(runs[cur].Calls, PTCCall{Name: e.Name, Input: e.Arguments})
		case e.Type == session.TypeToolResult && (strings.HasPrefix(e.CallID, "script-") || strings.HasPrefix(e.CallID, "runcode-")):
			if cur >= 0 && len(runs[cur].Calls) > 0 {
				c := &runs[cur].Calls[len(runs[cur].Calls)-1]
				if c.Output == "" && c.Name == e.Name {
					c.Output, c.Failed = e.Output, e.IsError
				}
			}
		}
	}
	return runs
}

func ptcCode(e session.Event) string {
	var in struct {
		Code        json.RawMessage `json:"code"`
		Description string          `json:"description"`
	}
	if json.Unmarshal([]byte(e.Arguments), &in) != nil {
		return e.Arguments
	}
	if len(in.Code) == 0 {
		return e.Arguments
	}
	if in.Code[0] == '"' {
		var s string
		if json.Unmarshal(in.Code, &s) == nil {
			return s
		}
	}
	return string(in.Code)
}

// RenderScripts reports every script this session ran: the code, what it
// printed, and the tool calls it made on the way. A script is the one
// place where tools run without their output passing in front of the
// model, so the log is the only record of what happened; this is how it is
// read back.
func RenderScripts(events []session.Event) string {
	type run struct {
		code, result string
		failed       bool
		calls        []string
	}
	var runs []*run
	var cur *run
	for _, e := range events {
		switch {
		case e.Type == session.TypeToolCall && e.Name == "execute_code":
			var in struct {
				Code string `json:"code"`
			}
			_ = json.Unmarshal([]byte(e.Arguments), &in)
			cur = &run{code: in.Code}
			runs = append(runs, cur)
		case e.Type == session.TypeToolResult && e.Name == "execute_code":
			if cur != nil {
				cur.result, cur.failed = e.Output, e.IsError
				cur = nil
			}
		case e.Type == session.TypeToolCall && strings.HasPrefix(e.CallID, "script-"):
			if cur != nil {
				cur.calls = append(cur.calls, e.Name+" "+textfmt.Clip(strings.Join(strings.Fields(e.Arguments), " "), 100))
			}
		}
	}
	if len(runs) == 0 {
		return "no script has been run this session"
	}
	var b strings.Builder
	for i, r := range runs {
		if i > 0 {
			b.WriteString("\n")
		}
		state := "finished"
		switch {
		case r.failed:
			state = "failed"
		case r.result == "":
			state = "still running"
		}
		fmt.Fprintf(&b, "script %d (%s, %s):\n", i+1, state, textfmt.Plural(len(r.calls), "tool call"))
		for _, line := range strings.Split(strings.TrimRight(r.code, "\n"), "\n") {
			b.WriteString("  | " + line + "\n")
		}
		for _, c := range r.calls {
			b.WriteString("  → " + c + "\n")
		}
		if r.result != "" {
			b.WriteString("  printed:\n")
			for _, line := range strings.Split(strings.TrimRight(tool.TruncateTo(r.result, 2000), "\n"), "\n") {
				b.WriteString("    " + line + "\n")
			}
		}
	}
	return strings.TrimRight(b.String(), "\n")
}
