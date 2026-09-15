package mermaid

import "sort"

// The layered layout behind flowcharts and state machines: rank the
// nodes by longest path, break every edge that spans more than one rank
// with invisible nodes so each segment joins neighbouring ranks, then
// order each rank by the barycentre of its neighbours to take some of
// the crossings out. It is the classic Sugiyama sketch, minus the parts
// that need floating-point placement, and it is deterministic: no map
// iteration decides anything, and every sort is stable.

type lnode struct {
	label string
	dummy bool
	rank  int
	w, h  int
	x, y  int
	order int
}

// seg is one drawn piece of an edge: it always joins two neighbouring
// ranks. head marks the piece that carries the arrowhead; tail marks a
// back edge, whose arrow points back up at the rank it came from.
type seg struct {
	from, to   int
	label      string
	head, tail bool
}

type layered struct {
	nodes []lnode
	segs  []seg
	ranks [][]int
}

// rankNodes assigns every node its rank and reports which edges point
// backwards. A back edge is one that closes a cycle; it is laid out in
// the forward direction and drawn with its arrow at the other end.
func rankNodes(d *Diagram) (ranks []int, back []bool, err error) {
	n := len(d.Nodes)
	out := make([][]int, n)
	for i, e := range d.Edges {
		if e.From == e.To {
			return nil, nil, errUnsupported // a self-loop needs an arc
		}
		out[e.From] = append(out[e.From], i)
	}
	back = make([]bool, len(d.Edges))
	const (
		white = iota
		grey
		black
	)
	state := make([]int, n)
	var visit func(int)
	visit = func(v int) {
		state[v] = grey
		for _, ei := range out[v] {
			switch w := d.Edges[ei].To; state[w] {
			case grey:
				back[ei] = true
			case white:
				visit(w)
			}
		}
		state[v] = black
	}
	for v := 0; v < n; v++ {
		if state[v] == white {
			visit(v)
		}
	}
	// Longest path over what is left, in a stable topological order.
	indeg := make([]int, n)
	for i, e := range d.Edges {
		if !back[i] {
			indeg[e.To]++
		}
	}
	ranks = make([]int, n)
	queue := make([]int, 0, n)
	for v := 0; v < n; v++ {
		if indeg[v] == 0 {
			queue = append(queue, v)
		}
	}
	seen := 0
	for len(queue) > 0 {
		v := queue[0]
		queue = queue[1:]
		seen++
		for _, ei := range out[v] {
			if back[ei] {
				continue
			}
			w := d.Edges[ei].To
			if ranks[v]+1 > ranks[w] {
				ranks[w] = ranks[v] + 1
			}
			if indeg[w]--; indeg[w] == 0 {
				queue = append(queue, w)
			}
		}
	}
	if seen != n {
		return nil, nil, errUnsupported // a cycle survived
	}
	return ranks, back, nil
}

// build turns a parsed graph into ranked nodes and drawable segments.
func build(d *Diagram) (*layered, error) {
	ranks, back, err := rankNodes(d)
	if err != nil {
		return nil, err
	}
	l := &layered{}
	for i, n := range d.Nodes {
		l.nodes = append(l.nodes, lnode{label: n.Label, rank: ranks[i], w: runeLen(n.Label) + 4, h: 3})
	}
	for i, e := range d.Edges {
		from, to := e.From, e.To
		tail := false
		if back[i] {
			from, to, tail = to, from, true
		}
		r1, r2 := l.nodes[from].rank, l.nodes[to].rank
		if r1 >= r2 {
			return nil, errUnsupported
		}
		// One segment per rank crossed, with an invisible node at each
		// rank in between so the line has somewhere to stand.
		prev := from
		for r := r1 + 1; r < r2; r++ {
			l.nodes = append(l.nodes, lnode{dummy: true, rank: r, w: 1, h: 3})
			cur := len(l.nodes) - 1
			l.segs = append(l.segs, seg{from: prev, to: cur, tail: tail && prev == from})
			prev = cur
		}
		l.segs = append(l.segs, seg{from: prev, to: to, label: e.Label, head: !tail, tail: tail && prev == from})
	}
	l.order()
	return l, nil
}

// order fills l.ranks and sorts each rank by the barycentre of the
// neighbours it is joined to, a few sweeps down and back up. It is the
// cheap crossing heuristic; it is not optimal and does not need to be.
func (l *layered) order() {
	top := 0
	for _, n := range l.nodes {
		if n.rank > top {
			top = n.rank
		}
	}
	l.ranks = make([][]int, top+1)
	for i, n := range l.nodes {
		l.ranks[n.rank] = append(l.ranks[n.rank], i)
	}
	up := make([][]int, len(l.nodes))
	down := make([][]int, len(l.nodes))
	for _, s := range l.segs {
		down[s.from] = append(down[s.from], s.to)
		up[s.to] = append(up[s.to], s.from)
	}
	pos := func() {
		for _, r := range l.ranks {
			for i, v := range r {
				l.nodes[v].order = i
			}
		}
	}
	pos()
	sweep := func(r []int, adj [][]int) {
		bary := make(map[int]float64, len(r))
		for _, v := range r {
			if len(adj[v]) == 0 {
				bary[v] = float64(l.nodes[v].order)
				continue
			}
			sum := 0
			for _, w := range adj[v] {
				sum += l.nodes[w].order
			}
			bary[v] = float64(sum) / float64(len(adj[v]))
		}
		sort.SliceStable(r, func(i, j int) bool { return bary[r[i]] < bary[r[j]] })
	}
	for pass := 0; pass < 4; pass++ {
		for r := 1; r < len(l.ranks); r++ {
			sweep(l.ranks[r], up)
			pos()
		}
		for r := len(l.ranks) - 2; r >= 0; r-- {
			sweep(l.ranks[r], down)
			pos()
		}
	}
}
