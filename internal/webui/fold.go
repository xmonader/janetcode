package webui

import "fmt"

// fact is one transcript entry as far as activity folding cares.
type fact struct {
	Kind  string
	Name  string
	Error bool
}

// run is a fold of two or more tool entries inside one turn. Head is the
// index of the first tool; the rest follow it, with thinking and notices
// between them left in place. Live is the burst still being written.
type run struct {
	Head  int
	Count int
	Label string
	Live  bool
}

// foldActivity groups every tool between a user message and the next
// assistant (and again after that assistant, until the next user) into
// one run. A single tool is not a run. running marks the open tail live
// so the page keeps it expanded while the turn is in flight.
func foldActivity(facts []fact, running bool) []run {
	var out []run
	var idxs []int
	flush := func(live bool) {
		if len(idxs) >= 2 {
			out = append(out, run{
				Head:  idxs[0],
				Count: len(idxs),
				Label: activityLabel(facts, idxs),
				Live:  live,
			})
		}
		idxs = nil
	}
	for i, f := range facts {
		switch f.Kind {
		case "user", "assistant":
			flush(false)
		case "tool":
			idxs = append(idxs, i)
		}
	}
	flush(running)
	return out
}

func activityLabel(facts []fact, idxs []int) string {
	order := []string{"edit", "write", "read", "cmd", "tool"}
	n := map[string]int{}
	bad := map[string]int{}
	for _, i := range idxs {
		b := toolBucket(facts[i].Name)
		n[b]++
		if facts[i].Error {
			bad[b]++
		}
	}
	var parts []string
	for _, b := range order {
		if n[b] == 0 {
			continue
		}
		parts = append(parts, bucketPhrase(b, n[b], bad[b]))
	}
	if len(parts) == 0 {
		return ""
	}
	out := parts[0]
	for _, p := range parts[1:] {
		out += " · " + p
	}
	return out
}

func toolBucket(name string) string {
	switch name {
	case "edit_file", "edit":
		return "edit"
	case "write_file", "write":
		return "write"
	case "read_file", "read":
		return "read"
	case "bash", "shell", "run":
		return "cmd"
	default:
		return "tool"
	}
}

// stampRunsLocked writes run fields onto the thread. The caller holds mu.
// It returns copies of the entries whose fold fields changed, so the page
// hears about a burst growing or closing.
func (s *server) stampRunsLocked() []entry {
	facts := make([]fact, len(s.entries))
	for i, e := range s.entries {
		facts[i] = fact{Kind: e.Kind, Name: e.Name, Error: e.Error || e.Denied}
	}
	type prev struct {
		run, label string
		head, live bool
	}
	was := make([]prev, len(s.entries))
	for i, e := range s.entries {
		was[i] = prev{e.Run, e.RunLabel, e.RunHead, e.RunLive}
		e.Run, e.RunLabel = "", ""
		e.RunHead, e.RunLive = false, false
	}
	covered := map[int]run{}
	for _, r := range foldActivity(facts, s.running) {
		seen := 0
		for i := r.Head; i < len(facts) && seen < r.Count; i++ {
			if facts[i].Kind != "tool" {
				continue
			}
			covered[i] = r
			seen++
		}
	}
	for i, r := range covered {
		e := s.entries[i]
		e.Run = s.entries[r.Head].ID
		e.RunHead = i == r.Head
		e.RunLabel = r.Label
		e.RunLive = r.Live
	}
	var changed []entry
	for i, e := range s.entries {
		if was[i].run != e.Run || was[i].label != e.RunLabel || was[i].head != e.RunHead || was[i].live != e.RunLive {
			changed = append(changed, *e)
		}
	}
	return changed
}

func (s *server) touchRuns() {
	s.mu.Lock()
	changed := s.stampRunsLocked()
	s.mu.Unlock()
	for _, e := range changed {
		s.emitEntry(e)
	}
}

func bucketPhrase(bucket string, n, bad int) string {
	var s string
	switch bucket {
	case "edit":
		s = fmt.Sprintf("Made %d edit", n)
		if n != 1 {
			s += "s"
		}
	case "write":
		s = fmt.Sprintf("Wrote %d file", n)
		if n != 1 {
			s += "s"
		}
	case "read":
		s = fmt.Sprintf("Read %d file", n)
		if n != 1 {
			s += "s"
		}
	case "cmd":
		s = fmt.Sprintf("Ran %d command", n)
		if n != 1 {
			s += "s"
		}
	default:
		s = fmt.Sprintf("%d tool call", n)
		if n != 1 {
			s += "s"
		}
	}
	if bad > 0 {
		s += fmt.Sprintf(" (%d failed)", bad)
	}
	return s
}
