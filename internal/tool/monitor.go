package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

const (
	monitorPoll = 250 * time.Millisecond
	monitorMax  = time.Hour
)

// Monitor starts a background command and, when a line matches pattern,
// delivers it through OnMatch (the session Steers). It is the grok-style
// watch without a second scheduler: Jobs and Steer already exist.
type Monitor struct {
	Jobs    *Jobs
	Root    string
	WS      *Workspace
	OnMatch func(string)
}

func (Monitor) Name() string { return "monitor" }
func (Monitor) Description() string {
	return "Start a command in the background and notify when its output matches a pattern. Returns a job id immediately. Use jobs to kill it."
}
func (Monitor) Mutating() bool { return true }
func (Monitor) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{
"command":{"type":"string","description":"Shell command to run in the background"},
"pattern":{"type":"string","description":"Substring to watch for in the output"}},
"required":["command","pattern"],"additionalProperties":false}`)
}

func (t Monitor) Run(ctx context.Context, input json.RawMessage) (string, error) {
	var in struct {
		Command string `json:"command"`
		Pattern string `json:"pattern"`
	}
	if err := decode(input, &in); err != nil {
		return "", err
	}
	in.Command = strings.TrimSpace(in.Command)
	in.Pattern = strings.TrimSpace(in.Pattern)
	if in.Command == "" || in.Pattern == "" {
		return "", fmt.Errorf("command and pattern are required")
	}
	if t.Jobs == nil {
		return "", fmt.Errorf("background jobs are not available")
	}
	job, err := t.Jobs.StartOn(t.WS, t.Root, in.Command)
	if err != nil {
		return "", err
	}
	// The tool call's ctx ends when Run returns; the watch lives as long
	// as the job (KillAll on session close stops it).
	go t.watch(job.ID, in.Pattern)
	return fmt.Sprintf("monitor job %d watching for %q; kill with jobs kill %d", job.ID, in.Pattern, job.ID), nil
}

func (t Monitor) watch(id int, pattern string) {
	ticker := time.NewTicker(monitorPoll)
	defer ticker.Stop()
	deadline := time.NewTimer(monitorMax)
	defer deadline.Stop()
	var seen string
	for {
		select {
		case <-deadline.C:
			return
		case <-ticker.C:
			job, ok := t.Jobs.Get(id)
			if !ok {
				return
			}
			out, err := t.Jobs.Output(id, 80)
			if err != nil {
				if job.Done {
					return
				}
				continue
			}
			matched := strings.Contains(out, pattern)
			if matched && !(strings.HasPrefix(out, seen) && strings.Contains(seen, pattern)) {
				seen = out
				if line := lastMatchingLine(out, pattern); t.OnMatch != nil && line != "" {
					t.OnMatch(fmt.Sprintf("[monitor %d] %s", id, line))
				}
				return
			}
			if job.Done {
				return
			}
		}
	}
}

func lastMatchingLine(out, pattern string) string {
	var hit string
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, pattern) {
			hit = strings.TrimSpace(line)
		}
	}
	return hit
}
