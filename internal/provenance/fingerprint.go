package provenance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/xmonader/janetcode/internal/llm"
)

// Check reads what a response says about itself, and a gateway that
// rewrites those fields — or a host that serves a quantised build under
// the full model's name — leaves it nothing to read. A behavioural
// fingerprint asks the model instead: a handful of cheap canary prompts
// with known answers, sent at the start of a session, whose replies are
// compared with the ones the same route gave the first time it was
// fingerprinted. A model that now names another vendor as its maker, or
// gets wrong what it used to get right, is a different model, whatever
// its name says.
//
// The comparison is deliberately coarse. A canary's reply is sampled,
// so a single changed answer is noise; only a change of self-named
// vendor, two lost answers, or most of the replies changing shape at
// once is reported. Logprobs would be a sharper instrument, and no
// provider here exposes them through llm.Provider.

// Canary is one fingerprint prompt.
type Canary struct {
	ID     string
	Prompt string
	// Answer is the expected reply, compared after normalisation; empty
	// for a canary whose reply is a feature rather than a test (the
	// self-named vendor).
	Answer string
}

// Canaries are the prompts a fingerprint is made of: one that asks the
// model who made it, and short known-answer items of the kinds a smaller
// or more heavily quantised model gets wrong first (multiplication,
// letter-level work).
var Canaries = []Canary{
	{ID: "maker", Prompt: "Which company trained you? Answer with the company name only."},
	{ID: "multiply", Prompt: "What is 37 * 43? Answer with the number only.", Answer: "1591"},
	{ID: "reverse", Prompt: "Write the word 'fingerprint' backwards. Answer with the result only.", Answer: "tnirpregnif"},
	{ID: "count", Prompt: "How many times does the letter r appear in the word 'strawberry'? Answer with the number only.", Answer: "3"},
	{ID: "capital", Prompt: "What is the capital of Australia? Answer with the city name only.", Answer: "canberra"},
	{ID: "sequence", Prompt: "What is the next number in 2, 6, 12, 20, 30? Answer with the number only.", Answer: "42"},
}

// canaryMaxTokens leaves a reasoning model room to think before its
// one-word answer; a model that does not think stops after a few.
const canaryMaxTokens = 2048

// Fingerprint is what one route answered to the canaries.
type Fingerprint struct {
	// Route is the configured provider and the model asked for.
	Route    string    `json:"route"`
	Recorded time.Time `json:"recorded"`
	// Maker is the vendor the model named as its maker, "" when it named
	// none janetcode knows.
	Maker string `json:"maker,omitempty"`
	// Correct and Terse are per canary: whether the answer was right, and
	// whether it came as asked (the bare answer, no preamble or
	// markdown) — a habit a model keeps, and another model does not share.
	Correct map[string]bool `json:"correct"`
	Terse   map[string]bool `json:"terse"`
	// Replies are the normalised replies, cut short, for a person
	// reading why a change was reported.
	Replies map[string]string `json:"replies"`
	// Served is the first canary response's own provenance.
	Served llm.Provenance `json:"served,omitempty"`
}

// Take sends the canaries to p for model, concurrently, and builds the
// fingerprint from the replies. A canary that fails is left out; Take
// fails only when every one did.
func Take(ctx context.Context, p llm.Provider, route, model string) (Fingerprint, error) {
	fp := Fingerprint{Route: route, Recorded: time.Now().UTC(), Correct: map[string]bool{}, Terse: map[string]bool{}, Replies: map[string]string{}}
	var (
		mu      sync.Mutex
		wg      sync.WaitGroup
		lastErr error
	)
	for _, c := range Canaries {
		wg.Add(1)
		go func(c Canary) {
			defer wg.Done()
			resp, err := llm.ForTaskOr(p).Stream(ctx, llm.Request{Model: model, MaxTokens: canaryMaxTokens,
				Messages: []llm.Message{llm.TextMessage(llm.RoleUser, c.Prompt)}}, nil)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				lastErr = err
				return
			}
			fp.observe(c, resp.Message.Text())
			if c.ID == Canaries[0].ID || fp.Served.Empty() {
				fp.Served = resp.Served
			}
		}(c)
	}
	wg.Wait()
	if len(fp.Replies) == 0 {
		if lastErr == nil {
			lastErr = errors.New("no canary answered")
		}
		return fp, fmt.Errorf("fingerprint: %w", lastErr)
	}
	return fp, nil
}

var (
	fpMarkdown = regexp.MustCompile("[*_`#]")
	fpSpaces   = regexp.MustCompile(`\s+`)
)

// observe records one canary's reply.
func (fp *Fingerprint) observe(c Canary, reply string) {
	norm := fpNormalise(reply)
	fp.Replies[c.ID] = norm
	if len(fp.Replies[c.ID]) > 60 {
		fp.Replies[c.ID] = norm[:60]
	}
	fp.Terse[c.ID] = len(strings.Fields(reply)) <= 3 && !fpMarkdown.MatchString(reply)
	if c.Answer == "" {
		fp.Maker = Vendor(norm)
		return
	}
	fp.Correct[c.ID] = norm == c.Answer || strings.Contains(" "+norm+" ", " "+c.Answer+" ")
}

// fpNormalise lowercases a reply, strips markdown and trailing
// punctuation, and collapses whitespace.
func fpNormalise(s string) string {
	s = strings.ToLower(fpMarkdown.ReplaceAllString(s, ""))
	s = fpSpaces.ReplaceAllString(strings.TrimSpace(s), " ")
	return strings.TrimRight(s, ".!")
}

// FingerprintVerdict is the outcome of comparing two fingerprints.
type FingerprintVerdict struct {
	// Changed is set when the evidence says another model is serving.
	Changed bool
	// Reasons say what changed, strongest first, whether or not it was
	// enough to set Changed.
	Reasons []string
}

// String is the one-line notice.
func (v FingerprintVerdict) String() string { return strings.Join(v.Reasons, "; ") }

// Compare judges now against the stored fingerprint of the same route.
// Only canaries both answered are compared. The thresholds are set so a
// sampled reply flipping on its own does not report a swap: the maker
// changing is enough alone, a lost answer needs a second one, and a
// change of reply shape needs most of the canaries.
func Compare(stored, now Fingerprint) FingerprintVerdict {
	var v FingerprintVerdict
	if stored.Maker != "" && now.Maker != "" && stored.Maker != now.Maker {
		v.Changed = true
		v.Reasons = append(v.Reasons, fmt.Sprintf("the model now says %s made it, where it said %s", now.Maker, stored.Maker))
	}
	var lost, gained, shape, both []string
	for _, id := range fpSortedKeys(now.Replies) {
		if _, ok := stored.Replies[id]; !ok {
			continue
		}
		both = append(both, id)
		if c, ok := stored.Correct[id]; ok {
			switch {
			case c && !now.Correct[id]:
				lost = append(lost, id)
			case !c && now.Correct[id]:
				gained = append(gained, id)
			}
		}
		if stored.Terse[id] != now.Terse[id] {
			shape = append(shape, id)
		}
	}
	if len(lost) >= 2 {
		v.Changed = true
	}
	if len(lost) > 0 {
		v.Reasons = append(v.Reasons, fmt.Sprintf("wrong now on %d canar%s it answered right before (%s)", len(lost), fpPlural(len(lost)), strings.Join(lost, ", ")))
	}
	if len(gained) >= 2 {
		// Better answers are a different model too, if a welcome one.
		v.Changed = true
		v.Reasons = append(v.Reasons, fmt.Sprintf("right now on %d canaries it answered wrong before (%s)", len(gained), strings.Join(gained, ", ")))
	}
	if len(both) >= 4 && len(shape)*2 > len(both) {
		v.Changed = true
		v.Reasons = append(v.Reasons, fmt.Sprintf("%d of %d replies changed shape (bare answer vs prose)", len(shape), len(both)))
	}
	return v
}

// CheckMaker is the check that needs nothing stored: a model that names
// a maker other than the requested model's vendor.
func CheckMaker(requested string, fp Fingerprint) (string, bool) {
	want := Vendor(requested)
	if want == "" || fp.Maker == "" || fp.Maker == want {
		return "", false
	}
	return fmt.Sprintf("asked who made it, %s answers %s", requested, fp.Maker), true
}

func fpPlural(n int) string {
	if n == 1 {
		return "y"
	}
	return "ies"
}

func fpSortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// FingerprintStore keeps one fingerprint per route in a directory.
type FingerprintStore struct {
	Dir string
}

var fpUnsafe = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

func (s FingerprintStore) path(route string) string {
	return filepath.Join(s.Dir, fpUnsafe.ReplaceAllString(route, "_")+".json")
}

// Load returns the stored fingerprint for route; ok is false when there is
// none.
func (s FingerprintStore) Load(route string) (Fingerprint, bool, error) {
	b, err := os.ReadFile(s.path(route))
	if errors.Is(err, os.ErrNotExist) {
		return Fingerprint{}, false, nil
	}
	if err != nil {
		return Fingerprint{}, false, err
	}
	var fp Fingerprint
	if err := json.Unmarshal(b, &fp); err != nil {
		return Fingerprint{}, false, fmt.Errorf("fingerprint %s: %w", s.path(route), err)
	}
	return fp, true, nil
}

// Save stores fp under its route.
func (s FingerprintStore) Save(fp Fingerprint) error {
	if err := os.MkdirAll(s.Dir, 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(fp, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path(fp.Route) + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path(fp.Route))
}

// Session is the session-start check: take a fingerprint, judge it
// against the maker the model name implies and the one stored for the
// route, and store it when there was none (trust on first use). A
// fingerprint that disagrees is not stored over the old one: the old one
// is the model the route used to serve, and one odd session must not
// teach the store that the swap is normal. Delete the file to accept a
// change. The returned text is the notice to show, "" when there is
// nothing to say.
func (s FingerprintStore) Session(ctx context.Context, p llm.Provider, route, model string) (notice string, changed bool, err error) {
	now, err := Take(ctx, p, route, model)
	if err != nil {
		return "", false, err
	}
	var reasons []string
	if why, bad := CheckMaker(model, now); bad {
		changed = true
		reasons = append(reasons, why)
	}
	stored, ok, err := s.Load(route)
	if err != nil {
		return "", changed, err
	}
	if !ok {
		if !changed {
			if err := s.Save(now); err != nil {
				return "", false, err
			}
		}
		if changed {
			return "fingerprint: " + strings.Join(reasons, "; "), true, nil
		}
		return fmt.Sprintf("fingerprint: recorded %s (%d canaries) as the reference for later sessions", route, len(now.Replies)), false, nil
	}
	v := Compare(stored, now)
	if v.Changed {
		changed = true
		reasons = append(reasons, v.Reasons...)
	}
	if !changed {
		return "", false, nil
	}
	return fmt.Sprintf("fingerprint: %s does not behave like the model fingerprinted on %s: %s; delete %s to accept the change",
		route, stored.Recorded.Format("2006-01-02"), strings.Join(reasons, "; "), s.path(route)), true, nil
}
