// Package mermaid parses the part of the mermaid diagram language that
// shows up in coding conversations and lays it out as terminal text.
//
// It is deliberately a subset: flowcharts (top-down and left-right),
// sequence diagrams and state diagrams. Anything else — a class diagram,
// a gantt chart, a subgraph, a note, a loop — is refused rather than
// approximated, because a diagram that quietly drops half of what the
// model drew is worse than showing the source. Every refusal is an
// error from Parse, and the caller falls back to the code block.
package mermaid

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"
)

// Kind is the diagram kind a source was recognised as.
type Kind int

const (
	// Flowchart covers both `flowchart` and `graph`, and is what a
	// `stateDiagram` is turned into as well.
	Flowchart Kind = iota
	// Sequence is `sequenceDiagram`.
	Sequence
	// State is `stateDiagram` / `stateDiagram-v2`; it is laid out as a
	// flowchart whose `[*]` ends became start and end nodes.
	State
)

// Direction is the reading direction of a flowchart.
type Direction int

const (
	// TopDown is mermaid's TD/TB: ranks are rows.
	TopDown Direction = iota
	// LeftRight is mermaid's LR: ranks are columns.
	LeftRight
)

// Node is one box.
type Node struct {
	ID    string
	Label string
}

// Edge joins two nodes by their index in Diagram.Nodes.
type Edge struct {
	From, To int
	Label    string
}

// Message is one arrow of a sequence diagram, between two actors given
// by their index in Diagram.Actors.
type Message struct {
	From, To int
	Label    string
	Dashed   bool
}

// Diagram is the parsed subset: a graph for a flowchart or a state
// machine, lanes and messages for a sequence diagram.
type Diagram struct {
	Kind Kind
	Dir  Direction

	Nodes []Node
	Edges []Edge

	Actors []Node
	Msgs   []Message

	index map[string]int
}

// Limits. A diagram past these is not laid out: the layout is cheap but
// the result would be unreadable in a transcript long before it is slow.
const (
	maxNodes  = 40
	maxEdges  = 80
	maxLabel  = 40
	maxSource = 16 << 10
)

var (
	errUnsupported = errors.New("mermaid: unsupported diagram")
	errTooBig      = errors.New("mermaid: diagram too large")
)

// Parse reads the body of a ```mermaid fence. A non-nil error means the
// caller should show the source instead.
func Parse(src string) (*Diagram, error) {
	if len(src) > maxSource {
		return nil, errTooBig
	}
	lines, err := statements(src)
	if err != nil {
		return nil, err
	}
	if len(lines) == 0 {
		return nil, errUnsupported
	}
	head := lines[0]
	switch {
	case matchHead(head, "flowchart"), matchHead(head, "graph"):
		return parseFlow(head, lines[1:], Flowchart)
	case matchHead(head, "stateDiagram-v2"), matchHead(head, "stateDiagram"):
		return parseState(head, lines[1:])
	case matchHead(head, "sequenceDiagram"):
		return parseSequence(lines[1:])
	}
	return nil, errUnsupported
}

func matchHead(line, word string) bool {
	if !strings.HasPrefix(line, word) {
		return false
	}
	rest := line[len(word):]
	return rest == "" || rest[0] == ' ' || rest[0] == '\t'
}

// statements splits the source into trimmed statements: comments and
// blank lines dropped, a trailing `;` removed. Front matter and
// directives (`---`, `%%{init}%%`) are refused rather than ignored,
// because they usually mean the diagram relies on something we do not
// render.
func statements(src string) ([]string, error) {
	var out []string
	for _, raw := range strings.Split(src, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "%%{") {
			return nil, errUnsupported
		}
		if strings.HasPrefix(line, "%%") {
			continue
		}
		if len(out) == 0 && line == "---" {
			return nil, errUnsupported // YAML front matter
		}
		line = strings.TrimSuffix(line, ";")
		if line = strings.TrimSpace(line); line != "" {
			out = append(out, line)
		}
	}
	return out, nil
}

func (d *Diagram) node(id, label string) int {
	if i, ok := d.index[id]; ok {
		if label != "" && d.Nodes[i].Label == d.Nodes[i].ID {
			d.Nodes[i].Label = clip(label)
		}
		return i
	}
	if label == "" {
		label = id
	}
	d.Nodes = append(d.Nodes, Node{ID: id, Label: clip(label)})
	d.index[id] = len(d.Nodes) - 1
	return len(d.Nodes) - 1
}

func clip(s string) string {
	s = strings.TrimSpace(strings.Join(strings.Fields(s), " "))
	if utf8.RuneCountInString(s) <= maxLabel {
		return s
	}
	r := []rune(s)
	return strings.TrimRight(string(r[:maxLabel-1]), " ") + "…"
}

// ---------------------------------------------------------------- flowchart

var (
	// arrowRe finds the link operators of the supported subset. The
	// dash-label form (`A -- yes --> B`) is picked off the left-hand
	// side afterwards, so only the arrow itself is matched here.
	arrowRe = regexp.MustCompile(`--+>|-\.-*->|==+>|-{3,}|={3,}`)
	// nodeRe is one node reference: an id and an optional shape.
	nodeRe = regexp.MustCompile(`^([A-Za-z0-9_.\-]+)(?:(\(\(|\(\[|\[\[|\[\(|\[|\(|\{\{|\{)(.*?)(\)\)|\]\)|\]\]|\)\]|\]|\)|\}\}|\})|)$`)
	// dashLabelRe strips `-- yes` / `-. yes .` / `== yes` off the end of
	// the segment before an arrow.
	dashLabelRe = regexp.MustCompile(`^(.*?)\s*(?:--|-\.|==)\s*([^-=.][^-=]*?)\s*\.?$`)
	pipeRe      = regexp.MustCompile(`^\|([^|]*)\|\s*`)
)

func parseFlow(head string, rest []string, kind Kind) (*Diagram, error) {
	d := &Diagram{Kind: kind, index: map[string]int{}}
	switch strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(head, "flowchart"), "graph")) {
	case "", "TD", "TB":
		d.Dir = TopDown
	case "LR":
		d.Dir = LeftRight
	default:
		// BT and RL would come out upside down, and a diagram drawn the
		// wrong way round is worse than its source.
		return nil, errUnsupported
	}
	for _, line := range rest {
		if err := d.flowStatement(line); err != nil {
			return nil, err
		}
	}
	return d, d.check()
}

func (d *Diagram) flowStatement(line string) error {
	switch first := strings.ToLower(strings.Fields(line)[0]); first {
	case "direction":
		return d.direction(line)
	case "subgraph", "end", "class", "classdef", "style", "click", "linkstyle", "note", "state":
		return errUnsupported
	}
	locs := arrowRe.FindAllStringIndex(line, -1)
	if len(locs) == 0 {
		_, err := d.nodeRef(line)
		return err
	}
	prev, pos, pending := -1, 0, ""
	for _, loc := range locs {
		if loc[0] < pos {
			continue // inside a target already consumed
		}
		seg, label := line[pos:loc[0]], pending
		pending = ""
		if s, l, ok := splitDashLabel(seg); ok {
			seg, label = s, l
		}
		from := prev
		if from < 0 {
			i, err := d.nodeRef(seg)
			if err != nil {
				return err
			}
			from = i
		} else if strings.TrimSpace(seg) != "" {
			return errUnsupported
		}
		pos = loc[1]
		if m := pipeRe.FindStringSubmatchIndex(line[pos:]); m != nil {
			label = line[pos+m[2] : pos+m[3]]
			pos += m[1]
		}
		// The target runs to the next arrow, or to the end of the line.
		endsAt := len(line)
		if next := arrowRe.FindStringIndex(line[pos:]); next != nil {
			endsAt = pos + next[0]
		}
		target := line[pos:endsAt]
		if s, l, ok := splitDashLabel(target); ok {
			target, pending = s, l
		}
		to, err := d.nodeRef(target)
		if err != nil {
			return err
		}
		d.Edges = append(d.Edges, Edge{From: from, To: to, Label: clip(label)})
		prev, pos = to, endsAt
	}
	return nil
}

// direction reads a `direction` statement, which both flowcharts and
// state diagrams have.
func (d *Diagram) direction(line string) error {
	switch strings.TrimSpace(line[len("direction"):]) {
	case "TD", "TB":
		d.Dir = TopDown
	case "LR":
		d.Dir = LeftRight
	default:
		return errUnsupported
	}
	return nil
}

// splitDashLabel picks the `-- yes` form of an edge label off the
// segment that precedes the arrow, so `A -- yes --> B` reads the same
// as `A -->|yes| B`.
func splitDashLabel(seg string) (node, label string, ok bool) {
	m := dashLabelRe.FindStringSubmatch(strings.TrimSpace(seg))
	if m == nil {
		return seg, "", false
	}
	return m[1], m[2], true
}

// nodeRef resolves one node reference, defining it if it is new.
func (d *Diagram) nodeRef(s string) (int, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, errUnsupported
	}
	m := nodeRe.FindStringSubmatch(s)
	if m == nil {
		return 0, fmt.Errorf("mermaid: cannot parse node %q", s)
	}
	label := strings.TrimSpace(m[3])
	if m[2] != "" && m[4] == "" {
		return 0, fmt.Errorf("mermaid: unclosed node %q", s)
	}
	label = strings.Trim(label, `"`)
	return d.node(m[1], label), nil
}

func (d *Diagram) check() error {
	switch {
	case len(d.Nodes)+len(d.Actors) == 0:
		return errUnsupported
	case len(d.Nodes) > maxNodes, len(d.Actors) > maxNodes:
		return errTooBig
	case len(d.Edges) > maxEdges, len(d.Msgs) > maxEdges:
		return errTooBig
	}
	return nil
}

// ---------------------------------------------------------------- state

var stateAliasRe = regexp.MustCompile(`^state\s+"([^"]*)"\s+as\s+([A-Za-z0-9_.\-]+)$`)

func parseState(head string, rest []string) (*Diagram, error) {
	if strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(head, "stateDiagram-v2"), "stateDiagram")) != "" {
		return nil, errUnsupported
	}
	d := &Diagram{Kind: State, index: map[string]int{}}
	ends := 0
	for _, line := range rest {
		if m := stateAliasRe.FindStringSubmatch(line); m != nil {
			d.node(m[2], m[1])
			continue
		}
		if strings.ToLower(strings.Fields(line)[0]) == "direction" {
			if err := d.direction(line); err != nil {
				return nil, err
			}
			continue
		}
		// Everything that is not a transition — a composite `state X {`,
		// a note, a concurrency `--` — changes what the machine means, so
		// it is refused rather than skipped.
		if !strings.Contains(line, "-->") {
			return nil, errUnsupported
		}
		body, label := line, ""
		if i := strings.Index(line, ":"); i >= 0 {
			body, label = line[:i], clip(strings.TrimSpace(line[i+1:]))
		}
		parts := strings.SplitN(body, "-->", 2)
		if len(parts) != 2 {
			return nil, errUnsupported
		}
		from, err := d.stateRef(strings.TrimSpace(parts[0]), "start", &ends)
		if err != nil {
			return nil, err
		}
		to, err := d.stateRef(strings.TrimSpace(parts[1]), "end", &ends)
		if err != nil {
			return nil, err
		}
		d.Edges = append(d.Edges, Edge{From: from, To: to, Label: label})
	}
	return d, d.check()
}

// stateRef resolves one end of a transition. `[*]` is the start marker
// on the left of an arrow and the end marker on its right, so it becomes
// two different nodes.
func (d *Diagram) stateRef(s, marker string, ends *int) (int, error) {
	if s == "[*]" {
		*ends++
		return d.node("[*]"+marker, marker), nil
	}
	return d.nodeRef(s)
}

// ---------------------------------------------------------------- sequence

var (
	// An actor id may not hold a dash: it would swallow the arrow.
	msgRe         = regexp.MustCompile(`^([A-Za-z0-9_.]+)\s*(-->>|->>|-->|->)\s*([A-Za-z0-9_.]+)\s*:\s*(.*)$`)
	participantRe = regexp.MustCompile(`^(?:participant|actor)\s+([A-Za-z0-9_.]+)(?:\s+as\s+(.+))?$`)
)

func parseSequence(rest []string) (*Diagram, error) {
	d := &Diagram{Kind: Sequence, index: map[string]int{}}
	actor := func(id, label string) int {
		if i, ok := d.index[id]; ok {
			if label != "" && d.Actors[i].Label == d.Actors[i].ID {
				d.Actors[i].Label = clip(label)
			}
			return i
		}
		if label == "" {
			label = id
		}
		d.Actors = append(d.Actors, Node{ID: id, Label: clip(label)})
		d.index[id] = len(d.Actors) - 1
		return len(d.Actors) - 1
	}
	for _, line := range rest {
		if m := participantRe.FindStringSubmatch(line); m != nil {
			actor(m[1], strings.Trim(strings.TrimSpace(m[2]), `"`))
			continue
		}
		m := msgRe.FindStringSubmatch(line)
		if m == nil {
			// Notes, loops, alt/opt/par, activation: each of them changes
			// what the diagram means, so none of them is ignored.
			return nil, errUnsupported
		}
		from, to := actor(m[1], ""), actor(m[3], "")
		if from == to {
			return nil, errUnsupported // self-call needs a loop arrow
		}
		d.Msgs = append(d.Msgs, Message{From: from, To: to, Label: clip(m[4]), Dashed: strings.HasPrefix(m[2], "--")})
	}
	if len(d.Actors) < 2 || len(d.Msgs) == 0 {
		return nil, errUnsupported
	}
	return d, d.check()
}
