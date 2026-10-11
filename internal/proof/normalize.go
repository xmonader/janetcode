package proof

import (
	"regexp"
	"strings"
)

// A command's output is compared across runs by a hash of a normalised
// form, not of the bytes: the same suite run twice differs in timings,
// in the directory it ran in (the session's workspace against verify's
// throwaway checkout), in temp paths and in pointer values, and none of
// that is a different outcome. What normalisation keeps is everything
// else: test names, counts, messages, line numbers.

var (
	ansiRe = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]`)
	// durationRe: "0.012s", "1.5ms", "12µs", "3m2.1s", "(0.01 seconds)".
	durationRe = regexp.MustCompile(`\b\d+(?:\.\d+)?(?:ns|µs|us|ms|s|m|h)(?:\d+(?:\.\d+)?(?:ms|s))*\b|\b\d+(?:\.\d+)?\s*seconds?\b`)
	// timestampRe: ISO dates with an optional time, and clock times.
	timestampRe = regexp.MustCompile(`\b\d{4}-\d{2}-\d{2}(?:[T ]\d{2}:\d{2}(?::\d{2}(?:\.\d+)?)?(?:Z|[+-]\d{2}:?\d{2})?)?\b|\b\d{2}:\d{2}:\d{2}(?:\.\d+)?\b`)
	addrRe      = regexp.MustCompile(`\b0x[0-9a-fA-F]{6,}\b`)
	// tempRe: a path under a temp directory, whose random suffix differs
	// run to run.
	tempRe = regexp.MustCompile(`(?:/private)?(?:/tmp|/var/folders/[^\s/]+/[^\s/]+/T)/[^\s:'"]*`)
	// cachedRe: go test's "(cached)" where a fresh run prints its time:
	// it says where a result came from, not what it was.
	cachedRe = regexp.MustCompile(`\(cached\)`)
	spaceRe  = regexp.MustCompile(`[ \t]+`)
	// harnessLineRe: the lines the bash tool appends — "[exit code N]"
	// (the exit code is compared on its own) and its other bracketed
	// notes — and its stand-in for an empty output.
	harnessLineRe = regexp.MustCompile(`^\[(exit code -?\d+|command timed out|the command (exited|read))[^\]]*\].*$|^\(no output\)$`)
	// omittedRe: the marker a cut output carries (textfmt.TruncateTo, the
	// verify runner's capture).
	omittedRe = regexp.MustCompile(`\.\.\. \[\d+ bytes omitted`)
)

// NormalizeOutput is the form of out that OutputDigest hashes. root, when
// given, is replaced by "<root>" so the same command run in two checkouts
// prints the same thing.
func NormalizeOutput(out, root string) string {
	out = strings.ReplaceAll(out, "\r\n", "\n")
	out = ansiRe.ReplaceAllString(out, "")
	if root = strings.TrimRight(root, "/"); root != "" && root != "." {
		out = strings.ReplaceAll(out, root, "<root>")
	}
	out = tempRe.ReplaceAllString(out, "<tmp>")
	out = cachedRe.ReplaceAllString(out, "<dur>")
	out = timestampRe.ReplaceAllString(out, "<time>")
	out = durationRe.ReplaceAllString(out, "<dur>")
	out = addrRe.ReplaceAllString(out, "<addr>")
	var keep []string
	for _, l := range strings.Split(out, "\n") {
		l = strings.TrimSpace(spaceRe.ReplaceAllString(l, " "))
		if l == "" || harnessLineRe.MatchString(l) {
			continue
		}
		keep = append(keep, l)
	}
	return strings.Join(keep, "\n")
}

// OutputDigest is "sha256:<hex>" of NormalizeOutput(out, root), and
// whether out was cut (it carries a "bytes omitted" marker), in which case
// the digest covers only what was kept and a re-run's cannot be expected
// to match it.
func OutputDigest(out, root string) (digest string, partial bool) {
	return "sha256:" + sha256Hex([]byte(NormalizeOutput(out, root))), omittedRe.MatchString(out)
}
