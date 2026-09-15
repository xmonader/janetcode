// Fork lineage: how a listing of sessions is shaped into the tree /fork
// actually made. Meta.ForkOf records which session a fork was copied
// from; Lineage turns a flat listing into parents with their forks
// underneath, so /resume and --sessions show where a conversation came
// from instead of a row that happens to sit next to its parent.
//
// It is a pure function over metadata: no disk, no agent, no terminal.
// Every front-end (the picker, the CLI listing, the web UI) reads the
// same nodes and only decides how to draw them.
package session

import "strings"

// MaxLineageDepth caps how far a chain of forks is indented. A fork of a
// fork of a fork is still shown, at the cap, with Deeper set — a long
// chain would otherwise walk off the right of the screen.
const MaxLineageDepth = 3

// Node is one session in a lineage listing, in the order it is drawn.
type Node struct {
	Info Info
	// Depth is the indent level, 0 for a root, capped at MaxLineageDepth.
	Depth int
	// Deeper marks a session whose real depth is past the cap, drawn at
	// the cap with an ellipsis instead of its true indent.
	Deeper bool
	// Current marks the session the caller is in.
	Current bool
	// Orphan marks a fork listed at top level because its parent is not
	// in this listing: deleted, settled out of view, or made in another
	// directory.
	Orphan bool
	// Context marks a node FilterLineage kept only because a match is
	// somewhere below it, not because it matched itself.
	Context bool
	// parent indexes the node this one hangs under, or -1 at top level.
	// Used by FilterLineage to keep a match's ancestors.
	parent int
}

// Lineage arranges infos (in the order List returned them) into fork
// order: roots keep the order they came in, and each session's forks
// follow it immediately in that same order — which is the listing's own,
// most recently active first with settled ones last. currentID, when it
// names a session in the list, marks the row the caller is in.
func Lineage(infos []Info, currentID string) []Node {
	at := make(map[string]int, len(infos))
	for i, in := range infos {
		if _, dup := at[in.Meta.ID]; !dup {
			at[in.Meta.ID] = i
		}
	}
	children := make(map[string][]int, len(infos))
	var roots []int
	for i, in := range infos {
		p := in.Meta.ForkOf
		if p != "" && p != in.Meta.ID {
			if _, ok := at[p]; ok {
				children[p] = append(children[p], i)
				continue
			}
		}
		roots = append(roots, i)
	}
	out := make([]Node, 0, len(infos))
	done := make(map[string]bool, len(infos))
	var walk func(i, depth, parent int)
	walk = func(i, depth, parent int) {
		in := infos[i]
		if done[in.Meta.ID] {
			return
		}
		done[in.Meta.ID] = true
		n := Node{Info: in, Depth: min(depth, MaxLineageDepth), Deeper: depth > MaxLineageDepth, Current: in.Meta.ID == currentID, parent: parent}
		if depth == 0 && in.Meta.ForkOf != "" {
			n.Orphan = true
		}
		here := len(out)
		out = append(out, n)
		for _, c := range children[in.Meta.ID] {
			walk(c, depth+1, here)
		}
	}
	for _, r := range roots {
		walk(r, 0, -1)
	}
	// Anything left is in a cycle of ForkOf links (a session forked from
	// its own descendant, a hand-edited file): it was never reached from
	// a root, and dropping it would hide a session from /resume.
	for i, in := range infos {
		if !done[in.Meta.ID] {
			walk(i, 0, -1)
		}
	}
	return out
}

// SearchKey is what a search field matches a node on.
func (n Node) SearchKey() string { return n.Info.Meta.ID + " " + n.Info.Label() }

// Prefix is what goes before the label: the indent for its depth, a
// branch mark for a fork, and an ellipsis for a chain past the cap.
func (n Node) Prefix() string {
	if n.Depth == 0 {
		return ""
	}
	mark := "└ "
	if n.Deeper {
		mark = "… "
	}
	return strings.Repeat("  ", n.Depth-1) + mark
}

// Mark is the one-word tag for the row: where you are, or done with.
func (n Node) Mark() string {
	switch {
	case n.Current:
		return "current"
	case n.Info.Meta.Settled:
		return "settled"
	}
	return ""
}

// Note is the aside a row needs to make sense on its own: today, only a
// fork whose parent is not in this listing.
func (n Node) Note() string {
	if !n.Orphan {
		return ""
	}
	return "fork of " + n.Info.Meta.ForkOf + ", not listed here"
}

// FilterLineage keeps the nodes matching every word of query (matched on
// SearchKey, case-insensitively) together with their ancestors, so a
// match is never shown adrift from the conversation it branched off.
// Ancestors kept only for that reason are marked Context. An empty query
// keeps everything. The result is for display: its nodes still carry the
// parent links of the listing they came from, so it is not itself a
// lineage to filter again.
func FilterLineage(nodes []Node, query string) []Node {
	words := strings.Fields(strings.ToLower(strings.TrimSpace(query)))
	if len(words) == 0 {
		return append([]Node(nil), nodes...)
	}
	hit := make([]bool, len(nodes))
	keep := make([]bool, len(nodes))
	for i, n := range nodes {
		key := strings.ToLower(n.SearchKey())
		ok := true
		for _, w := range words {
			if !strings.Contains(key, w) {
				ok = false
				break
			}
		}
		hit[i], keep[i] = ok, ok
	}
	// Descending, so an ancestor kept here has its own ancestors kept
	// when the loop reaches it.
	for i := len(nodes) - 1; i >= 0; i-- {
		if keep[i] && nodes[i].parent >= 0 {
			keep[nodes[i].parent] = true
		}
	}
	var out []Node
	for i, n := range nodes {
		if !keep[i] {
			continue
		}
		n.Context = !hit[i]
		out = append(out, n)
	}
	return out
}
