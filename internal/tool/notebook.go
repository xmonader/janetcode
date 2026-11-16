package tool

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"regexp"
	"strconv"
	"strings"

	"github.com/xmonader/janetcode/internal/diff"
	"github.com/xmonader/janetcode/internal/textfmt"
)

// notebook is a parsed .ipynb file, kept in the form it was written so an
// edit changes only the cells it names.
type notebook struct {
	root  *jval
	style jstyle
}

func isNotebookPath(p string) bool { return strings.EqualFold(pathExt(p), ".ipynb") }

func pathExt(p string) string {
	i := strings.LastIndexByte(p, '.')
	if i < 0 || strings.ContainsAny(p[i:], `/\`) {
		return ""
	}
	return p[i:]
}

func parseNotebook(data []byte) (*notebook, error) {
	root, err := parseJSON(data)
	if err != nil {
		return nil, fmt.Errorf("not a notebook: %w", err)
	}
	if root.kind != '{' || root.get("cells") == nil || root.get("cells").kind != '[' {
		return nil, errors.New("not a notebook: no cells list")
	}
	major, _ := strconv.Atoi(string(scalarOf(root.get("nbformat"))))
	if major != 0 && major < 4 {
		return nil, fmt.Errorf("nbformat %d notebooks are not supported; convert it to nbformat 4 with `jupyter nbconvert --to notebook`", major)
	}
	return &notebook{root: root, style: detectStyle(data)}, nil
}

func scalarOf(v *jval) []byte {
	if v == nil || v.kind != 's' {
		return nil
	}
	return v.raw
}

func (nb *notebook) cells() []*jval { return nb.root.get("cells").vals }

// minor is the nbformat minor version; cell ids exist from 4.5.
func (nb *notebook) minor() int {
	n, _ := strconv.Atoi(string(scalarOf(nb.root.get("nbformat_minor"))))
	return n
}

func (nb *notebook) bytes() []byte { return nb.style.marshal(nb.root) }

func cellType(c *jval) string { s, _ := c.get("cell_type").str(); return s }
func cellID(c *jval) string   { s, _ := c.get("id").str(); return s }

// language names the kernel's language, for the header of the rendering.
func (nb *notebook) language() string {
	md := nb.root.get("metadata")
	if s, ok := md.get("language_info").get("name").str(); ok {
		return s
	}
	if s, ok := md.get("kernelspec").get("language").str(); ok {
		return s
	}
	return ""
}

var ansiRe = regexp.MustCompile(`\x1b(\[[0-?]*[ -/]*[@-~]|\][^\x07\x1b]*(\x07|\x1b\\)|[@-Z\\-_])`)

// stripANSI removes terminal escape sequences: colour in a traceback, the
// cursor movement of a program running under a pty.
func stripANSI(s string) string {
	if !strings.Contains(s, "\x1b") {
		return s
	}
	return ansiRe.ReplaceAllString(s, "")
}

// Output limits for the rendering. A notebook's outputs can be far larger
// than its code — a printed dataframe, a training log — and what the
// model needs to edit a cell is the code and a glimpse of what it did.
const (
	nbOutputLines = 30
	nbOutputBytes = 3000
)

// render is the notebook as the model reads it: one block per cell with
// its index, type and id, the source, and each output trimmed.
func (nb *notebook) render(display string) string {
	cells := nb.cells()
	var sb strings.Builder
	head := fmt.Sprintf("notebook %s · %s", display, textfmt.Plural(len(cells), "cell"))
	if lang := nb.language(); lang != "" {
		head += " · " + lang
	}
	sb.WriteString(head + "\n")
	sb.WriteString("(rendered as cells, not raw JSON; change it with notebook_edit — load it with tool_search if you do not have it — which takes the cell index or id shown in brackets)\n")
	for i, c := range cells {
		sb.WriteString("\n" + cellHeader(i, c) + "\n")
		src := c.get("source").text()
		if src == "" {
			sb.WriteString("(empty)\n")
		} else {
			sb.WriteString(strings.TrimRight(src, "\n") + "\n")
		}
		if outs := c.get("outputs"); outs != nil {
			for _, o := range outs.vals {
				sb.WriteString(renderOutput(o))
			}
		}
	}
	return sb.String()
}

func cellHeader(i int, c *jval) string {
	h := fmt.Sprintf("[%d] %s", i, cellType(c))
	if id := cellID(c); id != "" {
		h += " · id " + id
	}
	if n := scalarOf(c.get("execution_count")); len(n) > 0 && string(n) != "null" {
		h += " · run " + string(n)
	}
	return h
}

func renderOutput(o *jval) string {
	kind, _ := o.get("output_type").str()
	var label, body string
	switch kind {
	case "stream":
		name, _ := o.get("name").str()
		label, body = name, o.get("text").text()
	case "error":
		en, _ := o.get("ename").str()
		ev, _ := o.get("evalue").str()
		label = "error " + en + ": " + ev
		if tb := o.get("traceback"); tb != nil {
			var lines []string
			for _, l := range tb.vals {
				s, _ := l.str()
				lines = append(lines, s)
			}
			body = strings.Join(lines, "\n")
		}
	case "execute_result", "display_data":
		label = "result"
		if kind == "display_data" {
			label = "display"
		}
		data := o.get("data")
		if t := data.get("text/plain"); t != nil {
			body = t.text()
		} else if t := data.get("text/markdown"); t != nil {
			body = t.text()
		}
		if data != nil {
			var others []string
			for i, k := range data.keys {
				if k == "text/plain" || (k == "text/markdown" && data.get("text/plain") == nil) {
					continue
				}
				others = append(others, fmt.Sprintf("%s, %s", k, textfmt.Bytes(int64(len(data.vals[i].text())))))
			}
			if len(others) > 0 {
				label += " (also " + strings.Join(others, "; ") + ")"
			}
		}
	default:
		label = kind
	}
	body = strings.TrimRight(stripANSI(body), "\n")
	lines := strings.Split(body, "\n")
	cut := ""
	if len(lines) > nbOutputLines {
		cut = fmt.Sprintf("… (%d more lines)\n", len(lines)-nbOutputLines)
		lines = lines[:nbOutputLines]
	}
	body = textfmt.ClipBytes(strings.Join(lines, "\n"), nbOutputBytes)
	out := "--- output: " + label + "\n"
	if body != "" {
		out += body + "\n"
	}
	return out + cut
}

// sourceValue stores text as the cell's source was stored: a list of
// lines each keeping its newline (nbformat's own form), or one string if
// the file used one.
func (nb *notebook) sourceValue(old *jval, text string) *jval {
	if old != nil && old.kind == 's' {
		return nb.style.encodeString(text)
	}
	arr := &jval{kind: '['}
	for _, line := range strings.SplitAfter(text, "\n") {
		if line == "" {
			continue
		}
		arr.vals = append(arr.vals, nb.style.encodeString(line))
	}
	return arr
}

func newCellID() string {
	var b [4]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// nbEdit is one notebook_edit call.
type nbEdit struct {
	Path     string  `json:"path"`
	Action   string  `json:"action"`
	Cell     *int    `json:"cell"`
	CellID   string  `json:"cell_id"`
	Source   *string `json:"source"`
	CellType string  `json:"cell_type"`
}

// locate finds the cell an edit names, by id or by index.
func (nb *notebook) locate(e nbEdit, forInsert bool) (int, error) {
	cells := nb.cells()
	if e.CellID != "" {
		for i, c := range cells {
			if cellID(c) == e.CellID {
				return i, nil
			}
		}
		var ids []string
		for _, c := range cells {
			if id := cellID(c); id != "" {
				ids = append(ids, id)
			}
		}
		if len(ids) == 0 {
			return 0, fmt.Errorf("no cell has id %q: this notebook's cells have no ids, so name the cell by index", e.CellID)
		}
		return 0, fmt.Errorf("no cell has id %q; the ids are %s", e.CellID, strings.Join(ids, ", "))
	}
	if e.Cell == nil {
		if forInsert {
			return len(cells), nil
		}
		return 0, errors.New("name the cell: give cell (its 0-based index) or cell_id")
	}
	i, limit := *e.Cell, len(cells)
	if forInsert {
		limit++
	}
	if i < 0 || i >= limit {
		if forInsert {
			return 0, fmt.Errorf("cell %d is out of range: an insert goes at 0..%d", i, len(cells))
		}
		return 0, fmt.Errorf("cell %d is out of range: this notebook has %s (0..%d)", i, textfmt.Plural(len(cells), "cell"), len(cells)-1)
	}
	return i, nil
}

func validCellType(t string) bool { return t == "code" || t == "markdown" || t == "raw" }

// apply carries out the edit on the notebook and says what it did.
func (nb *notebook) apply(e nbEdit) (string, error) {
	cellsV := nb.root.get("cells")
	switch e.Action {
	case "replace", "":
		i, err := nb.locate(e, false)
		if err != nil {
			return "", err
		}
		c := cellsV.vals[i]
		if e.CellType != "" && !validCellType(e.CellType) {
			return "", fmt.Errorf("cell_type must be code, markdown or raw, not %q", e.CellType)
		}
		if e.Source == nil && e.CellType == "" {
			return "", errors.New("replace needs source (the cell's new text), or cell_type to change only its type")
		}
		if e.Source != nil {
			c.set("source", nb.sourceValue(c.get("source"), *e.Source))
		}
		if e.CellType != "" && e.CellType != cellType(c) {
			c.set("cell_type", nb.style.encodeString(e.CellType))
		}
		nb.shapeCell(c)
		return fmt.Sprintf("replaced cell %d (%s)", i, cellType(c)), nil
	case "insert":
		i, err := nb.locate(e, true)
		if err != nil {
			return "", err
		}
		if e.CellID != "" {
			i++ // insert after the cell named by id
		}
		t := e.CellType
		if t == "" {
			t = "code"
		}
		if !validCellType(t) {
			return "", fmt.Errorf("cell_type must be code, markdown or raw, not %q", t)
		}
		c := &jval{kind: '{'}
		c.set("cell_type", nb.style.encodeString(t))
		if nb.minor() >= 5 {
			c.set("id", nb.style.encodeString(newCellID()))
		}
		c.set("metadata", &jval{kind: '{'})
		src := ""
		if e.Source != nil {
			src = *e.Source
		}
		c.set("source", nb.sourceValue(nil, src))
		nb.shapeCell(c)
		cellsV.vals = append(cellsV.vals[:i], append([]*jval{c}, cellsV.vals[i:]...)...)
		return fmt.Sprintf("inserted %s cell at %d", t, i), nil
	case "delete":
		i, err := nb.locate(e, false)
		if err != nil {
			return "", err
		}
		t := cellType(cellsV.vals[i])
		cellsV.vals = append(cellsV.vals[:i], cellsV.vals[i+1:]...)
		return fmt.Sprintf("deleted cell %d (%s)", i, t), nil
	}
	return "", fmt.Errorf("unknown action %q: use replace, insert or delete", e.Action)
}

// shapeCell gives a cell the fields its type requires and no others. A
// code cell whose source changed has its outputs and execution count
// cleared: they belong to the code that is gone, and leaving them makes
// the notebook claim the new code printed the old results.
func (nb *notebook) shapeCell(c *jval) {
	if cellType(c) == "code" {
		c.setSorted("execution_count", rawScalar("null"))
		c.setSorted("outputs", &jval{kind: '['})
		return
	}
	c.del("execution_count")
	c.del("outputs")
}

// NotebookEdit changes a Jupyter notebook cell by cell.
type NotebookEdit struct {
	Root  string
	WS    *Workspace
	Seen  *Seen
	Cache *IOCache
}

func (NotebookEdit) Name() string { return "notebook_edit" }
func (NotebookEdit) Description() string {
	return "Change one cell of a Jupyter notebook (.ipynb): replace a cell's source, insert a new cell, or delete one. read_file shows a notebook as numbered cells with their ids; name the cell by cell (0-based index) or cell_id. replace keeps the cell's type unless cell_type is given; insert puts the new cell at index cell (default: the end), or after the cell named by cell_id. Editing a code cell clears its outputs. The rest of the file, including its formatting and metadata, is left as it was. Use this instead of edit_file for notebooks."
}
func (NotebookEdit) Mutating() bool { return true }
func (NotebookEdit) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{
"path":{"type":"string","description":"Path of the .ipynb file"},
"action":{"type":"string","enum":["replace","insert","delete"],"description":"What to do (default replace)"},
"cell":{"type":"integer","description":"0-based cell index, as read_file shows it in brackets"},
"cell_id":{"type":"string","description":"The cell's id, instead of cell"},
"source":{"type":"string","description":"The cell's new source, for replace and insert"},
"cell_type":{"type":"string","enum":["code","markdown","raw"],"description":"For insert (default code), or to change a cell's type on replace"}},
"required":["path"],"additionalProperties":false}`)
}

// plan reads the notebook and applies the edit in memory.
func (t NotebookEdit) plan(ctx context.Context, input json.RawMessage) (e nbEdit, path string, before, after []byte, what string, err error) {
	if err = decode(input, &e); err != nil {
		return
	}
	if !isNotebookPath(e.Path) {
		err = fmt.Errorf("%s is not a notebook (.ipynb); use edit_file", e.Path)
		return
	}
	if path, err = resolveWriteOn(t.WS, t.Root, e.Path); err != nil {
		return
	}
	before, err = readFileOf(ctx, t.WS, path)
	if errors.Is(err, fs.ErrNotExist) {
		err = fmt.Errorf("%s %s; create a notebook with write_file first", e.Path, NotFound)
		return
	}
	if err != nil {
		return
	}
	nb, perr := parseNotebook(before)
	if perr != nil {
		err = fmt.Errorf("%s: %w", e.Path, perr)
		return
	}
	if what, err = nb.apply(e); err != nil {
		return
	}
	after = nb.bytes()
	return
}

func (t NotebookEdit) Run(ctx context.Context, input json.RawMessage) (string, error) {
	var peek nbEdit
	if err := decode(input, &peek); err != nil {
		return "", err
	}
	p, err := resolveWriteOn(t.WS, t.Root, peek.Path)
	if err != nil {
		return "", err
	}
	if err := t.Seen.CheckAt(ctx, t.WS, p, peek.Path, "edit"); err != nil {
		return "", err
	}
	if peek.Source != nil {
		if err := checkWriteSize("source", len(*peek.Source), "split it over several cells"); err != nil {
			return "", err
		}
	}
	_, path, _, after, what, err := t.plan(ctx, input)
	if err != nil {
		return "", err
	}
	if err := writeFileOf(ctx, t.WS, path, after); err != nil {
		return "", err
	}
	t.Cache.Drop(path)
	t.Seen.Record(path, after)
	nb, _ := parseNotebook(after)
	return fmt.Sprintf("Edited %s: %s; it now has %s", displayPath(rootOf(t.Root, t.WS), peek.Path), what, textfmt.Plural(len(nb.cells()), "cell")), nil
}

// Guard implements Guard, as write_file and edit_file do.
func (t NotebookEdit) Guard(input json.RawMessage) (bool, string) {
	return guardSelfModify(rootOf(t.Root, t.WS), input)
}

// Touches implements Toucher, which undo, provenance and path rules read.
func (t NotebookEdit) Touches(input json.RawMessage) ([]string, error) {
	return touchedPathOn(t.WS, t.Root, input)
}

// Result is what the edit would write, for impact analysis.
func (t NotebookEdit) Result(ctx context.Context, input json.RawMessage) (path, before, after string, err error) {
	_, path, b, a, _, err := t.plan(ctx, input)
	return path, string(b), string(a), err
}

// Preview implements Previewer. The diff is of the cells as read_file
// shows them, not of the JSON: that is what the edit means, and a JSON
// diff of a source list is quotes and commas.
func (t NotebookEdit) Preview(ctx context.Context, input json.RawMessage) (string, error) {
	e, _, before, after, _, err := t.plan(ctx, input)
	if err != nil {
		return "", err
	}
	b, _ := parseNotebook(before)
	a, _ := parseNotebook(after)
	d := diff.Unified(e.Path, b.render(e.Path), a.render(e.Path), 2)
	if d == "" {
		return "(no change)", nil
	}
	return d, nil
}
