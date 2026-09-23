// Package flavor supplies the whimsical texts shown on the status line
// while the model is thinking. The pool is Claude Code's present-participle
// verbs mixed with Qwen Code's sentence-style loading phrases. Tool, retry
// and wait phases are left alone; only the idle "thinking" wait is dressed up.
package flavor

import (
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// Interval is how long one phrase stays on screen before the next.
const Interval = 2500 * time.Millisecond

// verbs are seeded in the binary. They rotate; they are not generated.
var verbs = []string{
	"Accomplishing", "Actioning", "Actualizing", "Architecting",
	"Baking", "Beaming", "Beboppin'", "Befuddling", "Billowing",
	"Blanching", "Bloviating", "Boogieing", "Boondoggling", "Booping",
	"Bootstrapping", "Brewing", "Burrowing", "Calculating", "Canoodling",
	"Caramelizing", "Cascading", "Catapulting", "Cerebrating", "Choreographing",
	"Churning", "Coalescing", "Cogitating", "Combobulating", "Composing",
	"Computing", "Concocting", "Considering", "Contemplating", "Cooking",
	"Crafting", "Creating", "Crunching", "Crystallizing", "Cultivating",
	"Deciphering", "Deliberating", "Determining", "Dilly-dallying",
	"Discombobulating", "Doodling", "Drizzling", "Elucidating", "Embellishing",
	"Enchanting", "Envisioning", "Fermenting", "Fiddle-faddling", "Finagling",
	"Flambeing", "Flibbertigibbeting", "Flowing", "Flummoxing", "Fluttering",
	"Forging", "Frolicking", "Gallivanting", "Garnishing", "Generating",
	"Germinating", "Gitifying", "Grooving", "Harmonizing", "Hashing",
	"Hatching", "Herding", "Hullaballooing", "Hyperspacing", "Ideating",
	"Imagining", "Improvising", "Incubating", "Inferring", "Infusing",
	"Ionizing", "Janeting", "Jitterbugging", "Kneading", "Leavening",
	"Levitating", "Lollygagging", "Manifesting", "Marinating", "Meandering",
	"Metamorphosing", "Moonwalking", "Moseying", "Mulling", "Mustering",
	"Musing", "Nebulizing", "Nesting", "Noodling", "Orbiting", "Orchestrating",
	"Osmosing", "Perambulating", "Percolating", "Perusing", "Philosophising",
	"Photosynthesizing", "Pollinating", "Pondering", "Pontificating",
	"Pouncing", "Precipitating", "Prestidigitating", "Proofing", "Propagating",
	"Puttering", "Puzzling", "Quantumizing", "Razzle-dazzling", "Razzmatazzing",
	"Recombobulating", "Reticulating", "Roosting", "Ruminating", "Sauteing",
	"Scampering", "Schlepping", "Scurrying", "Seasoning", "Shenaniganing",
	"Shimmying", "Simmering", "Skedaddling", "Sketching", "Smooshing",
	"Sock-hopping", "Spelunking", "Sprouting", "Stewing", "Sublimating",
	"Swirling", "Swooping", "Synthesizing", "Tempering", "Thundering",
	"Tinkering", "Tomfoolering", "Topsy-turvying", "Transfiguring",
	"Transmuting", "Undulating", "Unfurling", "Unravelling", "Vibing",
	"Waddling", "Wandering", "Warping", "Whatchamacalliting", "Whirlpooling",
	"Whisking", "Wibbling", "Wrangling", "Zesting", "Zigzagging",
}

// pool is every phrase Phrase rotates through: the verbs only. The witty
// lines ("My other loading screen is even funnier.") narrate a loading
// screen at someone waiting on real work, and after a few minutes of a
// long turn they read as a toy; they stay Known so older recordings of
// the status line still parse, but they are no longer shown.
var pool = append([]string(nil), verbs...)

// Phrase picks from the pool.
// started seeds the first pick so consecutive turns do not all open on
// the same phrase; elapsed time walks the pool.
func Phrase(at, started time.Time) string {
	return dress(pick(pool, at, started))
}

// ForPhase leaves a specific wait (a tool, a retry, a compaction) as it
// is, and dresses only the idle "thinking" wait.
func ForPhase(phase string, at, started time.Time) string {
	if phase != "thinking" {
		return phase
	}
	return Phrase(at, started)
}

// known is every spelling of a seeded phrase that Known accepts: the
// phrase as written, the dressed form the status line shows, and the
// form with the trailing ellipsis taken off. The key is the phrase, the
// bound is the seeded pool (three entries per phrase, a few hundred
// phrases, fixed at build time), and there is no invalidation because
// the pool is a constant of the binary.
var known = sync.OnceValue(func() map[string]bool {
	m := make(map[string]bool, 3*(len(verbs)+len(witty)))
	for _, p := range append(append([]string(nil), verbs...), witty...) {
		p = strings.TrimSpace(p)
		m[p], m[dress(p)], m[trimEllipsis(p)] = true, true, true
	}
	return m
})

// Known reports whether s is one of the seeded verbs or witty lines,
// with or without the trailing ellipsis the status line adds.
func Known(s string) bool {
	s = strings.TrimSpace(s)
	set := known()
	return set[s] || set[trimEllipsis(s)]
}

// pick walks the list deterministically: where the turn started decides
// the phrase it opens on, and elapsed time walks on from there.
func pick(list []string, at, started time.Time) string {
	n := len(list)
	if n == 0 {
		return "Thinking"
	}
	if started.IsZero() {
		started = at
	}
	return list[(startOffset(started, n)+steps(at, started, n))%n]
}

// startOffset is the phrase a turn opens on, so that consecutive turns
// do not all open on the same one. The arithmetic stays in int64: a
// Unix time in milliseconds does not fit in a 32-bit int, and
// truncating it to one made every turn of a 32-bit build start from
// whatever the truncation happened to land on — in practice a negative
// number, clamped to the first phrase in the pool, every time.
func startOffset(started time.Time, n int) int {
	off := started.UnixMilli() % int64(n)
	if off < 0 {
		off += int64(n)
	}
	return int(off)
}

// steps is how far elapsed time has walked, reduced mod n so that a
// turn open for a very long time cannot overflow the sum.
func steps(at, started time.Time, n int) int {
	d := at.Sub(started)
	if d < 0 {
		return 0
	}
	return int(int64(d/Interval) % int64(n))
}

func dress(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return "Thinking…"
	}
	if strings.HasSuffix(s, "…") {
		return s
	}
	if strings.HasSuffix(s, "...") {
		return strings.TrimSuffix(s, "...") + "…"
	}
	r, _ := utf8.DecodeLastRuneInString(s)
	switch r {
	case '.', '!', '?', ')':
		return s
	}
	return s + "…"
}

func trimEllipsis(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimSuffix(s, "…")
	return strings.TrimSuffix(s, "...")
}
