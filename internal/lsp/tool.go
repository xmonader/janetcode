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

// SymbolTool answers questions about the code from the language server's
// resolved view of the program: where a name is defined, who uses it, what
// its type is, what a file declares. grep can approximate these, but it
// matches text — it cannot tell a declaration from a mention, or this
// package's Config from another's — and it costs a read of every hit.
type SymbolTool struct{ M *Manager }

func (SymbolTool) Name() string { return "symbols" }
func (SymbolTool) Description() string {
	return "Ask the language server about code: definition (where a name is declared), references (every use), type (hover: type and doc), implementations, outline (what a file declares), or search (find a symbol by name across the project). Prefer this over grep for anything about a symbol: it resolves the program, so it distinguishes a declaration from a mention and one package's name from another's. Name the file with path, and the symbol with symbol (or line, when the same name appears more than once)."
}
func (SymbolTool) Mutating() bool { return false }
func (SymbolTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{
"action":{"type":"string","enum":["definition","references","type","implementations","outline","search"],"description":"What to ask"},
"path":{"type":"string","description":"File the symbol is in; for search, any file of the language to ask"},
"symbol":{"type":"string","description":"Name to look at, or the query for search"},
"line":{"type":"integer","description":"1-based line, when the name appears more than once"},
"column":{"type":"integer","description":"1-based column; optional"}},
"required":["action"],"additionalProperties":false}`)
}

func (t SymbolTool) Run(ctx context.Context, input json.RawMessage) (string, error) {
	var in struct {
		Action string `json:"action"`
		Path   string `json:"path"`
		Symbol string `json:"symbol"`
		Line   int    `json:"line"`
		Column int    `json:"column"`
	}
	if err := json.Unmarshal(input, &in); err != nil {
		return "", fmt.Errorf("bad input: %w", err)
	}
	if !t.M.Enabled() {
		return "no language server is configured for this project", nil
	}
	if in.Action != "search" {
		if in.Path == "" {
			return "", fmt.Errorf("path is required for %s", in.Action)
		}
		if t.M.ServerFor(in.Path) == "" {
			return "no language server handles " + in.Path + " (" + strings.Join(t.M.Status(), "; ") + ")", nil
		}
	}
	pos := Point{Line: in.Line, Col: in.Column, Symbol: in.Symbol}
	switch in.Action {
	case "definition":
		return t.locations(ctx, "definition of "+t.what(in.Symbol, in.Line), t.M.Definition, in.Path, pos)
	case "implementations":
		return t.locations(ctx, "implementations of "+t.what(in.Symbol, in.Line), t.M.Implementation, in.Path, pos)
	case "references":
		return t.locations(ctx, "references to "+t.what(in.Symbol, in.Line), t.M.References, in.Path, pos)
	case "type":
		text, err := t.M.Hover(ctx, in.Path, pos)
		if err != nil {
			return "", err
		}
		if strings.TrimSpace(text) == "" {
			return "the language server has nothing for " + t.what(in.Symbol, in.Line) + " in " + in.Path, nil
		}
		return text, nil
	case "outline":
		syms, err := t.M.Outline(ctx, in.Path)
		if err != nil {
			return "", err
		}
		return renderSymbols(in.Path+" declares", syms, true), nil
	case "search":
		if in.Symbol == "" {
			return "", fmt.Errorf("symbol is the query for search")
		}
		syms, err := t.M.WorkspaceSymbols(ctx, in.Symbol, in.Path)
		if err != nil {
			return "", err
		}
		return renderSymbols("symbols matching "+in.Symbol, syms, false), nil
	default:
		return "", fmt.Errorf("unknown action %q", in.Action)
	}
}

func (t SymbolTool) what(symbol string, line int) string {
	switch {
	case symbol != "":
		return symbol
	case line > 0:
		return fmt.Sprintf("line %d", line)
	}
	return "the symbol"
}

func (t SymbolTool) locations(ctx context.Context, label string, q func(context.Context, string, Point) ([]Location, error), path string, pos Point) (string, error) {
	locs, err := q(ctx, path, pos)
	if err != nil {
		return "", err
	}
	if len(locs) == 0 {
		return "no " + label + " found", nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d %s:\n", len(locs), label)
	for _, l := range locs {
		b.WriteString(l.String() + "\n")
	}
	return strings.TrimRight(b.String(), "\n"), nil
}

// renderSymbols lists symbols; nested ones are indented under their
// container when the server gave the file's own tree.
func renderSymbols(label string, syms []Symbol, local bool) string {
	if len(syms) == 0 {
		return "no " + label
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s (%d):\n", label, len(syms))
	for _, s := range syms {
		indent := ""
		if local && s.Container != "" {
			indent = "  "
		}
		line := fmt.Sprintf("%s%s %s", indent, s.Kind, s.Name)
		if !local {
			line += " — " + s.Loc.Path
		}
		fmt.Fprintf(&b, "%s:%d\n", line, s.Loc.Line)
	}
	return strings.TrimRight(b.String(), "\n")
}
