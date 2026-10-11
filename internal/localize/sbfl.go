package localize

import (
	"math"
	"sort"
)

// Func is one function a coverage run can attribute lines to.
type Func struct {
	Path string // relative to the project root
	Name string // as the source spells it, with a receiver: (*T).M
	Line int    // first line, 1-based
	End  int    // last line
}

// Spectrum is one test run alone with coverage: whether it failed and
// which functions it executed.
type Spectrum struct {
	Test    Test
	Failed  bool
	Covered []Func
}

// Suspect is a function ranked by how strongly running it goes with
// failing: Ochiai's ef / sqrt(F * (ef + ep)), where ef and ep count the
// failing and passing tests that executed it and F every failing test.
// 1.0 is a function every failing test ran and no passing test did.
type Suspect struct {
	Func
	Score          float64
	Failed, Passed int // ef, ep
	// Edited says one of the turn's hunks touches the function.
	Edited bool
}

// ochiai ranks every function at least one failing test executed, most
// suspicious first. A tie goes to the function fewer passing tests ran,
// then to source order, so the ranking is stable across runs.
func ochiai(spectra []Spectrum) []Suspect {
	type counts struct {
		ef, ep int
	}
	by := map[Func]*counts{}
	var order []Func
	failing := 0
	for _, s := range spectra {
		if s.Failed {
			failing++
		}
		for _, f := range s.Covered {
			c := by[f]
			if c == nil {
				c = &counts{}
				by[f] = c
				order = append(order, f)
			}
			if s.Failed {
				c.ef++
			} else {
				c.ep++
			}
		}
	}
	if failing == 0 {
		return nil
	}
	var out []Suspect
	for _, f := range order {
		c := by[f]
		if c.ef == 0 {
			continue
		}
		score := float64(c.ef) / math.Sqrt(float64(failing)*float64(c.ef+c.ep))
		out = append(out, Suspect{Func: f, Score: score, Failed: c.ef, Passed: c.ep})
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Score != b.Score {
			return a.Score > b.Score
		}
		if a.Passed != b.Passed {
			return a.Passed < b.Passed
		}
		if a.Path != b.Path {
			return a.Path < b.Path
		}
		return a.Line < b.Line
	})
	return out
}
