package mermaid

import (
	"fmt"
	"strings"
	"testing"
)

func TestParse(t *testing.T) {
	cases := []struct {
		name   string
		src    string
		kind   Kind
		dir    Direction
		nodes  []string
		edges  []string // "from->to label"
		actors []string
		msgs   []string // "from->to label" with a leading "~" when dashed
		err    bool
	}{
		{
			name:  "flowchart with shapes and both label forms",
			src:   "flowchart TD\n  A[Start] --> B{ok?}\n  B -->|yes| C((done))\n  B -- no --> D(retry)\n",
			nodes: []string{"Start", "ok?", "done", "retry"},
			edges: []string{"0->1 ", "1->2 yes", "1->3 no"},
		},
		{
			name:  "graph is a flowchart, and a chain is three edges",
			src:   "graph LR\n  A --> B --> C --> D\n",
			dir:   LeftRight,
			nodes: []string{"A", "B", "C", "D"},
			edges: []string{"0->1 ", "1->2 ", "2->3 "},
		},
		{
			name:  "comments, semicolons, quoted labels and a bare node",
			src:   "flowchart TD\n%% a comment\n  A[\"a, b\"];\n  A --> B;\n  E\n",
			nodes: []string{"a, b", "B", "E"},
			edges: []string{"0->1 "},
		},
		{
			name:  "direction statement",
			src:   "flowchart TD\n  direction LR\n  A --> B\n",
			dir:   LeftRight,
			nodes: []string{"A", "B"},
			edges: []string{"0->1 "},
		},
		{
			name:   "sequence diagram with participants and a dashed reply",
			src:    "sequenceDiagram\n  participant U as User\n  participant S\n  U->>S: ask\n  S-->>U: answer\n",
			kind:   Sequence,
			actors: []string{"User", "S"},
			msgs:   []string{"0->1 ask", "~1->0 answer"},
		},
		{
			name:  "state diagram: [*] becomes start and end",
			src:   "stateDiagram-v2\n  [*] --> Idle\n  Idle --> Busy : work\n  Busy --> [*]\n",
			kind:  State,
			nodes: []string{"start", "Idle", "Busy", "end"},
			edges: []string{"0->1 ", "1->2 work", "2->3 "},
		},
		{name: "an unsupported kind falls back", src: "journey\n  title A\n", err: true},
		{name: "gantt falls back", src: "gantt\n  title A\n", err: true},
		{name: "a subgraph falls back", src: "flowchart TD\n  subgraph one\n  A --> B\n  end\n", err: true},
		{name: "bottom-up would come out upside down", src: "flowchart BT\n  A --> B\n", err: true},
		{name: "a note in a sequence falls back", src: "sequenceDiagram\n  A->>B: hi\n  Note over A: thinking\n", err: true},
		{name: "a loop in a sequence falls back", src: "sequenceDiagram\n  loop every day\n  A->>B: hi\n  end\n", err: true},
		{name: "a composite state falls back", src: "stateDiagram-v2\n  state Big {\n  A --> B\n  }\n", err: true},
		{name: "an unclosed node is malformed", src: "flowchart TD\n  A[Start --> B\n", err: true},
		{name: "a directive falls back", src: "%%{init: {'theme':'dark'}}%%\nflowchart TD\n  A --> B\n", err: true},
		{name: "front matter falls back", src: "---\ntitle: x\n---\nflowchart TD\n  A --> B\n", err: true},
		{name: "empty source falls back", src: "\n\n", err: true},
		{name: "prose is not a diagram", src: "this is not mermaid at all\n", err: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d, err := Parse(tc.src)
			if tc.err {
				if err == nil {
					t.Fatalf("expected a fallback, got %+v", d)
				}
				return
			}
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			if d.Kind != tc.kind || d.Dir != tc.dir {
				t.Errorf("kind/dir = %v/%v, want %v/%v", d.Kind, d.Dir, tc.kind, tc.dir)
			}
			var nodes []string
			for _, n := range d.Nodes {
				nodes = append(nodes, n.Label)
			}
			if strings.Join(nodes, ",") != strings.Join(tc.nodes, ",") {
				t.Errorf("nodes = %v, want %v", nodes, tc.nodes)
			}
			var edges []string
			for _, e := range d.Edges {
				edges = append(edges, fmt.Sprintf("%d->%d %s", e.From, e.To, e.Label))
			}
			if strings.Join(edges, "|") != strings.Join(tc.edges, "|") {
				t.Errorf("edges = %v, want %v", edges, tc.edges)
			}
			var actors []string
			for _, a := range d.Actors {
				actors = append(actors, a.Label)
			}
			if strings.Join(actors, ",") != strings.Join(tc.actors, ",") {
				t.Errorf("actors = %v, want %v", actors, tc.actors)
			}
			var msgs []string
			for _, m := range d.Msgs {
				dash := ""
				if m.Dashed {
					dash = "~"
				}
				msgs = append(msgs, fmt.Sprintf("%s%d->%d %s", dash, m.From, m.To, m.Label))
			}
			if strings.Join(msgs, "|") != strings.Join(tc.msgs, "|") {
				t.Errorf("msgs = %v, want %v", msgs, tc.msgs)
			}
		})
	}
}

// A label longer than the cap is cut rather than widening the diagram
// past the terminal.
func TestLongLabelIsClipped(t *testing.T) {
	d, err := Parse("flowchart TD\n  A[" + strings.Repeat("x", 100) + "] --> B\n")
	if err != nil {
		t.Fatal(err)
	}
	if got := runeLen(d.Nodes[0].Label); got != maxLabel {
		t.Fatalf("label is %d runes, want %d", got, maxLabel)
	}
	if !strings.HasSuffix(d.Nodes[0].Label, "…") {
		t.Fatalf("a cut label should say so: %q", d.Nodes[0].Label)
	}
}
