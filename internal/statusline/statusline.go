// Package statusline runs the user's status line command: a shell command
// that gets the session as JSON on stdin and prints a line or two for the
// terminal interface to show under the input. It is the status line
// Claude Code and Qwen Code have, and reads the same JSON they send.
package statusline

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/xmonader/janetcode/internal/shell"
)

// Config is the "status_line" object in the config file. It comes only
// from the user's own config: a command a cloned repository could set
// would run on every turn.
type Config struct {
	// Type is "command" or empty: the Claude Code and Qwen Code objects
	// carry it, and command is the only kind there is here.
	Type    string `json:"type,omitempty"`
	Command string `json:"command"`
	// RefreshInterval re-runs the command every so many seconds as well as
	// when the session changes, for a clock or a quota.
	RefreshInterval int `json:"refresh_interval,omitempty"`
	// Padding is accepted from a Claude Code object and ignored.
	Padding int `json:"padding,omitempty"`
}

// UnmarshalJSON also reads refreshInterval, the spelling Qwen Code uses.
func (c *Config) UnmarshalJSON(data []byte) error {
	type plain Config
	var aux struct {
		plain
		Refresh *int `json:"refreshInterval"`
	}
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}
	*c = Config(aux.plain)
	if aux.Refresh != nil && c.RefreshInterval == 0 {
		c.RefreshInterval = *aux.Refresh
	}
	return nil
}

// Validate refuses a status line that cannot work, when the config loads
// rather than as a blank footer later.
func (c Config) Validate() error {
	if c.Type != "" && c.Type != "command" {
		return fmt.Errorf("status_line: type %q is not supported; only command status lines are", c.Type)
	}
	if strings.TrimSpace(c.Command) == "" {
		return errors.New("status_line: command is empty")
	}
	if c.RefreshInterval < 0 {
		return fmt.Errorf("status_line: refresh_interval %d is negative", c.RefreshInterval)
	}
	return nil
}

// Refresh is how often the command re-runs on its own, or zero for never;
// under a second is a second.
func (c Config) Refresh() time.Duration {
	if c.RefreshInterval <= 0 {
		return 0
	}
	return time.Duration(c.RefreshInterval) * time.Second
}

// Input is what the command reads on stdin: the fields Claude Code and
// Qwen Code both send, where janetcode knows them. A field it cannot fill
// truthfully is left out rather than sent as zero.
type Input struct {
	HookEventName  string     `json:"hook_event_name"`
	SessionID      string     `json:"session_id,omitempty"`
	TranscriptPath string     `json:"transcript_path,omitempty"`
	Cwd            string     `json:"cwd"`
	Version        string     `json:"version,omitempty"`
	PermissionMode string     `json:"permission_mode,omitempty"`
	Model          Model      `json:"model"`
	Workspace      Workspace  `json:"workspace"`
	Git            *Git       `json:"git,omitempty"`
	ContextWindow  ContextWin `json:"context_window"`
	Cost           *Cost      `json:"cost,omitempty"`
}

type Model struct {
	ID          string `json:"id"`
	DisplayName string `json:"display_name"`
}

type Workspace struct {
	CurrentDir string `json:"current_dir"`
	ProjectDir string `json:"project_dir"`
}

type Git struct {
	Branch string `json:"branch"`
}

// ContextWin is how full the context is. Size is the model's window
// when it is known; the percentages are of it.
type ContextWin struct {
	Size              int      `json:"context_window_size,omitempty"`
	UsedPercentage    *float64 `json:"used_percentage,omitempty"`
	RemainingPercent  *float64 `json:"remaining_percentage,omitempty"`
	CurrentUsage      int      `json:"current_usage"`
	TotalInputTokens  int      `json:"total_input_tokens"`
	TotalOutputTokens int      `json:"total_output_tokens"`
}

// SetUsage fills the percentages from the tokens in use and the window.
func (c *ContextWin) SetUsage(used, window int) {
	c.CurrentUsage, c.Size = used, window
	if window <= 0 {
		return
	}
	u := float64(used) * 100 / float64(window)
	u = float64(int(min(u, 100)*10+0.5)) / 10
	r := float64(int((100-u)*10+0.5)) / 10
	c.UsedPercentage, c.RemainingPercent = &u, &r
}

type Cost struct {
	TotalCostUSD float64 `json:"total_cost_usd"`
}

// Timeout is how long the command may take; a slower one is killed and
// the status line is left blank.
const Timeout = 5 * time.Second

// MaxLines is how many lines of output are shown.
const MaxLines = 2

// Run runs the command in dir with in on stdin, and returns the lines to
// show: at most MaxLines, blank ones dropped, and with every escape
// sequence other than a colour taken out, so a command cannot move the
// cursor or retitle the terminal.
func Run(ctx context.Context, c Config, dir string, in Input) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, Timeout)
	defer cancel()
	in.HookEventName = "Status"
	payload, err := json.Marshal(in)
	if err != nil {
		return nil, err
	}
	cmd := shell.Command(ctx, c.Command)
	// A command that leaves a child holding the pipe open must not hold
	// the status line past its timeout.
	shell.Supervise(cmd)
	cmd.Dir = dir
	cmd.Stdin = bytes.NewReader(payload)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err = cmd.Run()
	switch {
	case ctx.Err() == context.DeadlineExceeded:
		return nil, fmt.Errorf("status line command timed out after %s", Timeout)
	case err != nil:
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return nil, fmt.Errorf("status line command failed: %s", firstLine(msg))
	}
	return Lines(stdout.String()), nil
}

// Lines turns the command's output into the lines to show.
func Lines(out string) []string {
	var lines []string
	for _, l := range strings.Split(out, "\n") {
		l = clean(l)
		if strings.TrimSpace(stripSGR.ReplaceAllString(l, "")) == "" {
			continue
		}
		lines = append(lines, l)
		if len(lines) == MaxLines {
			break
		}
	}
	return lines
}

var (
	// Any escape sequence: CSI, OSC (ended by BEL or ST), or a two-byte one.
	escapeSeq = regexp.MustCompile(`\x1b(?:\[[0-?]*[ -/]*[@-~]|\][^\x07\x1b]*(?:\x07|\x1b\\)|[@-Z\\-_])`)
	stripSGR  = regexp.MustCompile(`\x1b\[[0-9;:]*m`)
)

// clean keeps a line's colours and drops every other escape and control
// character; a tab is a space.
func clean(l string) string {
	l = escapeSeq.ReplaceAllStringFunc(l, func(seq string) string {
		if stripSGR.MatchString(seq) && stripSGR.FindString(seq) == seq {
			return seq
		}
		return ""
	})
	return strings.Map(func(r rune) rune {
		switch {
		case r == '\t':
			return ' '
		case r == '\x1b':
			return r // only colour sequences are left
		case r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0):
			return -1
		}
		return r
	}, l)
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
