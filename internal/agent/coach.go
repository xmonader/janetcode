package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/xmonader/janetcode/internal/tool"
)

// Deterministic feedback appended to edit results, at no model cost:
//   - a failed edit on a file the model never read comes back with the
//     file's content, so the retry can land;
//   - the second failure in a row on the same file tells the model to stop
//     adjusting the anchor and rewrite a larger unique region.

const (
	coachReadLines = 300
	coachReadBytes = 16 * 1024
)

// coach tracks per-file edit failures and which files were read.
type coach struct {
	fails map[string]int
	read  map[string]bool
}

// absPath asks a Toucher for the file it changed.
func absPath(t tool.Tool, input json.RawMessage) string {
	if tc, ok := t.(tool.Toucher); ok {
		if paths, err := tc.Touches(input); err == nil && len(paths) == 1 {
			return paths[0]
		}
	}
	return ""
}

func inputPath(input json.RawMessage) string {
	var in struct {
		Path string `json:"path"`
	}
	if json.Unmarshal(input, &in) != nil || in.Path == "" {
		return ""
	}
	return filepath.Clean(in.Path)
}

// after inspects a finished tool call and returns text to append to its
// result. An empty abs path (no Toucher) skips the per-edit feedback.
func (c *coach) after(ctx context.Context, tools *tool.Registry, t tool.Tool, name string, input json.RawMessage, isErr bool) string {
	if c.fails == nil {
		c.fails, c.read = map[string]int{}, map[string]bool{}
	}
	p := inputPath(input)
	if p == "" {
		return ""
	}
	switch name {
	case "read_file":
		if !isErr {
			c.read[p] = true
		}
		return ""
	case "write_file":
		if !isErr {
			c.read[p] = true
			return editFeedback(ctx, absPath(t, input))
		}
		return ""
	case "edit_file":
	default:
		return ""
	}
	if !isErr {
		delete(c.fails, p)
		c.read[p] = true
		return editFeedback(ctx, absPath(t, input))
	}
	c.fails[p]++
	var notes []string
	if !c.read[p] {
		if rf, ok := tools.Get("read_file"); ok {
			req, _ := json.Marshal(map[string]any{"path": p, "limit": coachReadLines})
			if body, err := rf.Run(ctx, req); err == nil {
				c.read[p] = true
				notes = append(notes, fmt.Sprintf("You edited %s without reading it first. Here is its current content (up to %d lines) so the next edit can match exactly:\n%s", p, coachReadLines, tool.TruncateTo(body, coachReadBytes)))
			}
		}
	}
	if c.fails[p] >= 2 {
		notes = append(notes, fmt.Sprintf("This is failure %d in a row editing %s. Stop adjusting the anchor: re-read the file and replace one larger unique region (the whole enclosing function or block) in a single edit_file call, or rewrite the file with write_file.", c.fails[p], p))
	}
	if len(notes) == 0 {
		return ""
	}
	return "\n\n" + strings.Join(notes, "\n\n")
}
