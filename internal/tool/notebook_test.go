package tool

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// report.ipynb was written by nbformat 5.11 (nbformat 4.5, cell ids), with
// a stream output, an execute_result and a coloured traceback.
func copyNotebook(t *testing.T) (dir, path string) {
	t.Helper()
	data, err := os.ReadFile("testdata/report.ipynb")
	if err != nil {
		t.Fatal(err)
	}
	dir = t.TempDir()
	path = filepath.Join(dir, "report.ipynb")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return dir, path
}

func TestNotebookRoundTripIsByteIdentical(t *testing.T) {
	data, err := os.ReadFile("testdata/report.ipynb")
	if err != nil {
		t.Fatal(err)
	}
	nb, err := parseNotebook(data)
	if err != nil {
		t.Fatal(err)
	}
	if got := nb.bytes(); string(got) != string(data) {
		t.Fatalf("round trip changed the file:\n%s", got)
	}
	// Other writers: compact, ensure_ascii, a string source, odd key order.
	other := `{"nbformat": 4, "nbformat_minor": 2, "metadata": {"z": 1.50, "a": "\u00e9"}, "cells": [{"source": "x = 1\n", "cell_type": "code", "metadata": {}, "outputs": [], "execution_count": null}]}`
	nb, err = parseNotebook([]byte(other))
	if err != nil {
		t.Fatal(err)
	}
	if got := string(nb.bytes()); got != `{"nbformat":4,"nbformat_minor":2,"metadata":{"z":1.50,"a":"\u00e9"},"cells":[{"source":"x = 1\n","cell_type":"code","metadata":{},"outputs":[],"execution_count":null}]}` {
		t.Fatalf("compact notebook rewritten as %s", got)
	}
}

func TestReadFileRendersNotebookAsCells(t *testing.T) {
	dir, _ := copyNotebook(t)
	out, err := ReadFile{Root: dir}.Run(context.Background(), json.RawMessage(`{"path":"report.ipynb"}`))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"notebook report.ipynb · 4 cells · python",
		"[0] markdown · id intro",
		"# Sales report",
		"[1] code · id sum · run 1",
		"total = sum([1, 2, 3])",
		"--- output: stdout\n6\n",
		"--- output: result\n18.84955592153876",
		"--- output: error ZeroDivisionError: division by zero",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("rendering lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "\x1b") || strings.Contains(out, `"cell_type"`) {
		t.Errorf("rendering carries escapes or raw JSON:\n%s", out)
	}
}

func TestReadFileBrokenNotebookIsShownAsText(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "x.ipynb"), []byte("{not json\n"), 0o644)
	out, err := ReadFile{Root: dir}.Run(context.Background(), json.RawMessage(`{"path":"x.ipynb"}`))
	if err != nil || !strings.Contains(out, "{not json") {
		t.Fatalf("got %q, %v", out, err)
	}
}

func runNB(t *testing.T, dir, input string) (string, error) {
	t.Helper()
	return NotebookEdit{Root: dir}.Run(context.Background(), json.RawMessage(input))
}

func TestNotebookEditReplaceClearsOutputsAndKeepsTheRest(t *testing.T) {
	dir, path := copyNotebook(t)
	before, _ := os.ReadFile(path)
	out, err := runNB(t, dir, `{"path":"report.ipynb","cell_id":"pi","source":"total * math.e\nprint('ü')"}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "replaced cell 2 (code)") {
		t.Fatalf("result: %s", out)
	}
	after, _ := os.ReadFile(path)
	want := strings.Replace(string(before), `   "execution_count": 2,
   "id": "pi",
   "metadata": {},
   "outputs": [
    {
     "data": {
      "text/plain": [
       "18.84955592153876"
      ]
     },
     "execution_count": 2,
     "metadata": {},
     "output_type": "execute_result"
    }
   ],
   "source": [
    "total * math.pi"
   ]`, `   "execution_count": null,
   "id": "pi",
   "metadata": {},
   "outputs": [],
   "source": [
    "total * math.e\n",
    "print('ü')"
   ]`, 1)
	if want == string(before) {
		t.Fatal("fixture changed; update the expected text")
	}
	if string(after) != want {
		t.Fatalf("edit touched more than the cell:\n%s", after)
	}
}

func TestNotebookEditInsertAndDelete(t *testing.T) {
	dir, path := copyNotebook(t)
	if _, err := runNB(t, dir, `{"path":"report.ipynb","action":"insert","cell_id":"intro","cell_type":"markdown","source":"## Setup"}`); err != nil {
		t.Fatal(err)
	}
	if _, err := runNB(t, dir, `{"path":"report.ipynb","action":"insert","source":"print('end')"}`); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	nb, err := parseNotebook(data)
	if err != nil {
		t.Fatal(err)
	}
	cells := nb.cells()
	if len(cells) != 6 || cellType(cells[1]) != "markdown" || cells[1].get("source").text() != "## Setup" {
		t.Fatalf("insert after intro went wrong:\n%s", nb.render("x"))
	}
	if cells[1].get("outputs") != nil || len(cellID(cells[1])) != 8 {
		t.Fatalf("new markdown cell malformed: %s", nb.style.marshal(cells[1]))
	}
	last := cells[5]
	if cellType(last) != "code" || string(scalarOf(last.get("execution_count"))) != "null" || last.get("outputs") == nil {
		t.Fatalf("appended code cell malformed: %s", nb.style.marshal(last))
	}
	if strings.Join(last.keys, ",") != "cell_type,execution_count,id,metadata,outputs,source" {
		t.Fatalf("new cell's keys not in nbformat order: %v", last.keys)
	}
	if _, err := runNB(t, dir, `{"path":"report.ipynb","action":"delete","cell":0}`); err != nil {
		t.Fatal(err)
	}
	data, _ = os.ReadFile(path)
	nb, _ = parseNotebook(data)
	if cellID(nb.cells()[0]) == "intro" || len(nb.cells()) != 5 {
		t.Fatalf("delete missed:\n%s", nb.render("x"))
	}
}

func TestNotebookEditTypeChange(t *testing.T) {
	dir, path := copyNotebook(t)
	if _, err := runNB(t, dir, `{"path":"report.ipynb","cell":3,"cell_type":"markdown","source":"no more division"}`); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	nb, _ := parseNotebook(data)
	c := nb.cells()[3]
	if cellType(c) != "markdown" || c.get("outputs") != nil || c.get("execution_count") != nil {
		t.Fatalf("code→markdown left code fields: %s", nb.style.marshal(c))
	}
}

func TestNotebookEditErrorsNameTheWayOut(t *testing.T) {
	dir, _ := copyNotebook(t)
	for input, want := range map[string]string{
		`{"path":"report.ipynb","cell":9,"source":"x"}`:                   "this notebook has 4 cells (0..3)",
		`{"path":"report.ipynb","cell_id":"nope","source":"x"}`:           "the ids are intro, sum, pi, boom",
		`{"path":"report.ipynb","source":"x"}`:                            "give cell",
		`{"path":"report.ipynb","action":"move","cell":0}`:                "unknown action",
		`{"path":"main.py","cell":0,"source":"x"}`:                        "not a notebook",
		`{"path":"missing.ipynb","cell":0,"source":"x"}`:                  "does not exist",
		`{"path":"report.ipynb","cell":0,"cell_type":"html","source":""}`: "cell_type must be",
	} {
		_, err := runNB(t, dir, input)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: got %v, want %q", input, err, want)
		}
	}
}

func TestNotebookEditPreviewIsACellDiff(t *testing.T) {
	dir, path := copyNotebook(t)
	before, _ := os.ReadFile(path)
	pv, err := NotebookEdit{Root: dir}.Preview(context.Background(), json.RawMessage(`{"path":"report.ipynb","cell":1,"source":"print(42)"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(pv, "-total = sum([1, 2, 3])") || !strings.Contains(pv, "+print(42)") || strings.Contains(pv, `"source"`) {
		t.Fatalf("preview:\n%s", pv)
	}
	if after, _ := os.ReadFile(path); string(after) != string(before) {
		t.Fatal("preview wrote the file")
	}
	paths, err := NotebookEdit{Root: dir}.Touches(json.RawMessage(`{"path":"report.ipynb","cell":1}`))
	if err != nil || len(paths) != 1 || filepath.Base(paths[0]) != "report.ipynb" {
		t.Fatalf("touches: %v %v", paths, err)
	}
}

func TestNotebookEditRefusesAFileChangedSinceRead(t *testing.T) {
	dir, path := copyNotebook(t)
	seen := &Seen{}
	if _, err := (ReadFile{Root: dir, Seen: seen}).Run(context.Background(), json.RawMessage(`{"path":"report.ipynb"}`)); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	os.WriteFile(path, []byte(strings.Replace(string(data), "Sales", "Revenue", 1)), 0o644)
	_, err := NotebookEdit{Root: dir, Seen: seen}.Run(context.Background(), json.RawMessage(`{"path":"report.ipynb","cell":1,"source":"x"}`))
	if err == nil {
		t.Fatal("edited a notebook that changed since it was read")
	}
}

func TestNotebookASCIIStyleKeepsEscaping(t *testing.T) {
	src := "{\n \"cells\": [\n  {\n   \"cell_type\": \"markdown\",\n   \"metadata\": {},\n   \"source\": [\n    \"caf\\u00e9\"\n   ]\n  }\n ],\n \"metadata\": {},\n \"nbformat\": 4,\n \"nbformat_minor\": 4\n}\n"
	nb, err := parseNotebook([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := nb.apply(nbEdit{Action: "replace", Cell: new(int), Source: ptr("naïve 😀")}); err != nil {
		t.Fatal(err)
	}
	out := string(nb.bytes())
	if !strings.Contains(out, `"na\u00efve \ud83d\ude00"`) || hasNonASCII([]byte(out)) {
		t.Fatalf("ascii style lost:\n%s", out)
	}
	if strings.Contains(out, `"id"`) {
		t.Fatal("a 4.4 notebook got a cell id")
	}
}

func ptr(s string) *string { return &s }

func TestNotebookEditTypeOnlyKeepsSource(t *testing.T) {
	dir, path := copyNotebook(t)
	if _, err := runNB(t, dir, `{"path":"report.ipynb","cell":1,"cell_type":"raw"}`); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	nb, _ := parseNotebook(data)
	if c := nb.cells()[1]; cellType(c) != "raw" || !strings.Contains(c.get("source").text(), "import math") {
		t.Fatalf("type-only change lost the source: %s", nb.style.marshal(c))
	}
	if _, err := runNB(t, dir, `{"path":"report.ipynb","cell":1}`); err == nil || !strings.Contains(err.Error(), "needs source") {
		t.Fatalf("replace with nothing: %v", err)
	}
}
