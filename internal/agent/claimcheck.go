package agent

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/shell"
)

// Claim-checked handoffs. The verdict (verdict.go) holds the model's last
// answer against what the turn's calls showed; this holds the text that
// crosses a context boundary to the same standard: the facts in the
// working-memory record written at compaction, a subagent's report to its
// parent, and the summary a spec review ends on. Those are the texts a
// later context trusts without the evidence behind them, so a false
// "tests pass" or "parse_row is defined in util.py" there costs more than
// anywhere else: under forced compaction pressure the run that read it
// has nothing left to check it against.
//
// Each claim the harness knows how to check is matched against the calls
// it watched and, for where-is-it claims, the file as it now is on disk.
// A claim with evidence keeps a pointer to it (the archive entry holding
// the call, which expand_output resolves); one with none is rewritten
// "unverified: …"; one the evidence contradicts is dropped. Claims of a
// shape the harness cannot check — judgements, plans, descriptions — pass
// through untouched: like claimRe, the patterns here are narrow on
// purpose, because a checker that marks every sentence unverified says
// nothing.
//
// It runs only where a boundary is crossed: at compaction, when a
// subagent returns and when a spec review ends, so a run that does none
// of those pays nothing. JANETCODE_OFF=claimcheck turns it off.

// claimKind is what the check made of one claim.
type claimKind int

const (
	claimUnchecked    claimKind = iota // nothing the harness can check
	claimBacked                        // a call or the file shows it
	claimUnbacked                      // checkable, and nothing shows it
	claimContradicted                  // the evidence says otherwise
)

// witness is one tool call the harness watched, reduced to what a claim
// can be checked against.
type witness struct {
	call   string // the harness's own id for the call
	name   string // the tool
	cmd    string // a bash call's command
	path   string // the file a read, write or edit named
	failed bool   // the call errored or the command exited non-zero
	// never is a command that did not start (exit 127, not found): no
	// verdict on the code either way.
	never bool
	// where is the archive entry that holds the call's output: its own,
	// when the output was long enough to be archived, or the archived
	// transcript the call was part of. Empty when neither exists.
	where string
	// own is true when where holds this call's output alone, so the
	// entry itself is the evidence and there is no call to look for in it.
	own bool
	// out is the call's result as the conversation had it, kept only
	// while the messages it came from are at hand (see carry).
	out string
}

// reArchiveRef finds the archive id a pruned placeholder names.
var reArchiveRef = regexp.MustCompile(`expand_output \{"id":"(ctx-\d+)"\}`)

// gatherProofs reduces the answered tool calls in msgs to proofs. where is
// the archive entry holding msgs as a whole, used for any call whose own
// output was not archived. A call the harness refused never ran and is
// left out.
func gatherProofs(msgs []llm.Message, where string) []witness {
	answered := answeredCalls(msgs)
	var out []witness
	for mi, m := range msgs {
		for bi, b := range m.Blocks {
			if b.Type != llm.BlockToolResult {
				continue
			}
			u, ok := answered[[2]int{mi, bi}]
			if !ok || refusedText(b.Text) {
				continue
			}
			p := witness{call: u.ID, name: u.Name, failed: b.IsError, where: where, out: b.Text}
			if id := archiveNoteID(b.Text); id != "" {
				p.where, p.own = id, true
			} else if m := reArchiveRef.FindStringSubmatch(b.Text); m != nil {
				p.where, p.own = m[1], true
			}
			switch u.Name {
			case "bash":
				p.cmd = strings.TrimSpace(shell.CommandOf(string(u.Input)))
				exit := ""
				if ms := reExitCode.FindAllStringSubmatch(b.Text, -1); ms != nil {
					exit = ms[len(ms)-1][1]
				} else if m := reExitArrow.FindStringSubmatch(b.Text); m != nil {
					exit = m[1] // a pruned placeholder keeps the exit this way
				}
				p.failed = p.failed || (exit != "" && exit != "0")
				p.never = exit == "127"
			default:
				p.path = argPathOf(string(u.Input))
				if editTool(u.Name) && !b.IsError {
					// What a write put in the file is evidence of what
					// the file holds, as much as a read of it is.
					var in struct {
						Content   string `json:"content"`
						NewString string `json:"new_string"`
					}
					json.Unmarshal(u.Input, &in)
					p.out += "\n" + in.Content + in.NewString
				}
			}
			out = append(out, p)
		}
	}
	return out
}

func refusedText(text string) bool {
	for _, p := range refusalPrefixes {
		if strings.HasPrefix(text, p) {
			return true
		}
	}
	return false
}

// claimChecker holds the evidence one handoff's claims are checked
// against: the calls, newest last, and the workspace the files live in
// ("" when there is none to look at, as in an offline replay).
type claimChecker struct {
	proofs []witness
	root   string
}

// Patterns for the claims the checker knows. Each is a shape a fact in a
// handoff takes when it states something a call could have shown.
var (
	// passRe is a present-tense claim that checks are green. Past tense
	// ("passed before the refactor") is history, not a claim about now.
	passRe = regexp.MustCompile(`(?i)\b(tests? (?:now |currently |still |all )?(?:pass|passes|passing|are green|succeed)|all (?:\d+ )?tests? (?:now |currently |still )?pass|build (?:succeeds|passes|is green|works)|checks? pass)\b`)
	// ranClaimRe is a claim that a check was run, with no state attached.
	ranClaimRe = regexp.MustCompile(`(?i)\b(verified|I (?:ran|have run) the tests?)\b`)
	// failNowRe is a present-tense claim that a command fails.
	failNowRe = regexp.MustCompile(`(?i)\b(fails|is failing|is red|exits? (?:with )?(?:code |status )?[1-9]\d*)\b`)
	// backtickRe finds `spans`.
	backtickRe = regexp.MustCompile("`([^`\n]+)`")
	// definedInRe: "<name> is defined in <path>" and its variants.
	definedInRe = regexp.MustCompile(`(?i)` + "`?" + `([A-Za-z_][\w.]*)(?:\(\))?` + "`?" + `\s+(?:is|are)\s+(?:defined|declared|implemented)\s+(?:in|at)\s+` + "`?" + `([\w./-]+\.[A-Za-z]{1,6})\b`)
	// definesRe: "<path> defines <name>".
	definesRe = regexp.MustCompile(`(?i)` + "`?" + `([\w./-]+\.[A-Za-z]{1,6})` + "`?" + `\s+(?:defines|declares)\s+` + "`?" + `([A-Za-z_][\w.]*)`)
	// claimPathRe is a file path: something with a directory, or a bare name
	// with a source or config extension (so fmt.Println is not a path).
	claimPathRe = regexp.MustCompile(`(?:[\w.-]+/)+[\w.-]+\.[A-Za-z]{1,6}\b|\b[\w-]+\.(?:go|py|js|mjs|ts|tsx|jsx|rs|java|kt|c|h|cc|cpp|hpp|rb|php|cs|swift|md|json|yaml|yml|toml|txt|sh|mod|cfg|ini|html|css|sql|lua|zig|ex|exs)\b`)
)

// check classifies one claim and, when it is backed, says where the
// evidence is.
func (c *claimChecker) check(claim string) (claimKind, string) {
	if k, ref := c.checkCommand(claim); k != claimUnchecked {
		return k, ref
	}
	if k, ref := c.checkDefinition(claim); k != claimUnchecked {
		return k, ref
	}
	if k, ref := c.checkState(claim); k != claimUnchecked {
		return k, ref
	}
	return c.checkPaths(claim)
}

// checkCommand handles a claim naming a command in backticks: "reproduced
// with `go test -run TestParse ./ledger`", "`make lint` fails". The
// command must have run; a pass or fail it asserts in the present tense
// must agree with how its last run exited.
func (c *claimChecker) checkCommand(claim string) (claimKind, string) {
	for _, m := range backtickRe.FindAllStringSubmatch(claim, -1) {
		span := strings.TrimSpace(m[1])
		if !strings.Contains(span, " ") {
			continue // an identifier or a path, not a command
		}
		var last *witness
		for i := range c.proofs {
			p := &c.proofs[i]
			if p.name == "bash" && p.cmd != "" && !p.never && (p.cmd == span || strings.Contains(p.cmd, span)) {
				last = p
			}
		}
		if last == nil {
			if shell.IsCheck(span) {
				return claimUnbacked, ""
			}
			continue
		}
		switch {
		case passRe.MatchString(claim) && last.failed, failNowRe.MatchString(claim) && !last.failed:
			return claimContradicted, last.ref()
		}
		return claimBacked, last.ref()
	}
	return claimUnchecked, ""
}

// checkDefinition handles "<name> is defined in <path>" and "<path>
// defines <name>": the file must exist and hold the name. With a
// workspace that is read from disk as it is now, so a claim a later edit
// made false is caught; without one, the outputs of the calls that read
// or wrote the file are the evidence.
func (c *claimChecker) checkDefinition(claim string) (claimKind, string) {
	var name, path string
	subject := false // the name opens the claim, as its subject
	if m := definedInRe.FindStringSubmatchIndex(claim); m != nil {
		name, path = claim[m[2]:m[3]], claim[m[4]:m[5]]
		subject = strings.TrimLeft(claim[:m[2]], "`") == ""
	} else if m := definesRe.FindStringSubmatch(claim); m != nil {
		path, name = m[1], m[2]
	} else {
		return claimUnchecked, ""
	}
	if !identLike(name, subject, strings.Contains(claim, "`"+name)) {
		return claimUnchecked, ""
	}
	if i := strings.LastIndex(name, "."); i >= 0 {
		name = name[i+1:] // Class.method: the method is what the file holds
	}
	if name == "" {
		return claimUnchecked, ""
	}
	nameRe := regexp.MustCompile(`\b` + regexp.QuoteMeta(name) + `\b`)
	touched := c.touching(path)
	if c.root != "" {
		data, full, ok := c.readFile(path, touched)
		if !ok {
			if touched == nil {
				// No call ever opened it: the claim may abbreviate a
				// path the harness cannot resolve, so not found is
				// not proof of absence. A file a call did open and
				// that is gone now is.
				return claimUnbacked, ""
			}
			return claimContradicted, ""
		}
		if !nameRe.Match(data) {
			return claimContradicted, ""
		}
		if touched != nil {
			return claimBacked, touched.ref()
		}
		return claimBacked, "checked in " + full
	}
	for i := len(c.proofs) - 1; i >= 0; i-- {
		p := &c.proofs[i]
		if samePath(p.path, path) && !p.failed && nameRe.MatchString(p.out) {
			return claimBacked, p.ref()
		}
	}
	return claimUnbacked, ""
}

// identLike reports whether the word a definition claim names is a code
// name rather than English: backticked, shaped like an identifier (an
// underscore, a dot, a capital), or the subject the claim opens with
// ("main is defined in cmd/app.go"). "The REQUIRED tuple is defined in
// parse.py" names "tuple", and a file not holding the word "tuple" says
// nothing about the claim.
func identLike(name string, subject, quoted bool) bool {
	if quoted || strings.ContainsAny(name, "_.") {
		return true
	}
	switch strings.ToLower(name) {
	case "it", "this", "that", "which", "what", "each", "everything", "something", "nothing", "all", "both", "one", "logic", "code":
		return false // a pronoun opening the sentence
	}
	return subject || strings.ToLower(name) != name
}

// checkState handles a claim about whether the checks are green that
// names no command: it is judged by the last check that ran. A green
// claim against a red last run is contradicted; against a tree edited
// since the last run, or with no run at all, it is unverified.
func (c *claimChecker) checkState(claim string) (claimKind, string) {
	pass, ran := passRe.MatchString(claim), ranClaimRe.MatchString(claim)
	if !pass && !ran {
		return claimUnchecked, ""
	}
	last, editedAfter := -1, false
	for i, p := range c.proofs {
		switch {
		case p.name == "bash" && !p.never && shell.IsCheck(p.cmd):
			last, editedAfter = i, false
		case editTool(p.name) && !p.failed && last >= 0:
			editedAfter = true
		}
	}
	if last < 0 {
		return claimUnbacked, ""
	}
	p := &c.proofs[last]
	switch {
	case pass && p.failed:
		return claimContradicted, p.ref()
	case pass && editedAfter:
		return claimUnbacked, ""
	}
	return claimBacked, p.ref()
}

// checkPaths handles any other claim that names files: a file a call
// read or changed backs it; a file that is neither on disk nor named by
// any call leaves it unverified. A claim naming a file that exists but
// was never opened is left alone — it may well be right, and the
// harness has nothing to point at.
func (c *claimChecker) checkPaths(claim string) (claimKind, string) {
	paths := claimPathRe.FindAllString(claim, -1)
	if len(paths) == 0 {
		return claimUnchecked, ""
	}
	missing := 0
	for _, path := range paths {
		if p := c.touching(path); p != nil {
			return claimBacked, p.ref()
		}
		if c.root != "" {
			if _, _, ok := c.readFile(path, nil); ok {
				continue
			}
		} else if c.mentioned(path) {
			continue
		}
		missing++
	}
	if missing == len(paths) {
		return claimUnbacked, ""
	}
	return claimUnchecked, ""
}

// touching is the newest call that read or changed path without failing.
func (c *claimChecker) touching(path string) *witness {
	for i := len(c.proofs) - 1; i >= 0; i-- {
		p := &c.proofs[i]
		if p.path != "" && !p.failed && samePath(p.path, path) {
			return p
		}
	}
	return nil
}

// mentioned reports whether any call named path in its command or
// output: offline, the closest thing to the file being there.
func (c *claimChecker) mentioned(path string) bool {
	for _, p := range c.proofs {
		if strings.Contains(p.cmd, path) || strings.Contains(p.out, path) || samePath(p.path, path) {
			return true
		}
	}
	return false
}

// readFile reads path under the workspace. A bare or partial path the
// claim abbreviated ("parse.py" for "ledger/parse.py") is found through
// the call that touched it.
func (c *claimChecker) readFile(path string, touched *witness) ([]byte, string, bool) {
	cands := []string{path}
	if touched != nil && touched.path != path {
		cands = append(cands, touched.path)
	}
	for _, p := range cands {
		full := p
		if !filepath.IsAbs(full) {
			full = filepath.Join(c.root, full)
		}
		if data, err := os.ReadFile(full); err == nil {
			return data, p, true
		}
	}
	return nil, "", false
}

// samePath reports whether a call's path and a claim's name the same
// file, the claim being allowed to drop leading directories.
func samePath(callPath, claimPath string) bool {
	if callPath == "" || claimPath == "" {
		return false
	}
	a, b := filepath.ToSlash(filepath.Clean(callPath)), filepath.ToSlash(filepath.Clean(claimPath))
	return a == b || strings.HasSuffix(a, "/"+b) || strings.HasSuffix(b, "/"+a)
}

// ref is where a backed claim's evidence is, in the form the tag shows.
func (p *witness) ref() string {
	switch {
	case p.where != "" && (p.own || p.call == ""):
		return p.where
	case p.where != "":
		return p.where + ", call " + p.call
	}
	return "call " + p.call
}

// Tags a checked claim carries. They are stripped before a claim is
// checked again, so a record that goes through several compactions does
// not pile them up, and a claim backed once is re-judged against the
// newer evidence rather than trusted for having been backed.
const unverifiedTag = "unverified: "

var evidenceTagRe = regexp.MustCompile(`\s*\((?:evidence: [^()]*|checked in [^()]*)\)$`)

func untag(claim string) string {
	claim = strings.TrimSpace(claim)
	claim = strings.TrimPrefix(claim, unverifiedTag)
	return evidenceTagRe.ReplaceAllString(claim, "")
}

// claimTally counts what a pass over a handoff did.
type claimTally struct {
	Backed, Unbacked, Dropped int
	dropped                   []string // the claims removed, for the notice
}

func (t claimTally) changed() bool { return t.Unbacked+t.Dropped > 0 }

// String is the notice line: what was dropped and marked.
func (t claimTally) String() string {
	var parts []string
	if t.Dropped > 0 {
		parts = append(parts, fmt.Sprintf("dropped %d contradicted (%s)", t.Dropped, strings.Join(clipAll(t.dropped, 60), "; ")))
	}
	if t.Unbacked > 0 {
		parts = append(parts, fmt.Sprintf("marked %d unverified", t.Unbacked))
	}
	if t.Backed > 0 {
		parts = append(parts, fmt.Sprintf("%d backed by a call", t.Backed))
	}
	return strings.Join(parts, ", ")
}

func clipAll(items []string, n int) []string {
	out := make([]string, len(items))
	for i, s := range items {
		out[i] = prefix(s, n)
	}
	return out
}

// apply checks each claim and returns the list to hand over: backed ones
// tagged with their evidence, unbacked ones marked, contradicted ones
// gone.
func (c *claimChecker) apply(claims []string, t *claimTally) []string {
	var out []string
	for _, raw := range claims {
		claim := untag(raw)
		if claim == "" {
			continue
		}
		switch k, ref := c.check(claim); k {
		case claimBacked:
			t.Backed++
			out = append(out, fmt.Sprintf("%s (%s)", claim, tagOf(ref)))
		case claimUnbacked:
			t.Unbacked++
			out = append(out, unverifiedTag+claim)
		case claimContradicted:
			t.Dropped++
			t.dropped = append(t.dropped, claim)
		default:
			out = append(out, claim)
		}
	}
	return out
}

func tagOf(ref string) string {
	if strings.HasPrefix(ref, "checked in ") {
		return ref
	}
	return "evidence: " + ref
}

// claimLegend tells the next context what the tags mean. It is added to
// a handoff only when the handoff carries a tag.
const claimLegend = `Claims were checked against the calls the harness watched: one ending "(evidence: ctx-N, call X)" is backed by that call — expand_output {"id":"ctx-N","grep":"X"} finds it, and "(evidence: ctx-N)" by that whole entry; "(checked in <file>)" means the file holds it now; "unverified:" means no call showed it, so check it before relying on it.`

// tagged reports whether any of items carries a tag from apply.
func tagged(items []string) bool {
	for _, s := range items {
		if strings.HasPrefix(s, unverifiedTag) || evidenceTagRe.MatchString(s) {
			return true
		}
	}
	return false
}

// maxProofs bounds the evidence a working-memory record carries from one
// compaction to the next.
const maxProofs = 400

// carry adds the proofs of a compacted head to the record's evidence,
// without their outputs: the head is gone from the conversation once the
// compaction is done, and what a later check needs of it is the call,
// how it ended and where its archive entry is.
func (w *WorkingMemory) carry(ps []witness) {
	have := map[string]bool{}
	for _, p := range w.proofs {
		have[p.call] = true
	}
	for _, p := range ps {
		if p.call != "" && have[p.call] {
			continue
		}
		p.out = ""
		w.proofs = append(w.proofs, p)
	}
	if len(w.proofs) > maxProofs {
		w.proofs = w.proofs[len(w.proofs)-maxProofs:]
	}
}

// checkClaims checks the record's facts against everything compacted so
// far plus the head being compacted now, whose outputs are still at hand.
// where is the archive entry the head was written to.
func (w *WorkingMemory) checkClaims(head []llm.Message, where, root string) claimTally {
	fresh := gatherProofs(head, where)
	c := &claimChecker{proofs: append(append([]witness(nil), w.proofs...), fresh...), root: root}
	var t claimTally
	w.Facts = c.apply(w.Facts, &t)
	w.carry(fresh)
	return t
}

// checkReport checks a free-text report line by line — a subagent's
// answer, the summary a review ends on — and returns it with backed lines
// tagged, unbacked lines marked and contradicted lines removed. Lines
// inside code fences are code, not claims, and are left alone.
func (c *claimChecker) checkReport(report string) (string, claimTally) {
	var t claimTally
	var out []string
	fence := false
	for _, line := range strings.Split(report, "\n") {
		trim := strings.TrimSpace(line)
		if strings.HasPrefix(trim, "```") {
			fence = !fence
		}
		if fence || trim == "" || strings.HasPrefix(trim, "```") || strings.HasPrefix(trim, "#") {
			out = append(out, line)
			continue
		}
		lead := line[:len(line)-len(strings.TrimLeft(line, " \t"))]
		body := strings.TrimLeft(line, " \t")
		bullet := ""
		if m := bulletRe.FindString(body); m != "" {
			bullet, body = m, body[len(m):]
		}
		kept := c.apply([]string{body}, &t)
		if len(kept) == 0 {
			continue
		}
		out = append(out, lead+bullet+kept[0])
	}
	return strings.Join(out, "\n"), t
}

var bulletRe = regexp.MustCompile(`^(?:[-*+]|\d+[.)])\s+`)

// checkSubagentReport holds a subagent's report to its own calls before
// the parent reads it. The parent never saw those calls, so a pointer to
// them is only useful once they are somewhere it can read: the child's
// transcript is archived, and only when the report makes a claim worth
// pointing at. A report with nothing checkable is returned as it was and
// costs no archive entry.
func (a *Agent) checkSubagentReport(report, id string, child *Agent) string {
	if a.Off["claimcheck"] || child == nil {
		return report
	}
	c := &claimChecker{proofs: gatherProofs(child.Messages, ""), root: child.RootDir()}
	if _, t := c.checkReport(report); t.Backed+t.Unbacked+t.Dropped == 0 {
		return report
	}
	if a.Archive != nil {
		if where, err := a.Archive.PutSubject(fmt.Sprintf("subagent-%s-%d", id, len(child.Messages)), "subagent transcript", id, renderMessages(child.Messages)); err == nil {
			c.proofs = gatherProofs(child.Messages, where)
		}
	}
	out, t := c.checkReport(report)
	if t.Dropped > 0 {
		out += fmt.Sprintf("\n\n[harness: removed %d line(s) of this report that the subagent's own calls contradict]", t.Dropped)
	}
	if t.Backed+t.Unbacked > 0 {
		out += "\n\n[harness: " + claimLegend + "]"
	}
	return out
}

// noteReviewClaims checks the summary a spec review ends on against the
// turn's calls. The summary is the model's answer to the user, so it is
// not rewritten; what it claims that no call showed, or that a call
// contradicts, is reported beside it, the way the verdict is.
func (a *Agent) noteReviewClaims(text string, emit func(Event)) {
	if a.Off["claimcheck"] || strings.TrimSpace(text) == "" {
		return
	}
	c := &claimChecker{proofs: gatherProofs(a.Messages, ""), root: a.RootDir()}
	if _, t := c.checkReport(text); t.changed() {
		emit(Event{Kind: EvNotice, Text: "the review's summary: " + t.String()})
	}
}
