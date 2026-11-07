package agent

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// The wrap-up note (wrapup.go) is time-driven, and on effbench (Kimi,
// syn-kimi-pi2: janetcode 17/24, pi 23/24) it fired in three failing runs;
// each time the model stopped without checking a one-line rule of the brief
// that the hidden test then failed: "an argument whose value is None is
// treated as absent" (a-catalog-paging), "prev returns the latest datetime
// strictly earlier than before" (d-cronexpr), "a target is in the plan iff
// not up to date or any dep is in the plan" (a-build-incremental). So when
// the note fires it lists up to six rules of the brief (sentences with
// must, iff, strictly, None, a signature and the like) that no check of the
// turn mentions, and asks for each to be confirmed before finishing; with
// none found the note keeps its old text. JANETCODE_OFF=wraprules turns it
// off.

const maxWrapRules = 6

var ruleWords = regexp.MustCompile(`(?i)\b(must|iff|exactly|strictly|never|always|only|none|null|absent|signature|at most|at least|regardless|even if|raise|raises|error)\b`)

var (
	wrapTickRe   = regexp.MustCompile("`([^`\n]+)`")
	sigRe        = regexp.MustCompile("`[A-Za-z_][\\w.]*\\([^`\n]*\\)`")
	strongRuleRe = regexp.MustCompile(`(?i)\b(iff|strictly|absent|signature|exactly|even if|regardless|only if|treated as)\b`)
)

// briefRuleSentences: the sentences of the brief that state a rule, in order.
func briefRuleSentences(spec string) []string {
	var out []string
	for _, b := range briefBlocks(spec) {
		for _, s := range sentencesOf(b.text) {
			if !notARule(s) && (ruleWords.MatchString(s) || sigRe.MatchString(s)) {
				out = append(out, s)
			}
		}
	}
	return out
}

var genericTicks = map[string]bool{"none": true, "null": true, "nil": true, "true": true, "false": true, "self": true}

// ruleAnchors: what a check must mention for the rule to count as checked.
func ruleAnchors(s string, df map[string]int) []string {
	var out []string
	for _, m := range wrapTickRe.FindAllStringSubmatch(s, -1) {
		a := strings.TrimSpace(m[1])
		if i := strings.Index(a, "("); i > 0 {
			a = a[:i]
		}
		// A literal every check prints (None, true) says nothing about
		// which rule the check exercised.
		if len(a) >= 3 && !genericTicks[strings.ToLower(a)] {
			out = append(out, a)
		}
	}
	if len(out) > 0 {
		return out
	}
	var words []string
	seen := map[string]bool{}
	for _, w := range wordsOf(s) {
		l := strings.ToLower(w)
		if len(l) >= 4 && !stopWords[l] && !allDigits(l) && !seen[l] {
			seen[l] = true
			words = append(words, l)
		}
	}
	sort.SliceStable(words, func(i, j int) bool { return df[stemWord(words[i])] < df[stemWord(words[j])] })
	if len(words) > 2 {
		words = words[:2]
	}
	return words
}

// uncheckedBriefRules: the rule sentences none of whose anchors-set is
// missing from the checks, trimmed to max with the strong ones preferred.
func uncheckedBriefRules(spec string, checks []string, max int) []string {
	rules := briefRuleSentences(spec)
	if len(rules) == 0 {
		return nil
	}
	df := map[string]int{}
	for _, b := range briefBlocks(spec) {
		for _, s := range sentencesOf(b.text) {
			for t := range termsOf(s) {
				df[t]++
			}
		}
	}
	all := strings.Join(checks, "\n")
	lower := strings.ToLower(all)
	var un []string
	for _, r := range rules {
		anchors := ruleAnchors(r, df)
		checked := len(anchors) > 0
		for _, a := range anchors {
			if !strings.Contains(all, a) && !strings.Contains(lower, strings.ToLower(a)) {
				checked = false
				break
			}
		}
		if !checked {
			un = append(un, r)
		}
	}
	if len(un) <= max {
		return un
	}
	// The rules hidden tests probe are the strict ones (iff, strictly,
	// None is absent); a bare signature line or a sentence of the story
	// ranks below them.
	idx := make([]int, len(un))
	for i := range idx {
		idx[i] = i
	}
	sort.SliceStable(idx, func(i, j int) bool { return ruleScore(un[idx[i]]) > ruleScore(un[idx[j]]) })
	keep := map[int]bool{}
	for _, i := range idx[:max] {
		keep[i] = true
	}
	var out []string
	for i, r := range un {
		if keep[i] {
			out = append(out, r)
		}
	}
	return out
}

var storyRe = regexp.MustCompile(`(?i)^(today|currently|right now|a customer|users? (report|complain)|we |our )|\b(was|were|got|refused|disagree)\b`)

// ruleScore ranks a rule sentence: strict words count double, other rule
// words once, a signature once; a sentence telling the bug's story loses.
func ruleScore(s string) int {
	n := 2*len(strongRuleRe.FindAllString(s, -1)) + len(ruleWords.FindAllString(s, -1))
	if sigRe.MatchString(s) {
		n++
	}
	if storyRe.MatchString(s) {
		n -= 3
	}
	return n
}

// wrapRulesNote is the wrap-up note listing the unchecked rules, or "" when
// there are none.
func wrapRulesNote(by string, calls int, spec string, checks []string) string {
	rules := uncheckedBriefRules(spec, checks, maxWrapRules)
	if len(rules) == 0 {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "[harness] Your change passes `%s`, and you have spent %d calls checking it since it first passed. Before you finish, make sure each of these rules from the task holds; none of your checks exercised it:\n", by, calls)
	for i, r := range rules {
		fmt.Fprintf(&b, "%d. %s\n", i+1, cutRunes(r, 200))
	}
	b.WriteString("Check each by reading the code or one short inline assert, fix any that fails, then finish. Nothing else.")
	return b.String()
}
