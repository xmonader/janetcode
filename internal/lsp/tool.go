package lsp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// Tool exposes diagnostics to the model on demand.
type Tool struct{ M *Manager }

func (Tool) Name() string { return "diagnostics" }
func (Tool) Description() string {
	return "Language-server diagnostics (compile errors, type errors, lint) for one file or for every file reported so far. Edits already return diagnostics for the edited file; use this to re-check a file or to see problems in other files of the package."
}
func (Tool) Mutating() bool { return false }
func (Tool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"path":{"type":"string","description":"File to check; omit for every file with known problems"}}}`)
}

func (t Tool) Run(ctx context.Context, input json.RawMessage) (string, error) {
	var in struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(input, &in); err != nil {
		return "", fmt.Errorf("bad input: %w", err)
	}
	if !t.M.Enabled() {
		return "diagnostics are disabled or no language server is configured", nil
	}
	if in.Path == "" {
		known := t.M.Known()
		if len(known) == 0 {
			return "no known problems (" + strings.Join(t.M.Status(), "; ") + ")", nil
		}
		return strings.Join(known, "\n"), nil
	}
	if t.M.ServerFor(in.Path) == "" {
		return "no language server handles " + in.Path + " (" + strings.Join(t.M.Status(), "; ") + ")", nil
	}
	lines, ok, err := t.M.Diagnose(ctx, in.Path)
	if err != nil {
		return "", err
	}
	switch {
	case !ok && len(lines) == 0:
		return "the language server did not answer in time; try again", nil
	case len(lines) == 0:
		return "no problems in " + in.Path, nil
	}
	return strings.Join(lines, "\n"), nil
}
