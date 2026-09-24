package textfmt

import "strings"

// Nearest is the name in names closest to what was typed, when one is
// close enough to be worth suggesting, else "". A typo is the common
// case, so a small edit distance counts as well as a shared prefix.
func Nearest(typed string, names []string) string {
	best, bestScore := "", 0
	for _, n := range names {
		score := 0
		switch {
		case strings.HasPrefix(n, typed), strings.HasPrefix(typed, n):
			score = 4
		case strings.Contains(n, typed), strings.Contains(typed, n):
			score = 3
		}
		// Within two edits of a name is a typo, not a different word.
		if d := Distance(typed, n); d <= 2 && score < 4-d {
			score = 4 - d
		}
		if score > bestScore {
			best, bestScore = n, score
		}
	}
	return best
}

// Distance is the Levenshtein distance between two short words.
func Distance(a, b string) int {
	if a == b {
		return 0
	}
	prev := make([]int, len(b)+1)
	cur := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, min(cur[j-1]+1, prev[j-1]+cost))
		}
		prev, cur = cur, prev
	}
	return prev[len(b)]
}
