package agent

import (
	"encoding/json"
	"strings"

	"github.com/xmonader/janetcode/internal/session"
)

// The by-construction waste labels. They are the Go port of the labeller in
// docs/benchmarks/decisions/build-stuck-corpus.py, moved here so the eval
// report, the profiler and the corpus builder cannot drift apart. Nothing
// about them is a judgement: each is a fact the log already records.
const (
	// WasteNone is a call that did its job.
	WasteNone = ""
	// WasteErrored is a call whose result came back an error, or that a
	// permission rule refused.
	WasteErrored = "errored"
	// WasteLoop is a call inside a repeat: three or more identical calls in a
	// row, or three calls on the same (tool, path) inside a window of five.
	WasteLoop = "loop"
	// WasteDeadTail is a call a failing run made after its last successful
	// change and after its last green gate — work that went nowhere because
	// the run did not end well.
	WasteDeadTail = "dead_tail"
)

// wasteLoopRun is how many identical calls in a row make a loop, and
// wasteLoopWindow how wide the same-path window is.
const (
	wasteLoopRun    = 3
	wasteLoopWindow = 5
)

// mutatingTools are the calls that change the tree, and so are the only ones
// that can be the last progress a run made.
var mutatingTools = map[string]bool{
	"edit_file": true, "ast_edit": true, "write_file": true, "apply_patch": true,
	"create_file": true, "multi_edit": true, "insert_lines": true,
	"replace_lines": true,
}

// WasteLabels labels every tool call of one recorded run, in call order.
// passed says whether the run reached its goal, which the dead-tail rule
// needs: a run that passed has no dead tail by definition.
func WasteLabels(events []session.Event, passed bool) []string {
	type callRec struct {
		name, args string
		seq        int
		failed     bool
	}
	var calls []callRec
	at := map[string]int{}
	gateOKSeq := 0
	for _, e := range events {
		switch e.Type {
		case session.TypeToolCall:
			at[e.CallID] = len(calls)
			calls = append(calls, callRec{name: e.Name, args: e.Arguments, seq: e.Seq})
		case session.TypeToolResult:
			if i, ok := at[e.CallID]; ok && (e.IsError || e.Denied) {
				calls[i].failed = true
			}
		case session.TypeGate:
			if !e.IsError && e.Seq > gateOKSeq {
				gateOKSeq = e.Seq
			}
		}
	}
	labels := make([]string, len(calls))

	// (a) errored or denied.
	for i := range calls {
		if calls[i].failed {
			labels[i] = WasteErrored
		}
	}

	// (b) loops: a run of identical calls, then a same-path window.
	for i := 0; i < len(calls); {
		j := i
		key := calls[i].name + "\x00" + normArgs(calls[i].args)
		for j+1 < len(calls) && calls[j+1].name+"\x00"+normArgs(calls[j+1].args) == key {
			j++
		}
		if j-i+1 >= wasteLoopRun {
			setIfNone(labels, i, j+1, WasteLoop)
		}
		i = j + 1
	}
	for i := range calls {
		end := i + wasteLoopWindow
		if end > len(calls) {
			end = len(calls)
		}
		count := map[string]int{}
		for k := i; k < end; k++ {
			if p := wastePath(calls[k].args); p != "" {
				count[calls[k].name+"\x00"+p]++
			}
		}
		for key, n := range count {
			if n < wasteLoopRun {
				continue
			}
			for k := i; k < end; k++ {
				if p := wastePath(calls[k].args); p != "" && calls[k].name+"\x00"+p == key && labels[k] == WasteNone {
					labels[k] = WasteLoop
				}
			}
		}
	}

	// (c) dead tail, only for a run that did not pass.
	if !passed {
		lastProgress := -1
		for i := range calls {
			if mutatingTools[calls[i].name] && !calls[i].failed {
				lastProgress = i
			}
		}
		for i := range calls {
			if i > lastProgress && calls[i].seq > gateOKSeq && labels[i] == WasteNone {
				labels[i] = WasteDeadTail
			}
		}
	}
	return labels
}

func setIfNone(labels []string, from, to int, label string) {
	for i := from; i < to; i++ {
		if labels[i] == WasteNone {
			labels[i] = label
		}
	}
}

func normArgs(s string) string { return strings.Join(strings.Fields(s), " ") }

// wastePath is the file a call names, under any of the key spellings the tools
// and MCP servers use.
func wastePath(arguments string) string {
	var d map[string]any
	if json.Unmarshal([]byte(arguments), &d) != nil {
		return ""
	}
	for _, k := range []string{"path", "file", "filename", "file_path"} {
		if v, ok := d[k].(string); ok && v != "" {
			return v
		}
	}
	return ""
}
