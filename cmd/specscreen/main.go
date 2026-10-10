// specscreen is the offline pre-screen for speculation during the user's
// think time (internal/agent/speculate.go): how often the prompt a user
// sent next is one a follow-up prediction could have matched. It reads
// session logs; it never calls a model.
//
//	specscreen ~/.local/share/janetcode/sessions
//
// Two numbers come out. The direct one compares each recorded follow-up
// suggestion (a followup/suggest event, logged since speculation was
// added) with the prompt that came after it. Sessions recorded before
// then have no suggestions, so the second number is a proxy on them: of
// the prompts a user typed after a reply, how many took up a next step
// the reply itself offered ("Want me to add tests?" followed by "yes" or
// "add the tests"). That is roughly the most a predictor reading the
// reply could match; a real predictor matching the exact text, which is
// what adoption needs, does worse.
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/session"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: specscreen <sessions dir>...")
		os.Exit(2)
	}
	var s screen
	for _, dir := range os.Args[1:] {
		files, _ := filepath.Glob(filepath.Join(dir, "*.jsonl"))
		sort.Strings(files)
		for _, f := range files {
			evs, err := read(f)
			if err != nil {
				fmt.Fprintf(os.Stderr, "%s: %v\n", f, err)
				continue
			}
			s.session(evs)
		}
	}
	fmt.Print(s.text())
}

// event is the part of a logged event the screen reads.
type event struct {
	T       string       `json:"t"`
	Surface bool         `json:"surface"`
	Output  string       `json:"output"`
	Message *llm.Message `json:"message"`
}

func read(path string) ([]event, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []event
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 64<<20)
	for sc.Scan() {
		var e event
		if json.Unmarshal(sc.Bytes(), &e) == nil && e.T != "" {
			out = append(out, e)
		}
	}
	return out, sc.Err()
}

type screen struct {
	sessions, pairs, harness int
	suggested, exact, near   int
	offered, tookOffer       int
	examples                 []string
}

// session walks one log: each human prompt after the first is paired with
// the reply before it and the suggestion logged after that reply.
func (s *screen) session(evs []event) {
	s.sessions++
	lastReply, suggestion := "", ""
	seenPrompt := false
	for _, e := range evs {
		switch e.T {
		case "assistant/message":
			if e.Message != nil {
				if t := strings.TrimSpace(replyText(*e.Message)); t != "" {
					lastReply = t
				}
			}
		case session.TypeFollowup:
			suggestion = e.Output
		case "user/message":
			if e.Message == nil || !e.Surface {
				continue
			}
			p := promptText(*e.Message)
			if harnessPrompt(p) {
				s.harness++
				continue
			}
			if seenPrompt && lastReply != "" {
				s.pair(lastReply, suggestion, p)
			}
			seenPrompt = true
			lastReply, suggestion = "", ""
		}
	}
}

func (s *screen) pair(reply, suggestion, prompt string) {
	s.pairs++
	if suggestion != "" {
		s.suggested++
		switch {
		case normal(suggestion) == normal(prompt):
			s.exact++
		case jaccard(words(suggestion), words(prompt)) >= 0.6:
			s.near++
		}
	}
	offer := lastOffer(reply)
	if offer != "" {
		s.offered++
		if os.Getenv("SPECSCREEN_DEBUG") != "" {
			fmt.Fprintf(os.Stderr, "offer %q -> %q\n", clip(offer, 80), clip(prompt, 60))
		}
	}
	if takesOffer(offer, prompt) {
		s.tookOffer++
		if len(s.examples) < 8 {
			s.examples = append(s.examples, fmt.Sprintf("%q -> %q", clip(offer, 70), clip(prompt, 50)))
		}
	}
}

func (s *screen) text() string {
	var b strings.Builder
	fmt.Fprintf(&b, "sessions %d, prompt pairs %d (harness-written prompts left out: %d)\n", s.sessions, s.pairs, s.harness)
	if s.suggested == 0 {
		b.WriteString("direct: no recorded follow-up suggestions to compare (the log records them from now on)\n")
	} else {
		fmt.Fprintf(&b, "direct: %d suggestions, exact match %d (%s), near %d (%s)\n", s.suggested, s.exact, pct(s.exact, s.suggested), s.near, pct(s.near, s.suggested))
	}
	fmt.Fprintf(&b, "proxy: reply offered a next step %d/%d (%s); user took the offered step %d/%d (%s)\n",
		s.offered, s.pairs, pct(s.offered, s.pairs), s.tookOffer, s.pairs, pct(s.tookOffer, s.pairs))
	for _, ex := range s.examples {
		b.WriteString("  " + ex + "\n")
	}
	return b.String()
}

func pct(n, d int) string {
	if d == 0 {
		return "n/a"
	}
	return fmt.Sprintf("%.0f%%", 100*float64(n)/float64(d))
}

func replyText(m llm.Message) string {
	var parts []string
	for _, b := range m.Blocks {
		if b.Type == llm.BlockText {
			parts = append(parts, b.Text)
		}
	}
	return strings.Join(parts, "\n")
}

// promptText is what the user typed: the first text block, without the
// repository map or memory the harness attached to it.
func promptText(m llm.Message) string {
	for _, b := range m.Blocks {
		if b.Type != llm.BlockText || b.Synthetic {
			continue
		}
		t := b.Text
		for _, cut := range []string{"[repository map", "<memory ", "<attached "} {
			if i := strings.Index(t, cut); i >= 0 {
				t = t[:i]
			}
		}
		return strings.TrimSpace(t)
	}
	return ""
}

// harnessPrompt is a user message the harness or a front-end wrote: a
// correction, a goal round, an undo report, a recipe or a slash command.
func harnessPrompt(p string) bool {
	if p == "" {
		return true
	}
	for _, pre := range []string{"[", "/", "!", "I undid", "Continue the goal", "Late answer", "<"} {
		if strings.HasPrefix(p, pre) {
			return true
		}
	}
	return false
}

var offerRe = regexp.MustCompile(`(?i)(want me to|shall i|should i|would you like( me)? to|i can also|if you(?:'d)? like,? i can|next step(?:s)?:|let me know if you(?:'d)? like( me)? to)\s*([^\n?.]*)`)

// lastOffer is the last next step the reply offered, or "".
func lastOffer(reply string) string {
	m := offerRe.FindAllStringSubmatch(reply, -1)
	if len(m) == 0 {
		return ""
	}
	last := m[len(m)-1]
	if o := strings.TrimSpace(last[len(last)-1]); o != "" {
		return o
	}
	return strings.TrimSpace(last[0])
}

var acceptRe = regexp.MustCompile(`(?i)^(yes|yeah|yep|sure|ok(ay)?|go ahead|do it|do that|let'?s do (it|that)|please( do)?|proceed|continue|sounds good|right)\b`)

// takesOffer reports whether prompt takes up offer: accepts it outright
// or asks for mostly the same thing.
func takesOffer(offer, prompt string) bool {
	if offer == "" {
		return false
	}
	if acceptRe.MatchString(strings.TrimSpace(prompt)) {
		return true
	}
	return overlap(words(offer), words(prompt)) >= 0.5
}

var stop = map[string]bool{"the": true, "a": true, "an": true, "to": true, "and": true, "of": true, "for": true, "in": true, "it": true, "this": true, "that": true, "me": true, "you": true, "i": true, "please": true, "can": true, "we": true, "on": true, "with": true, "is": true}

func words(s string) map[string]bool {
	out := map[string]bool{}
	for _, w := range strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '_')
	}) {
		if !stop[w] {
			out[w] = true
		}
	}
	return out
}

// overlap is the share of b's words found in a.
func overlap(a, b map[string]bool) float64 {
	if len(b) == 0 {
		return 0
	}
	n := 0
	for w := range b {
		if a[w] {
			n++
		}
	}
	return float64(n) / float64(len(b))
}

func jaccard(a, b map[string]bool) float64 {
	n := 0
	for w := range a {
		if b[w] {
			n++
		}
	}
	u := len(a) + len(b) - n
	if u == 0 {
		return 0
	}
	return float64(n) / float64(u)
}

func normal(s string) string {
	s = strings.ToLower(strings.Join(strings.Fields(s), " "))
	return strings.TrimRight(s, ".!; ")
}

func clip(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}
