package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/xmonader/janetcode/internal/agent"
	"github.com/xmonader/janetcode/internal/textfmt"
	"github.com/xmonader/janetcode/internal/tool"
)

// shellDoneMsg carries the result of a "!command" the user ran directly.
type shellDoneMsg struct {
	call   agent.ToolCall
	output string
	err    error
	dur    time.Duration
}

// shell runs a command the user typed with a "!" prefix in the project
// directory, shows it like a tool call, and hands the output to the model
// as context for the next turn.
func (m *model) shell(cmdline string) tea.Cmd {
	cmdline = m.inflatePastes(cmdline)
	if cmdline == "" {
		m.transcript.errorf("usage: !<command> · runs in %s and shares the output with the model", shortPath(m.opts.Cwd))
		m.refresh(false)
		return nil
	}
	if m.turn.running {
		m.transcript.errorf("wait for the current turn to finish before running a command")
		m.refresh(false)
		return nil
	}
	// A command the user ran themselves names hosts as much as a prompt
	// does: curl localhost:3000 is the user pointing at it.
	m.opts.Typed.Note(cmdline)
	// It also says what the project's check command is: somebody running
	// the tests by hand twice is somebody who has not met /verify.
	m.observeBang(cmdline)
	m.noteUse("!command")
	input, _ := json.Marshal(map[string]string{"command": cmdline})
	call := agent.ToolCall{ID: fmt.Sprintf("shell-%d", time.Now().UnixNano()), Name: "shell", Input: input}
	m.transcript.apply(agent.Event{Kind: agent.EvToolRun, Call: &call})
	m.startTurn("running your command")
	ctx, cancel := context.WithCancel(m.life)
	m.turn.cancel = cancel
	send, cwd := m.send, m.opts.Cwd
	m.repin()
	m.refresh(false)
	go func() {
		start := time.Now()
		out, err := tool.Bash{Root: cwd}.Run(ctx, input)
		send(shellDoneMsg{call: call, output: out, err: err, dur: time.Since(start)})
	}()
	return m.spinner.Tick
}

func (m *model) finishShell(msg shellDoneMsg) {
	m.turn.running = false
	m.turn.cancelling = false
	if m.turn.cancel != nil {
		m.turn.cancel()
		m.turn.cancel = nil
	}
	m.turn.setPhase("")
	out, isErr := msg.output, false
	if msg.err != nil {
		out, isErr = "Error: "+msg.err.Error(), true
	}
	if out == "" {
		out = "(no output)"
	}
	// The shell tool reports a non-zero exit in the output, not as an
	// error, so "!nosuchcommand" used to be shown with the same green
	// tick as a command that worked.
	failed := ""
	if !isErr {
		if code, ok := exitCode(out); ok {
			isErr, failed = true, fmt.Sprintf("exit code %d", code)
		}
	}
	m.transcript.apply(agent.Event{Kind: agent.EvToolResult, Call: &msg.call, Output: out, IsError: isErr, Duration: msg.dur})
	var in struct{ Command string }
	json.Unmarshal(msg.call.Input, &in)
	m.opts.Agent.AddContext(fmt.Sprintf("I ran this command myself in %s:\n$ %s\n<output>\n%s\n</output>", m.opts.Cwd, in.Command, strings.TrimRight(out, "\n")))
	m.status = "ran in " + textfmt.Duration(msg.dur)
	if failed != "" {
		m.status += " · " + failed
	}
	m.status += " · output shared with the model"
	if len(m.turn.queued) > 0 {
		m.status += " · sending queued prompt"
	}
}

// exitCodeRe matches the note tool.Bash appends for a non-zero exit.
// The form that ends "not a failure" is grep or diff saying it found
// nothing, which is an answer and not a red mark.
var exitCodeRe = regexp.MustCompile(`\[exit code (\d+)\]`)

// exitCode reports the non-zero exit a command's output carries.
func exitCode(out string) (int, bool) {
	m := exitCodeRe.FindStringSubmatch(out)
	if m == nil || strings.Contains(out, "not a failure") {
		return 0, false
	}
	n, err := strconv.Atoi(m[1])
	if err != nil || n == 0 {
		return 0, false
	}
	return n, true
}
