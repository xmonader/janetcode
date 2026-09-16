package memory

import (
	"math"
	"time"
)

// useHistoryCap is how many uses an entry keeps timestamps for. The sum
// in activation is over what is recorded, and twenty uses is far past
// the point where one more changes the ranking.
const useHistoryCap = 20

// minAgeDays floors a use's age so a use in this minute does not blow
// the power law up: t^(-1/2) at zero is not a number.
const minAgeDays = 0.01

// lambdaActivation scales activation against the lexical score; one
// point of activation is worth a third of a name-word hit.
const lambdaActivation = 1.0

// activation is the entry's ACT-R base-level activation: ln of the sum,
// over its recorded uses, of age^(-1/2) in days. Frequency and recency
// fall out of the one expression, and because Touch records a use,
// retrieval itself strengthens the entry -- a memory recalled is a
// memory remembered. An entry nobody has used is sampled at its last
// update, so a freshly written entry starts warm and cools off.
func activation(e Entry, now time.Time) float64 {
	uses := e.Used
	if len(uses) == 0 {
		switch t := e.Updated; {
		case !t.IsZero():
			uses = []time.Time{t}
		case !e.Created.IsZero():
			uses = []time.Time{e.Created}
		default:
			return 0
		}
	}
	sum := 0.0
	for _, t := range uses {
		age := now.Sub(t).Hours() / 24
		if age < minAgeDays {
			age = minAgeDays
		}
		sum += math.Pow(age, -0.5)
	}
	return math.Log(sum)
}

// typePrior is the ranking nudge an entry gets for being the user's own
// words: a preference or a correction outranks an automatically noted
// project fact at equal relevance and use.
func typePrior(e Entry) float64 {
	if e.Type == TypeUser || e.Type == TypeFeedback {
		return 1.0
	}
	return 0
}
