package localize

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

// errOutOfBudget stops a minimisation that ran out of time: what it had
// narrowed to so far still fails, it is just not known to be minimal.
var errOutOfBudget = errors.New("out of time")

// ddmin is Zeller's delta debugging over the indices in all: it returns a
// subset that still fails and from which no single chunk at the final
// granularity can be dropped (1-minimal when it ran to the end). fails
// is asked about subsets, never about all itself, which the caller has
// already seen fail. Each distinct subset is asked once.
//
// Failing is not monotone over hunks: a hunk that adds a call and the
// hunk that adds the function it calls do not build apart, and the caller
// reports such a subset as not failing (the test it is after did not
// run). ddmin then keeps both, which is the right answer.
//
// complete is false when fails returned an error (the budget ran out): the
// subset returned still fails, but a smaller one may exist.
func ddmin(all []int, fails func([]int) (bool, error)) (subset []int, complete bool, err error) {
	seen := map[string]bool{}
	ask := func(s []int) (bool, error) {
		k := key(s)
		if v, ok := seen[k]; ok {
			return v, nil
		}
		v, err := fails(s)
		if err != nil {
			return false, err
		}
		seen[k] = v
		return v, nil
	}
	c := append([]int(nil), all...)
	n := 2
	for len(c) >= 2 {
		chunks := split(c, n)
		reduced := false
		for _, ch := range chunks {
			f, err := ask(ch)
			if err != nil {
				return c, false, errOrBudget(err)
			}
			if f {
				c, n, reduced = ch, 2, true
				break
			}
		}
		// With two chunks each complement is the other chunk, already
		// asked above.
		if !reduced && n > 2 {
			for i := range chunks {
				co := complement(chunks, i)
				f, err := ask(co)
				if err != nil {
					return c, false, errOrBudget(err)
				}
				if f {
					c, n, reduced = co, max(n-1, 2), true
					break
				}
			}
		}
		if reduced {
			continue
		}
		if n >= len(c) {
			break
		}
		n = min(2*n, len(c))
	}
	return c, true, nil
}

func errOrBudget(err error) error {
	if errors.Is(err, errOutOfBudget) {
		return nil
	}
	return err
}

// split cuts c into n chunks of near-equal size, in order.
func split(c []int, n int) [][]int {
	var out [][]int
	start := 0
	for i := 0; i < n; i++ {
		end := start + (len(c)-start)/(n-i)
		if end > start {
			out = append(out, c[start:end])
		}
		start = end
	}
	return out
}

// complement is every chunk but the i-th, flattened.
func complement(chunks [][]int, i int) []int {
	var out []int
	for j, ch := range chunks {
		if j != i {
			out = append(out, ch...)
		}
	}
	return out
}

func key(s []int) string {
	c := append([]int(nil), s...)
	sort.Ints(c)
	parts := make([]string, len(c))
	for i, v := range c {
		parts[i] = fmt.Sprint(v)
	}
	return strings.Join(parts, ",")
}
