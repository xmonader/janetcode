// Package flavor supplies the whimsical texts shown on the status line
// while the model is thinking. The pool is Claude Code's present-participle
// verbs mixed with Qwen Code's sentence-style loading phrases. Tool, retry
// and wait phases are left alone; only the idle "thinking" wait is dressed up.
package flavor

import (
	"strings"
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

// Verb picks the verb for this moment of a turn. started seeds the
// first pick so consecutive turns do not all open on the same word;
// elapsed time walks the list.
func Verb(at, started time.Time) string {
	return pick(verbs, at, started)
}

// Phrase picks from the mixed pool: Claude verbs and Qwen loading lines.
func Phrase(at, started time.Time) string {
	n := len(verbs) + len(witty)
	if n == 0 {
		return dress("Thinking")
	}
	if started.IsZero() {
		started = at
	}
	off := int(started.UnixNano()/int64(time.Millisecond)) % n
	if off < 0 {
		off = 0
	}
	step := int(at.Sub(started) / Interval)
	if step < 0 {
		step = 0
	}
	i := (off + step) % n
	if i < len(verbs) {
		return dress(verbs[i])
	}
	return dress(witty[i-len(verbs)])
}

// ForPhase leaves a specific wait (a tool, a retry, a compaction) as it
// is, and dresses only the idle "thinking" wait.
func ForPhase(phase string, at, started time.Time) string {
	if phase != "thinking" {
		return phase
	}
	return Phrase(at, started)
}

// Known reports whether s is one of the seeded verbs or witty lines,
// with or without the trailing ellipsis the status line adds.
func Known(s string) bool {
	s = strings.TrimSpace(s)
	key := trimEllipsis(s)
	for _, v := range verbs {
		if v == key {
			return true
		}
	}
	for _, p := range witty {
		p = strings.TrimSpace(p)
		if s == p || s == dress(p) || key == trimEllipsis(p) {
			return true
		}
	}
	return false
}

func pick(list []string, at, started time.Time) string {
	n := len(list)
	if n == 0 {
		return "Thinking"
	}
	if started.IsZero() {
		started = at
	}
	off := int(started.UnixNano()/int64(time.Millisecond)) % n
	if off < 0 {
		off = 0
	}
	step := int(at.Sub(started) / Interval)
	if step < 0 {
		step = 0
	}
	return list[(off+step)%n]
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
