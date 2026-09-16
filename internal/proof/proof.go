// Package proof attaches machine-readable evidence to a git commit: which
// session, turn and stated intent wrote each changed file, and what the
// harness actually observed demonstrating the turn's work (see
// internal/agent's Change and TurnOutcome). It is stored as a git note
// under NotesRef, so it travels with the commit rather than the session
// file, and survives long after the session that made it is gone.
//
// This package does not import internal/agent: agent already needs to call
// into proof (to attach evidence after a commit, and to serve /proof), so
// the plain ChangeInput and TurnOutcomeInput types below let the caller
// hand over the facts without a dependency cycle.
package proof

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/xmonader/janetcode/internal/diff"
	"github.com/xmonader/janetcode/internal/gitx"
)

// NotesRef is where proof records are stored, as git notes.
const NotesRef = "refs/notes/janetcode"

// Version is the current Record schema version, so a future format change
// can tell an old record apart from a new one.
const Version = 1

// ChangeRef is one file change contributed to a Record. Session names the
// session that made it; it is not part of what the harness hands over for
// a single build (a session only ever reports its own work) but is filled
// in when two sessions' records are merged onto the same commit, so that
// Seq — unique only within one session — cannot collide with another
// session's numbering and the two contributions stay individually
// attributable.
type ChangeRef struct {
	Seq     int    `json:"seq"`
	Turn    int    `json:"turn"`
	Session string `json:"session,omitempty"`
	Tool    string `json:"tool"`
	Intent  string `json:"intent,omitempty"`
	Added   int    `json:"added"`
	Removed int    `json:"removed"`
}

// File is every recorded change to one path committed.
type File struct {
	Path    string      `json:"path"`
	Changes []ChangeRef `json:"changes"`
}

// Turn is what one turn that touched a committed file actually
// demonstrated, folded from internal/agent's TurnOutcome. Session, like
// ChangeRef.Session, is filled in only across a merge.
type Turn struct {
	Turn        int      `json:"turn"`
	Session     string   `json:"session,omitempty"`
	Verdict     string   `json:"verdict"`
	Why         string   `json:"why"`
	Checks      []string `json:"checks,omitempty"`
	Passed      int      `json:"passed"`
	Failed      int      `json:"failed"`
	Tested      bool     `json:"tested"`
	TestsEdited []string `json:"tests_edited,omitempty"`
	// Mutation is the mutation-testing score for this turn, when one was
	// logged (see internal/mutate.Report.Event and TurnOutcomeInput.Mutation).
	Mutation *Mutation `json:"mutation,omitempty"`
}

// Mutation is a turn's mutation-testing score: how many of the small
// mutations tried against the lines it changed were caught by the tests.
type Mutation struct {
	Killed   int `json:"killed"`
	Survived int `json:"survived"`
	Total    int `json:"total"`
}

// Record is the evidence attached to one commit. Session and Model name
// the session that most recently attached to (or merged into) this
// record; per-item Session fields on ChangeRef and Turn disambiguate once
// more than one session has contributed (see Attach).
type Record struct {
	Version int       `json:"version"`
	Session string    `json:"session"`
	Model   string    `json:"model"`
	Created time.Time `json:"created"`
	Files   []File    `json:"files"`
	Turns   []Turn    `json:"turns"`
}

// Empty reports whether the record has nothing to show — the sentinel
// Build returns when none of a session's changes landed in the commit.
func (r Record) Empty() bool { return len(r.Files) == 0 }

// ChangeInput is one tracked file change, as internal/agent's Change
// reports it: Added/Removed are already computed (Change.Stats), since
// this package does not have the diff logic that produces them. Before and
// After are the file's full content just before and after this one change
// (internal/agent's Change.Before/After) — Build needs them to tell a
// change that actually landed in a given commit from one that only shares
// its path with a change an earlier commit already carried (see Build).
type ChangeInput struct {
	Seq, Turn      int
	Tool, Intent   string
	Path           string
	Added, Removed int
	Before, After  []byte
}

// FileVersions is one committed file's content just before and after the
// commit that touched it: Parent is nil when the file did not exist in the
// commit's parent (new file, or the commit is a root commit with none),
// Commit is nil when the commit deleted the file. CommittedFileVersions
// builds this from git; Build uses it to decide which of a session's
// changes to a path are actually part of one specific commit.
type FileVersions struct {
	Parent []byte
	Commit []byte
}

// TurnOutcomeInput is one turn's verdict, as internal/agent's TurnOutcome
// reports it.
type TurnOutcomeInput struct {
	Verdict     string
	Why         string
	Checks      []string
	Passed      int
	Failed      int
	Tested      bool
	TestsEdited []string
	// Mutation is the turn's mutation-testing score, folded from the
	// latest session.TypeMutation event logged for that turn, or nil when
	// none was.
	Mutation *Mutation
}

// Build assembles a Record from one session's tracked changes and turn
// outcomes, keeping only what actually landed in commit: a change whose
// path (absolute, or relative to repoRoot) does not normalise to one of
// committed's keys is dropped, and — see changeLandedIn — so is a change
// to a committed path whose content shows it was not part of this
// specific commit (a file edited more than once across the session, and
// committed more than once, must not have an earlier edit re-attributed
// to a later commit just because they share a path). A turn is kept only
// when it wrote a change that survived both filters. An empty result
// (Files == nil) means none of this session's changes reached the commit,
// and the caller should not attach it.
func Build(sessionID, model, repoRoot string, changes []ChangeInput, outcomes map[int]TurnOutcomeInput, committed map[string]FileVersions) Record {
	byPath := map[string]*File{}
	var order []string
	turnsUsed := map[int]bool{}
	for _, c := range changes {
		p := normPath(repoRoot, c.Path)
		fv, ok := committed[p]
		if !ok || !changeLandedIn(c, fv) {
			continue
		}
		f, ok := byPath[p]
		if !ok {
			f = &File{Path: p}
			byPath[p] = f
			order = append(order, p)
		}
		f.Changes = append(f.Changes, ChangeRef{
			Seq: c.Seq, Turn: c.Turn, Tool: c.Tool, Intent: c.Intent,
			Added: c.Added, Removed: c.Removed,
		})
		turnsUsed[c.Turn] = true
	}
	if len(order) == 0 {
		return Record{}
	}
	sort.Strings(order)
	files := make([]File, 0, len(order))
	for _, p := range order {
		f := *byPath[p]
		sort.Slice(f.Changes, func(i, j int) bool { return f.Changes[i].Seq < f.Changes[j].Seq })
		files = append(files, f)
	}
	turnNums := make([]int, 0, len(turnsUsed))
	for t := range turnsUsed {
		turnNums = append(turnNums, t)
	}
	sort.Ints(turnNums)
	var turns []Turn
	for _, tn := range turnNums {
		o, ok := outcomes[tn]
		if !ok {
			continue
		}
		turns = append(turns, Turn{
			Turn: tn, Verdict: o.Verdict, Why: o.Why, Checks: o.Checks,
			Passed: o.Passed, Failed: o.Failed, Tested: o.Tested, TestsEdited: o.TestsEdited,
			Mutation: o.Mutation,
		})
	}
	return Record{
		Version: Version, Session: sessionID, Model: model,
		Created: time.Now().UTC(), Files: files, Turns: turns,
	}
}

// changeLandedIn reports whether change c is actually part of the commit
// fv describes, as opposed to a change to the same path that a different
// commit — earlier or later in the session — already carries. This is the
// fix for the misattribution bug Build used to have: filtering by path
// alone means a file edited in turn 1 (committed in commit A) and again
// in turn 3 (committed in commit B) puts turn 1's change in both notes,
// because both commits touch the path.
//
// The rule, applied line by line rather than by byte-for-byte diff so it
// survives unrelated edits elsewhere in the file: take the lines c's own
// edit added (present in c.After, absent from c.Before). The change
// belongs to this commit when at least one of those lines is present in
// the commit's own version of the file (fv.Commit) and not every one of
// them was already present in the commit's parent (fv.Parent) — a change
// whose added lines are all already in the parent was committed earlier,
// by an earlier commit, and does not belong here. "At least one" rather
// than "all", in both directions, because a large edit can share
// incidental lines (a closing brace, a blank line) with content that
// predates it or postdates the commit; requiring every line to agree
// would make the check too easy to fool by an unrelated coincidence.
//
// A change with nothing to add (a pure deletion) falls back to the same
// logic on the lines it removed: it belongs to the commit when at least
// one of them was present before the commit (in fv.Parent) and not every
// one of them survived into the commit's own version (fv.Commit) — i.e.
// the commit actually is where they went away.
//
// Blank lines are excluded from every set compared here: they carry no
// attribution signal (every version of every file has some) and would
// otherwise make coincidental overlaps and "already there" false
// positives more likely, not less.
func changeLandedIn(c ChangeInput, fv FileVersions) bool {
	added := linesAddedBy(c.Before, c.After)
	if len(added) > 0 {
		return intersects(added, lineSet(fv.Commit)) && !isSubset(added, lineSet(fv.Parent))
	}
	removed := linesAddedBy(c.After, c.Before)
	if len(removed) == 0 {
		// No line-level change survives blank-line filtering either way:
		// nothing here to attribute to any commit.
		return false
	}
	return intersects(removed, lineSet(fv.Parent)) && !isSubset(removed, lineSet(fv.Commit))
}

// lineSet is data's non-blank lines as a set, for the content comparisons
// changeLandedIn makes. Line-exact, not fuzzy: a line changed by so much
// as whitespace counts as a different line, which is the conservative
// direction to be wrong in here (it can miss a genuine match rather than
// invent one).
func lineSet(data []byte) map[string]bool {
	set := map[string]bool{}
	for _, l := range diff.SplitLinesKeepEOL(string(data)) {
		if strings.TrimSpace(l) == "" {
			continue
		}
		set[l] = true
	}
	return set
}

// linesAddedBy returns the lines present in after but not in before, as a
// set. Used both ways (after vs before, and — for a pure deletion — before
// vs after) by changeLandedIn.
func linesAddedBy(before, after []byte) map[string]bool {
	b := lineSet(before)
	added := map[string]bool{}
	for l := range lineSet(after) {
		if !b[l] {
			added[l] = true
		}
	}
	return added
}

func intersects(a, b map[string]bool) bool {
	for l := range a {
		if b[l] {
			return true
		}
	}
	return false
}

func isSubset(a, b map[string]bool) bool {
	for l := range a {
		if !b[l] {
			return false
		}
	}
	return true
}

// commitParent returns commit's first parent, or "" when it has none (a
// root commit) — the tree state a commit's own content is measured
// against. git rev-list prints the commit's own sha followed by its
// parents' (multiple for a merge; this uses only the first), or the sha
// alone for a root commit, so the parent count tells the two apart without
// relying on parsing an error message.
func commitParent(ctx context.Context, repoDir, commit string) (string, error) {
	out, err := gitx.Run(ctx, repoDir, nil, "rev-list", "--parents", "-n1", commit)
	if err != nil {
		return "", err
	}
	fields := strings.Fields(out)
	if len(fields) < 2 {
		return "", nil
	}
	return fields[1], nil
}

// blobMissingRe matches git's message for a path that has no blob at a
// given revision — the file did not exist there yet, or was deleted — as
// opposed to a real error (bad repository, bad revision) that should not
// be swallowed into "no content".
var blobMissingRe = regexp.MustCompile(`(?i)exists on disk, but not in|does not exist in`)

// showBlob reads path's content at rev, ok=false when the path has no
// blob there (not an error: a new file has nothing at its parent, a
// deleted one has nothing at the commit).
func showBlob(ctx context.Context, repoDir, rev, path string) (data []byte, ok bool, err error) {
	data, err = gitx.RunBytes(ctx, repoDir, nil, "show", rev+":"+filepath.ToSlash(path))
	if err != nil {
		var ge *gitx.Error
		if errors.As(err, &ge) && blobMissingRe.MatchString(ge.Stderr) {
			return nil, false, nil
		}
		return nil, false, gitx.Wrapf(err, "git show %s:%s", gitx.Short(rev), path)
	}
	return data, true, nil
}

// CommittedFileVersions resolves, for every path in paths, the file's
// content just before (the commit's parent) and after (the commit
// itself) — what Build needs to tell which of a session's changes to a
// path actually landed in this commit (see changeLandedIn). paths is
// normally CommittedFiles(ctx, repoDir, commit)'s own result.
func CommittedFileVersions(ctx context.Context, repoDir, commit string, paths []string) (map[string]FileVersions, error) {
	parent, err := commitParent(ctx, repoDir, commit)
	if err != nil {
		return nil, err
	}
	out := make(map[string]FileVersions, len(paths))
	for _, p := range paths {
		var fv FileVersions
		if parent != "" {
			data, ok, err := showBlob(ctx, repoDir, parent, p)
			if err != nil {
				return nil, err
			}
			if ok {
				fv.Parent = data
			}
		}
		data, ok, err := showBlob(ctx, repoDir, commit, p)
		if err != nil {
			return nil, err
		}
		if ok {
			fv.Commit = data
		}
		out[filepath.ToSlash(p)] = fv
	}
	return out, nil
}

// normPath makes an absolute-or-relative path comparable to what git
// reports for a commit: relative to repoRoot, forward slashes.
func normPath(repoRoot, p string) string {
	if repoRoot != "" && filepath.IsAbs(p) {
		if rel, err := filepath.Rel(repoRoot, p); err == nil && !strings.HasPrefix(rel, "..") {
			p = rel
		}
	}
	return filepath.ToSlash(filepath.Clean(p))
}

// Root resolves the top level of the git work tree containing dir, the
// base every path in a Record and every git command here is anchored to.
func Root(ctx context.Context, dir string) (string, error) {
	return gitx.Run(ctx, dir, nil, "rev-parse", "--show-toplevel")
}

// Resolve returns the full commit sha that ref names (a short sha, "HEAD",
// a branch, ...), or an error naming what could not be resolved.
func Resolve(ctx context.Context, repoDir, ref string) (string, error) {
	return gitx.Run(ctx, repoDir, nil, "rev-parse", "--verify", "-q", ref+"^{commit}")
}

// Short is a commit sha shortened for display.
func Short(sha string) string { return gitx.Short(sha) }

// Attach stores r as the proof note on commit, merging it with whatever
// note is already there (see merge) so that two sessions contributing to
// one commit both show, instead of the second overwriting the first.
func Attach(ctx context.Context, repoDir, commit string, r Record) error {
	// So the note survives a later `git commit --amend` or `git rebase`
	// on this commit instead of being silently dropped (see
	// EnsureRewriteConfig); a no-op after the first call in a repository.
	if err := EnsureRewriteConfig(ctx, repoDir); err != nil {
		return err
	}
	if existing, ok, err := Read(ctx, repoDir, commit); err != nil {
		return err
	} else if ok {
		r = merge(existing, r)
	}
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	if _, err := gitx.RunInput(ctx, repoDir, data, "notes", "--ref="+NotesRef, "add", "-f", "-F", "-", commit); err != nil {
		return gitx.Wrapf(err, "git notes add")
	}
	return nil
}

// noNoteRe matches git's message for a commit that carries no note yet,
// on both the porcelain and human-readable wordings it has used.
var noNoteRe = regexp.MustCompile(`(?i)no note found for object`)

// Read returns the proof record attached to commit, or ok=false when the
// commit has none.
func Read(ctx context.Context, repoDir, commit string) (Record, bool, error) {
	out, err := gitx.RunBytes(ctx, repoDir, nil, "notes", "--ref="+NotesRef, "show", commit)
	if err != nil {
		var ge *gitx.Error
		if errors.As(err, &ge) && noNoteRe.MatchString(ge.Stderr) {
			return Record{}, false, nil
		}
		return Record{}, false, gitx.Wrapf(err, "git notes show")
	}
	var r Record
	if err := json.Unmarshal(out, &r); err != nil {
		return Record{}, false, fmt.Errorf("proof note on %s is not valid JSON: %w", gitx.Short(commit), err)
	}
	return r, true, nil
}

// CommittedFiles lists the paths one commit changed, repo-root-relative.
// A root commit (no parent) is handled the same way as any other: --root
// tells diff-tree to treat it as a diff against the empty tree, which is
// otherwise the one case it reports nothing for.
func CommittedFiles(ctx context.Context, repoDir, commit string) ([]string, error) {
	out, err := gitx.Run(ctx, repoDir, nil, "diff-tree", "--no-commit-id", "--name-only", "-r", "--root", commit)
	if err != nil {
		return nil, err
	}
	if out == "" {
		return nil, nil
	}
	return strings.Split(out, "\n"), nil
}

// verdictMark is TurnOutcome.Line's ✓/⚠/✗ convention, reproduced here
// rather than imported: the mark is a property of the verdict string, not
// of the agent package, and importing it back would cycle.
func verdictMark(verdict string) string {
	switch verdict {
	case "verified":
		return "✓"
	case "check failed", "vacuous":
		return "✗"
	case "unverified", "not demonstrated", "self-checked":
		return "⚠"
	default:
		return "—"
	}
}

// Summary renders r for a person: one line per turn's verdict, then per
// file the changes that landed with their intents.
func Summary(r Record) string {
	if r.Empty() {
		return "no proof recorded"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "proof · session %s · model %s · %s\n", orDash(r.Session), orDash(r.Model), r.Created.Format(time.RFC3339))
	for _, t := range r.Turns {
		who := ""
		if t.Session != "" {
			who = " (" + t.Session + ")"
		}
		fmt.Fprintf(&b, "%s turn %d%s: %s: %s", verdictMark(t.Verdict), t.Turn, who, t.Verdict, t.Why)
		if t.Mutation != nil {
			fmt.Fprintf(&b, " · mutations %d/%d caught", t.Mutation.Killed, t.Mutation.Total)
		}
		b.WriteString("\n")
	}
	for _, f := range r.Files {
		fmt.Fprintf(&b, "%s\n", f.Path)
		for _, c := range f.Changes {
			who := ""
			if c.Session != "" {
				who = " (" + c.Session + ")"
			}
			fmt.Fprintf(&b, "  #%d turn %d%s %s +%d -%d", c.Seq, c.Turn, who, c.Tool, c.Added, c.Removed)
			if c.Intent != "" {
				fmt.Fprintf(&b, " · %s", c.Intent)
			}
			b.WriteString("\n")
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

// Line is one line of a committed file with what proof knows about it:
// the commit that wrote it (from git blame) and, when that commit carries
// a proof note naming the file, the intent and verdict behind it.
type Line struct {
	No      int
	Commit  string
	Text    string
	Intent  string
	Verdict string
}

// blameHeaderRe matches a git blame --porcelain line-attribution header:
// "<sha> <orig-line> <final-line>[ <num-lines>]". It fires on both the
// full header (first time a commit is seen) and the short repeat form,
// which is all that is needed to know which commit wrote which line.
var blameHeaderRe = regexp.MustCompile(`^([0-9a-f]{40}) \d+ (\d+)`)

// BlameCommitted attributes each line of path to the commit git blame
// says wrote it, and — when that commit carries a proof note mentioning
// path — the intent and verdict of the change. Notes are read once per
// commit and cached, since a file's lines commonly come from a handful of
// commits.
func BlameCommitted(ctx context.Context, repoDir, path string) ([]Line, error) {
	out, err := gitx.RunBytes(ctx, repoDir, nil, "blame", "--porcelain", "--", path)
	if err != nil {
		return nil, err
	}
	var lines []Line
	var curSHA string
	var curNo int
	for _, raw := range strings.Split(string(out), "\n") {
		if m := blameHeaderRe.FindStringSubmatch(raw); m != nil {
			curSHA = m[1]
			curNo, _ = strconv.Atoi(m[2])
			continue
		}
		if strings.HasPrefix(raw, "\t") {
			lines = append(lines, Line{No: curNo, Commit: curSHA, Text: raw[1:]})
		}
	}
	cache := map[string]*Record{}
	relPath := path
	if root, rerr := Root(ctx, repoDir); rerr == nil {
		relPath = normPath(root, absIn(repoDir, path))
	}
	for i := range lines {
		rec, ok := cache[lines[i].Commit]
		if !ok {
			if r, found, rerr := Read(ctx, repoDir, lines[i].Commit); rerr == nil && found {
				rc := r
				rec = &rc
			}
			cache[lines[i].Commit] = rec
		}
		if rec == nil {
			continue
		}
		for _, f := range rec.Files {
			if f.Path != relPath || len(f.Changes) == 0 {
				continue
			}
			last := f.Changes[len(f.Changes)-1]
			lines[i].Intent = last.Intent
			for _, t := range rec.Turns {
				if t.Turn == last.Turn {
					lines[i].Verdict = t.Verdict
					break
				}
			}
		}
	}
	return lines, nil
}

func absIn(dir, p string) string {
	if filepath.IsAbs(p) {
		return p
	}
	return filepath.Join(dir, p)
}

// merge unions two records attached to the same commit, tagging each
// record's items with the session that produced it before folding them
// together so Seq and Turn — unique only within a session — cannot
// collide across sessions. The more recently attached record's Session
// and Model become the record's top-level values; both contributions
// remain visible in Files and Turns.
func merge(existing, incoming Record) Record {
	tagSession(&existing)
	tagSession(&incoming)
	out := Record{
		Version: max(existing.Version, incoming.Version),
		Session: incoming.Session,
		Model:   incoming.Model,
		Created: existing.Created,
	}
	if out.Created.IsZero() {
		out.Created = incoming.Created
	}
	files := map[string]*File{}
	var order []string
	addFiles := func(fs []File) {
		for _, f := range fs {
			cur, ok := files[f.Path]
			if !ok {
				cp := File{Path: f.Path}
				files[f.Path] = &cp
				cur = &cp
				order = append(order, f.Path)
			}
			cur.Changes = append(cur.Changes, f.Changes...)
		}
	}
	addFiles(existing.Files)
	addFiles(incoming.Files)
	sort.Strings(order)
	for _, p := range order {
		f := *files[p]
		f.Changes = dedupChanges(f.Changes)
		sort.Slice(f.Changes, func(i, j int) bool {
			if f.Changes[i].Session != f.Changes[j].Session {
				return f.Changes[i].Session < f.Changes[j].Session
			}
			return f.Changes[i].Seq < f.Changes[j].Seq
		})
		out.Files = append(out.Files, f)
	}
	// Keyed by session and turn number, and ordered by them — the turn
	// numerically, not as the string it is keyed by: sorting "10" before
	// "2" put a merged record's turns in an order no reader expects.
	type turnKey struct {
		session string
		turn    int
	}
	turns := map[turnKey]Turn{}
	var tkeys []turnKey
	addTurns := func(ts []Turn) {
		for _, t := range ts {
			k := turnKey{t.Session, t.Turn}
			if _, ok := turns[k]; !ok {
				tkeys = append(tkeys, k)
			}
			turns[k] = t
		}
	}
	addTurns(existing.Turns)
	addTurns(incoming.Turns)
	sort.Slice(tkeys, func(i, j int) bool {
		if tkeys[i].session != tkeys[j].session {
			return tkeys[i].session < tkeys[j].session
		}
		return tkeys[i].turn < tkeys[j].turn
	})
	for _, k := range tkeys {
		out.Turns = append(out.Turns, turns[k])
	}
	return out
}

// tagSession backfills every item's Session with the record's own, so
// items from a record attached before per-item Session existed (or built
// fresh by Build, which leaves it blank) can still be told apart once
// merged with another session's contribution.
func tagSession(r *Record) {
	for i := range r.Files {
		for j := range r.Files[i].Changes {
			if r.Files[i].Changes[j].Session == "" {
				r.Files[i].Changes[j].Session = r.Session
			}
		}
	}
	for i := range r.Turns {
		if r.Turns[i].Session == "" {
			r.Turns[i].Session = r.Session
		}
	}
}

func dedupChanges(cs []ChangeRef) []ChangeRef {
	type changeKey struct {
		session string
		seq     int
	}
	seen := map[changeKey]bool{}
	var out []ChangeRef
	for _, c := range cs {
		k := changeKey{c.Session, c.Seq}
		if seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, c)
	}
	return out
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
