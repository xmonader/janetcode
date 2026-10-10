// Package numclaim holds the numbers an answer states about a change —
// "40% faster", "allocations halved", "coverage 85%" — against the
// measurements the session actually ran. The verdict (internal/agent/
// verdict.go) already does this for "tests pass"; a number is the same
// kind of claim with more room to be invented, because nothing about a
// green test run says how fast the code got.
//
// A claim is backed when a command that measures its kind ran (a
// benchmark for speed, -benchmem or a profiler for memory, a coverage run
// for coverage, ls/du/stat for size) and the number can be found in what
// that command printed, or, for a change ("40% faster", "2x"), worked out
// from a run before the code changed and a run after it. A speed or memory
// change with no run on one side of the edits is unbacked however good the
// number looks: one run says what the code costs, not what it saved. A
// benchstat comparison whose every delta is "~" backs nothing: the
// difference is noise by its own test.
//
// The patterns are narrow on purpose. A number counts only with a unit
// that says what it measures (%, x, ns/op, ms, MB, allocs) and a word in
// the same sentence that says which kind; versions, line numbers, counts
// of tests and hedged estimates ("could be 2x faster", "aim for 80%") are
// left alone. A checker that flags every number says nothing.
package numclaim

import (
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
)

// Kind is what a number measures.
type Kind string

const (
	Speed    Kind = "speed"
	Memory   Kind = "memory"
	Coverage Kind = "coverage"
	Size     Kind = "size"
)

// what names the command a kind wants, for the notice.
func (k Kind) what() string {
	switch k {
	case Speed:
		return "benchmark or timing run"
	case Memory:
		return "allocation or memory measurement"
	case Coverage:
		return "coverage run"
	case Size:
		return "size measurement"
	}
	return "measurement"
}

// Claim is one number an answer states.
type Claim struct {
	Text  string  `json:"text"` // as written: "40% faster"
	Kind  Kind    `json:"kind"`
	Value float64 `json:"value"` // as written: 40 for "40%", 2 for "2x"; a level in the unit's base (ns, bytes)
	// Unit is the dimension: "%" and "x" for a change or a coverage
	// level, "time" (ns), "bytes", "allocs".
	Unit string `json:"unit"`
	// Relative is a change against the old code ("40% faster", "2x",
	// "halved") rather than a level ("120 ns/op", "coverage 85%").
	Relative bool `json:"relative"`
	// tol is how far a measured number may sit from Value and still be
	// the number written, given the digits it was written with.
	tol float64
}

// Step is one thing the session did, in order: a command and what it
// printed, or an edit to the code.
type Step struct {
	Cmd    string
	Output string
	Failed bool
	Edit   bool
}

// Status is what the check made of one claim.
type Status string

const (
	Backed         Status = "backed"
	Unmeasured     Status = "unmeasured"      // no command of the kind ran
	NoBaseline     Status = "no baseline"     // a change with no run on one side of the edits
	Stale          Status = "stale"           // every measurement predates the last edit
	NotFound       Status = "not found"       // measured, and the number is not in it
	NotSignificant Status = "not significant" // benchstat calls the difference noise
)

// Finding is one claim and what backs it, or why nothing does.
type Finding struct {
	Claim  Claim  `json:"claim"`
	Status Status `json:"status"`
	Why    string `json:"why"`
	Cmd    string `json:"cmd,omitempty"` // the measurement that backs it
}

// OK reports whether the claim is backed.
func (f Finding) OK() bool { return f.Status == Backed }

// ---- extraction ----

var (
	// numUnitRe is a number with a unit attached. The unit is what makes a
	// number a measurement rather than a version or a count.
	numUnitRe = regexp.MustCompile(`([+\-−]?)(\d{1,3}(?:,\d{3})+(?:\.\d+)?|\d+(?:\.\d+)?)\s?(%|x\b|×|ns/op|sec/op|ns\b|µs\b|μs\b|us\b|ms\b|s\b|secs?\b|seconds?\b|B/op|allocs/op|allocs?\b|allocations?\b|[KMG]i?B\b|kB\b|bytes?\b|B\b)`)
	// wordRe is a change written as a word.
	wordRe = regexp.MustCompile(`(?i)\b(halved|doubled|tripled|quadrupled|twice as (?:fast|quick)|half (?:the|as many)|cut in half)\b`)
	// hedgeRe marks a sentence about what could be, was asked for, or is
	// hoped for: not a statement of what was measured.
	hedgeRe = regexp.MustCompile(`(?i)\b(could|might|may|would|should|expect\w*|estimat\w*|target\w*|goal|aim\w*|up to|potentially|theoretical\w*|if|require\w*|must|needs? to|at least|threshold)\b`)
	// comparativeRe makes a percentage a change rather than a level.
	comparativeRe   = regexp.MustCompile(`(?i)\b(faster|slower|quicker|speed-?ups?|fewer|less|more|reduc\w*|improv\w*|increas\w*|decreas\w*|drop\w*|cut|sav\w*|shav\w*|smaller|larger|bigger|lower|higher|gain\w*|better|worse|regress\w*)\b`)
	coverageWordRe  = regexp.MustCompile(`(?i)\b(coverage|covered)\b`)
	memoryWordRe    = regexp.MustCompile(`(?i)(\balloc\w*|\bmemory\b|\bheap\b|\brss\b|b/op|\bgc\b|\bgarbage\b)`)
	speedWordRe     = regexp.MustCompile(`(?i)(\bfast(?:er)?\b|\bslower\b|\bquicker\b|\bspeed\w*|\blatency\b|\bthroughput\b|ns/op|sec/op|\bperf\w*|\bruntime\b|\brun ?time\b|\btakes\b|\btook\b|\btimings?\b|\bqps\b|ops/s|\bbenchmark\w*)`)
	sizeWordRe      = regexp.MustCompile(`(?i)\b(size|binary|bundle|smaller|larger|bigger|footprint|artifact)\b`)
	sentenceBreakRe = regexp.MustCompile(`([.!?;])\s+`)
)

// Extract finds the claims in an answer. Lines inside code fences are
// code or pasted output, not claims, and quoted lines are someone else's
// words.
func Extract(text string) []Claim {
	var out []Claim
	fence := false
	for _, line := range strings.Split(text, "\n") {
		trim := strings.TrimSpace(line)
		if strings.HasPrefix(trim, "```") {
			fence = !fence
			continue
		}
		if fence || strings.HasPrefix(trim, ">") {
			continue
		}
		for _, s := range sentences(trim) {
			out = append(out, fromSentence(s)...)
		}
	}
	return out
}

// sentences splits a line at sentence ends. A decimal point is no end:
// the break needs white space after it.
func sentences(line string) []string {
	var out []string
	last := 0
	for _, m := range sentenceBreakRe.FindAllStringIndex(line, -1) {
		out = append(out, line[last:m[0]+1])
		last = m[1]
	}
	return append(out, line[last:])
}

func fromSentence(s string) []Claim {
	if strings.TrimSpace(s) == "" || hedgeRe.MatchString(s) {
		return nil
	}
	var out []Claim
	for _, m := range numUnitRe.FindAllStringSubmatchIndex(s, -1) {
		sign, num, unit := s[m[2]:m[3]], s[m[4]:m[5]], s[m[6]:m[7]]
		if m[2] > 0 && isWordByte(s[m[2]-1]) {
			continue // part of a word or a longer token: v1.2s, abc5%
		}
		if m[1] < len(s) && s[m[1]] >= '0' && s[m[1]] <= '9' {
			continue // a product or a size: 8×366, 1080x720
		}
		if c, ok := claimOf(s, s[m[0]:m[1]], sign, num, unit); ok {
			out = append(out, c)
		}
	}
	for _, m := range wordRe.FindAllString(s, -1) {
		c := Claim{Text: m, Relative: true}
		switch w := strings.ToLower(m); {
		case strings.HasPrefix(w, "halved"), strings.HasPrefix(w, "half"), strings.HasPrefix(w, "cut in half"):
			c.Value, c.Unit, c.tol = 50, "%", 6
		case w == "doubled", strings.HasPrefix(w, "twice"):
			c.Value, c.Unit, c.tol = 2, "x", 0.25
		case w == "tripled":
			c.Value, c.Unit, c.tol = 3, "x", 0.3
		case w == "quadrupled":
			c.Value, c.Unit, c.tol = 4, "x", 0.4
		}
		if c.Kind = kindOf(s, c.Unit); c.Kind != "" {
			out = append(out, c)
		}
	}
	return out
}

func isWordByte(b byte) bool {
	return b == '_' || b == '.' || b >= '0' && b <= '9' || b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z'
}

// claimOf turns one number-with-unit in sentence s into a claim, or
// reports that it is none the checker can judge.
func claimOf(s, text, sign, num, unit string) (Claim, bool) {
	num = strings.ReplaceAll(num, ",", "")
	v, err := strconv.ParseFloat(num, 64)
	if err != nil {
		return Claim{}, false
	}
	digits := 0
	if i := strings.IndexByte(num, '.'); i >= 0 {
		digits = len(num) - i - 1
	}
	half := 0.5 * math.Pow(10, -float64(digits))
	c := Claim{Text: strings.TrimSpace(text), Value: v}
	switch u := strings.ToLower(unit); {
	case u == "%":
		c.Unit = "%"
		c.Relative = sign != "" || comparativeRe.MatchString(s) && !coverageWordRe.MatchString(s)
		c.tol = math.Max(half, 1)
		if c.Relative {
			// A rounded "40%" from a measured 37.8% is the same claim;
			// "40%" from 25% is not.
			c.tol = math.Max(c.tol, 0.08*v)
		}
	case u == "x" || u == "×":
		c.Unit, c.Relative = "x", true
		c.tol = math.Max(half, 0.08*v)
	case u == "allocs/op" || strings.HasPrefix(u, "alloc"):
		c.Unit = "allocs"
		c.tol = math.Max(half, 0.02*v)
	default:
		if scale, ok := timeScale(u); ok {
			c.Unit, c.Value = "time", v*scale
			c.tol = math.Max(half*scale, 0.02*c.Value)
		} else if scale, ok := byteScale(unit); ok {
			c.Unit, c.Value = "bytes", v*scale
			c.tol = math.Max(half*scale, 0.02*c.Value)
		} else {
			return Claim{}, false
		}
	}
	c.Kind = kindOf(s, c.Unit)
	if c.Kind == "" {
		return Claim{}, false
	}
	if c.Kind == Coverage && c.Unit != "%" {
		return Claim{}, false
	}
	return c, true
}

// kindOf decides what the sentence says the number measures. The unit
// settles it where it can (an allocation is memory, a duration is time);
// otherwise the sentence's words do, and a sentence with none says
// nothing the checker can hold against a measurement.
func kindOf(s, unit string) Kind {
	switch unit {
	case "allocs":
		return Memory
	case "time":
		// A duration alone is no claim about speed: "the cache keeps
		// entries for 30s", "125 tests pass (0.2s)". It is one beside a
		// word that says so: took, latency, faster, ns/op.
		if speedWordRe.MatchString(s) {
			return Speed
		}
		return ""
	case "bytes":
		if memoryWordRe.MatchString(s) {
			return Memory
		}
		if sizeWordRe.MatchString(s) {
			return Size
		}
		return ""
	}
	switch {
	case coverageWordRe.MatchString(s):
		return Coverage
	case memoryWordRe.MatchString(s):
		return Memory
	case speedWordRe.MatchString(s):
		return Speed
	case sizeWordRe.MatchString(s):
		return Size
	}
	return ""
}

func timeScale(u string) (float64, bool) {
	switch u {
	case "ns", "ns/op":
		return 1, true
	case "µs", "μs", "us":
		return 1e3, true
	case "ms":
		return 1e6, true
	case "s", "sec", "secs", "second", "seconds", "sec/op":
		return 1e9, true
	}
	return 0, false
}

func byteScale(u string) (float64, bool) {
	switch u {
	case "B", "B/op", "byte", "bytes":
		return 1, true
	case "kB", "KB", "KiB":
		return 1 << 10, true
	case "MB", "MiB":
		return 1 << 20, true
	case "GB", "GiB":
		return 1 << 30, true
	}
	return 0, false
}

// ---- measurements ----

var (
	speedCmdRe = regexp.MustCompile(`(?i)(-bench\b|-bench=|-test\.bench|\bhyperfine\b|\bbenchstat\b|(^|[;&|(]\s*)time\s|/usr/bin/time|--benchmark|\bcargo bench\b|\bbench(mark)?s?\b|\btimeit\b|\bperf stat\b|\bab -n|\bwrk\b|\bk6 run\b)`)
	speedOutRe = regexp.MustCompile(`(?m)(ns/op|sec/op|Time \(mean|^real\s+\d|\bops/s(ec)?\b|loops?, best of)`)
	memCmdRe   = regexp.MustCompile(`(?i)(-benchmem|memprofile|\bpprof\b|\bvalgrind\b|massif|heaptrack|/usr/bin/time -v|\bmemray\b|tracemalloc|memory_profiler)`)
	memOutRe   = regexp.MustCompile(`(allocs/op|B/op|Maximum resident set size|peak memory)`)
	covCmdRe   = regexp.MustCompile(`(?i)(-cover\b|-coverprofile|-covermode|go tool cover|--cov\b|--cov=|\bcoverage\b|\bnyc\b|\bc8\b|--coverage|tarpaulin|llvm-cov|\bgcov\b)`)
	covOutRe   = regexp.MustCompile(`(?im)(coverage: \d|^TOTAL\s.*\d%|All files\s*\|)`)
	sizeCmdRe  = regexp.MustCompile(`(?i)(\bls -[a-z]*l|\bdu\b|\bwc -c\b|\bstat\b|\bsize\b|go tool nm|\bbloaty\b)`)
	// compareCmdRe is one command that measures both sides itself: benchstat
	// over two result files, hyperfine over two commands.
	compareCmdRe = regexp.MustCompile(`(?i)\bbenchstat\s+\S+\s+\S+|\bhyperfine\b.*(\s'[^']+'|\s"[^"]+"){2,}`)
	// measureRe is a measured number with its unit, as tools print them.
	measureRe = regexp.MustCompile(`(\d+(?:\.\d+)?)\s?(ns/op|sec/op|B/op|allocs/op|ns|µs|μs|us|ms|s|[KMG]i?B|kB|B|%)(?:\s|$|[,)\]|])`)
	// realRe is the shell's time: real 0m1.234s.
	realRe = regexp.MustCompile(`(?m)^real\s+(\d+)m(\d+(?:\.\d+)?)s`)
	// rssRe is GNU time -v's peak memory, in kilobytes.
	rssRe = regexp.MustCompile(`Maximum resident set size \(kbytes\):\s*(\d+)`)
	// labelRe names the benchmark a result line belongs to.
	labelRe = regexp.MustCompile(`^\s*(Benchmark\S+?)(?:-\d+)?\s`)
	// benchDeltaRe is a benchstat delta: a percentage or "~".
	benchDeltaRe  = regexp.MustCompile(`(?m)\s([+-]\d+(?:\.\d+)?%)\s|\s~\s`)
	benchSignifRe = regexp.MustCompile(`\s[+-]\d+(?:\.\d+)?%\s`)
)

// measures reports which kinds a step measured.
func measures(st Step) map[Kind]bool {
	if st.Edit || st.Failed && !speedOutRe.MatchString(st.Output) && !memOutRe.MatchString(st.Output) {
		return nil
	}
	k := map[Kind]bool{}
	if speedCmdRe.MatchString(st.Cmd) || speedOutRe.MatchString(st.Output) {
		k[Speed] = true
	}
	if memCmdRe.MatchString(st.Cmd) || memOutRe.MatchString(st.Output) {
		k[Memory] = true
	}
	if covOutRe.MatchString(st.Output) || covCmdRe.MatchString(st.Cmd) && strings.Contains(st.Output, "%") {
		k[Coverage] = true
	}
	if sizeCmdRe.MatchString(st.Cmd) {
		k[Size] = true
	}
	return k
}

// reading is one number a measurement printed, in base units.
type reading struct {
	label string // the benchmark it belongs to, when the line names one
	unit  string // "time", "bytes", "allocs", "%"
	value float64
}

func readings(out string) []reading {
	var rs []reading
	for _, line := range strings.Split(out, "\n") {
		label := ""
		if m := labelRe.FindStringSubmatch(line); m != nil {
			label = m[1]
		}
		if m := realRe.FindStringSubmatch(line); m != nil {
			mins, _ := strconv.ParseFloat(m[1], 64)
			secs, _ := strconv.ParseFloat(m[2], 64)
			rs = append(rs, reading{label: "real", unit: "time", value: (mins*60 + secs) * 1e9})
			continue
		}
		if m := rssRe.FindStringSubmatch(line); m != nil {
			kb, _ := strconv.ParseFloat(m[1], 64)
			rs = append(rs, reading{label: "rss", unit: "bytes", value: kb * 1024})
			continue
		}
		for _, m := range measureRe.FindAllStringSubmatch(line+" ", -1) {
			v, err := strconv.ParseFloat(m[1], 64)
			if err != nil {
				continue
			}
			r := reading{label: label, value: v}
			switch u := m[2]; {
			case u == "%":
				r.unit = "%"
			case u == "allocs/op":
				r.unit = "allocs"
			default:
				if scale, ok := timeScale(u); ok {
					r.unit, r.value = "time", v*scale
				} else if scale, ok := byteScale(u); ok {
					r.unit, r.value = "bytes", v*scale
				} else {
					continue
				}
			}
			rs = append(rs, r)
		}
	}
	return rs
}

// unitsFor are the reading units a claim of this kind is judged on.
func unitsFor(c Claim) map[string]bool {
	switch {
	case !c.Relative:
		return map[string]bool{c.Unit: true}
	case c.Kind == Speed:
		return map[string]bool{"time": true}
	case c.Kind == Memory:
		return map[string]bool{"bytes": true, "allocs": true}
	case c.Kind == Size:
		return map[string]bool{"bytes": true}
	}
	return map[string]bool{"%": true}
}

// ---- checking ----

// Check holds each claim against the steps, oldest first.
func Check(claims []Claim, steps []Step) []Finding {
	lastEdit, firstEdit := -1, -1
	for i, st := range steps {
		if st.Edit {
			lastEdit = i
			if firstEdit < 0 {
				firstEdit = i
			}
		}
	}
	out := make([]Finding, 0, len(claims))
	for _, c := range claims {
		out = append(out, checkOne(c, steps, firstEdit, lastEdit))
	}
	return out
}

func checkOne(c Claim, steps []Step, firstEdit, lastEdit int) Finding {
	f := Finding{Claim: c}
	var before, after, any []int // indexes of the measurements of c's kind
	compare := -1                // a comparison run after the last edit
	for i, st := range steps {
		if !measures(st)[c.Kind] {
			continue
		}
		any = append(any, i)
		if i > lastEdit {
			after = append(after, i)
			if compareCmdRe.MatchString(st.Cmd) {
				compare = i
			}
		}
		if i < lastEdit {
			before = append(before, i)
		}
	}
	switch {
	case len(any) == 0:
		f.Status, f.Why = Unmeasured, "no "+c.Kind.what()+" in this session"
		return f
	case lastEdit >= 0 && len(after) == 0:
		f.Status, f.Why = Stale, "the code changed after the last "+c.Kind.what()
		return f
	}
	units := unitsFor(c)
	if !c.Relative {
		for _, i := range after {
			for _, r := range readings(steps[i].Output) {
				if units[r.unit] && math.Abs(r.value-c.Value) <= c.tol {
					return backed(f, steps[i].Cmd)
				}
			}
		}
		// A level read off a run before the change ("from 340 ns/op")
		// is the old code's number, and as good a fact.
		for _, i := range any {
			for _, r := range readings(steps[i].Output) {
				if units[r.unit] && math.Abs(r.value-c.Value) <= c.tol {
					return backed(f, steps[i].Cmd)
				}
			}
		}
		f.Status, f.Why = NotFound, fmt.Sprintf("%s ran, and printed no %s", c.Kind.what(), c.Text)
		return f
	}
	// A change: a comparison run states it, or a before run and an after
	// run do between them.
	if compare >= 0 {
		out := steps[compare].Output
		if strings.Contains(strings.ToLower(steps[compare].Cmd), "benchstat") && benchDeltaRe.MatchString(out) && !benchSignifRe.MatchString(out) {
			f.Status, f.Why, f.Cmd = NotSignificant, "benchstat found no significant difference (every delta is ~)", steps[compare].Cmd
			return f
		}
		if literal(c, out) {
			return backed(f, steps[compare].Cmd)
		}
	}
	for _, i := range after {
		if literal(c, steps[i].Output) && compareCmdRe.MatchString(steps[i].Cmd) {
			return backed(f, steps[i].Cmd)
		}
	}
	if len(before) == 0 && compare < 0 {
		f.Status, f.Why = NoBaseline, "no "+c.Kind.what()+" before the change to compare with"
		if lastEdit < 0 {
			f.Why = "nothing changed in this session, so there is no before and after"
		}
		return f
	}
	for _, bi := range before {
		for _, ai := range after {
			if derivable(c, readings(steps[bi].Output), readings(steps[ai].Output), units) {
				return backed(f, steps[ai].Cmd)
			}
		}
	}
	f.Status, f.Why = NotFound, fmt.Sprintf("the runs before and after the change do not give %s", c.Text)
	return f
}

func backed(f Finding, cmd string) Finding {
	f.Status, f.Cmd, f.Why = Backed, cmd, "measured by "+clip(cmd, 60)
	return f
}

// literal reports whether a comparison's own output states the change:
// benchstat's -40.12%, hyperfine's "1.95 ± 0.05 times faster".
func literal(c Claim, out string) bool {
	re := pctRe
	if c.Unit == "x" {
		re = timesRe
	}
	for _, m := range re.FindAllStringSubmatch(out, -1) {
		if v, err := strconv.ParseFloat(m[1], 64); err == nil && math.Abs(v-c.Value) <= c.tol {
			return true
		}
	}
	return false
}

var (
	pctRe   = regexp.MustCompile(`[+-]?(\d+(?:\.\d+)?)%`)
	timesRe = regexp.MustCompile(`(\d+(?:\.\d+)?)\s*(?:±\s*\d+(?:\.\d+)?\s*)?times`)
)

// derivable reports whether some pair of readings, one from before the
// change and one from after, of the same unit and the same benchmark,
// gives the claimed change under any of the ways people write one: 40%
// faster is (b-a)/b or (b-a)/a, 2x is b/a or a/b.
func derivable(c Claim, before, after []reading, units map[string]bool) bool {
	for _, b := range before {
		if !units[b.unit] || b.value <= 0 {
			continue
		}
		for _, a := range after {
			if a.unit != b.unit || a.value <= 0 || a.label != b.label {
				continue
			}
			var cands []float64
			if c.Unit == "x" {
				cands = []float64{b.value / a.value, a.value / b.value}
			} else {
				cands = []float64{
					100 * math.Abs(b.value-a.value) / b.value,
					100 * math.Abs(b.value-a.value) / a.value,
				}
			}
			for _, v := range cands {
				if math.Abs(v-c.Value) <= c.tol {
					return true
				}
			}
		}
	}
	return false
}

func clip(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}

// Summary is the notice line for the unbacked findings: each number and
// why nothing backs it. Empty when every claim is backed.
func Summary(fs []Finding) string {
	var parts []string
	for _, f := range fs {
		if !f.OK() {
			parts = append(parts, fmt.Sprintf("%q: %s", f.Claim.Text, f.Why))
		}
	}
	return strings.Join(parts, "; ")
}

// Unbacked counts the findings nothing backs.
func Unbacked(fs []Finding) int {
	n := 0
	for _, f := range fs {
		if !f.OK() {
			n++
		}
	}
	return n
}
