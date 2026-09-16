// Package memory keeps what janetcode learns across sessions: one markdown
// file per entry with frontmatter (name, description, type, scope, ...),
// in a project scope keyed by project identity and a global scope shared
// by every project. An index of names and descriptions goes into the
// system prompt; bodies are attached to a turn when they are relevant, or
// fetched by the model through the memory tool.
package memory

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/xmonader/janetcode/internal/fsutil"
	"github.com/xmonader/janetcode/internal/md"
	"github.com/xmonader/janetcode/internal/project"
	"github.com/xmonader/janetcode/internal/textfmt"
)

// MarkerFile, when present in the project, pins its identity to the
// file's content so several checkouts share one memory.
const MarkerFile = ".janetcode/project"

// GlobalKey names the scope shared by every project.
const GlobalKey = "_global"

// Types of memory. The type tells the model how to use an entry.
const (
	TypeUser      = "user"      // who the user is, how they like to work
	TypeFeedback  = "feedback"  // corrections and confirmed approaches
	TypeProject   = "project"   // facts about this project
	TypeDecision  = "decision"  // a choice that was made and why
	TypeReference = "reference" // pointers to URLs, tickets, dashboards
)

// Types lists the valid types.
var Types = []string{TypeUser, TypeFeedback, TypeProject, TypeDecision, TypeReference}

// Limits.
const (
	MaxEntries   = 200      // per scope; the least used, oldest go first
	MaxBody      = 2_000    // bytes per entry body
	MaxIndexLine = 160      // bytes of description shown in the index
	IndexBudget  = 8 * 1024 // bytes of index in the system prompt
	RecallBudget = 6 * 1024 // bytes of bodies attached to one turn
	MaxRecall    = 4        // entries attached to one turn
)

// Entry is one remembered fact.
type Entry struct {
	Name        string // slug; the file name
	Description string // one line, shown in the index
	Type        string
	Scope       string // "project" or "global"
	Body        string
	Source      string // "user", "model" or "auto"
	Created     time.Time
	Updated     time.Time
	Uses        int
	Path        string
	// Ecosystems are the languages or toolchains a global entry is about
	// ("go", "python"), recorded when it is written and inferred again
	// from its text when it is read, so one written before tagging existed
	// is still recognised. A global entry about one ecosystem is never
	// recalled in a project of another (see Store.hidden).
	Ecosystems []string
}

// Key returns the project identity: the marker file's content, else the
// git origin URL, else a hash of the path. The result is safe as a
// directory name.
func Key(cwd string) string {
	if data, err := os.ReadFile(filepath.Join(cwd, MarkerFile)); err == nil {
		if id := strings.TrimSpace(string(data)); id != "" {
			return Slug(id)
		}
	}
	cmd := exec.Command("git", "config", "--get", "remote.origin.url")
	cmd.Dir = cwd
	if out, err := cmd.Output(); err == nil {
		if u := normalizeRemote(strings.TrimSpace(string(out))); u != "" {
			return Slug(u)
		}
	}
	sum := sha256.Sum256([]byte(cwd))
	return filepath.Base(cwd) + "-" + hex.EncodeToString(sum[:6])
}

// normalizeRemote reduces the ways of writing one remote to a single form:
// host/owner/repo without scheme, user, port or .git.
func normalizeRemote(u string) string {
	u = strings.TrimSuffix(u, "/")
	u = strings.TrimSuffix(u, ".git")
	if i := strings.Index(u, "://"); i >= 0 {
		u = u[i+3:]
	}
	if i := strings.Index(u, "@"); i >= 0 {
		u = u[i+1:]
	}
	u = strings.Replace(u, ":", "/", 1)
	u = strings.TrimSuffix(u, "/")
	return strings.ToLower(u)
}

var unsafeRe = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

// Slug makes s safe as a file or directory name.
func Slug(s string) string {
	s = strings.Trim(unsafeRe.ReplaceAllString(strings.ToLower(s), "-"), "-.")
	if len(s) > 80 {
		sum := sha256.Sum256([]byte(s))
		s = s[:60] + "-" + hex.EncodeToString(sum[:4])
	}
	if s == "" {
		return "entry"
	}
	return s
}

// Store is the memory of one project plus the global scope.
type Store struct {
	Root string // <data>/memory
	// Key is the project key and Ecosystem the project's language or
	// toolchain, as project.Detect names it in lower case ("go",
	// "python"), or "" when it is not recognised. Rekey changes both when
	// /cd moves the session to another project, so anything that reads
	// them while a session is running goes through ProjectKey and
	// ecosystem, which take mu; the fields stay exported because the
	// store is also built and read before it has any concurrency.
	Key string
	// Embed, when set, adds semantic ranking to recall (see EmbedFunc).
	Embed     EmbedFunc
	Ecosystem string

	mu        sync.Mutex
	embedMu   sync.Mutex
	vecs      map[string]vecEntry
	embedDown time.Time
}

// Open returns the store for the project at cwd under dataDir/memory and
// migrates the flat file an earlier version kept.
func Open(dataDir, cwd string) *Store {
	s := &Store{Root: filepath.Join(dataDir, "memory"), Key: Key(cwd)}
	if p, ok := project.Detect(cwd); ok {
		s.Ecosystem = strings.ToLower(p.Kind)
	}
	s.migrate()
	return s
}

// ProjectKey is the project identity this store is writing under, read
// safely against a concurrent Rekey.
func (s *Store) ProjectKey() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.Key
}

// ecosystem is Ecosystem read under the lock. Callers must not already
// hold s.mu.
func (s *Store) ecosystem() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.Ecosystem
}

// dir is the directory a scope's entries live in. The caller holds s.mu:
// the project key it names is what Rekey changes.
func (s *Store) dir(scope string) string {
	if scope == "global" {
		return filepath.Join(s.Root, GlobalKey)
	}
	return filepath.Join(s.Root, s.Key)
}

// Entries returns every entry of both scopes, most recently updated first.
func (s *Store) Entries() []Entry {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.read()
}

func (s *Store) read() []Entry {
	var out []Entry
	for _, scope := range []string{"project", "global"} {
		names, _ := filepath.Glob(filepath.Join(s.dir(scope), "*.md"))
		for _, p := range names {
			if e, ok := readEntry(p, scope); ok {
				out = append(out, e)
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Updated.After(out[j].Updated) })
	return out
}

func readEntry(p, scope string) (Entry, bool) {
	data, err := os.ReadFile(p)
	if err != nil {
		return Entry{}, false
	}
	meta, body := md.Parse(string(data))
	e := Entry{
		Name:        strings.TrimSuffix(filepath.Base(p), ".md"),
		Description: meta["description"],
		Type:        meta["type"],
		Scope:       scope,
		Body:        strings.TrimSpace(body),
		Source:      meta["source"],
		Path:        p,
	}
	if e.Description == "" && e.Body == "" {
		return Entry{}, false
	}
	if e.Description == "" {
		e.Description = firstLine(e.Body)
	}
	if e.Body == "" {
		e.Body = e.Description
	}
	e.Created, _ = time.Parse(time.RFC3339, meta["created"])
	e.Updated, _ = time.Parse(time.RFC3339, meta["updated"])
	e.Uses, _ = strconv.Atoi(meta["uses"])
	if scope == "global" {
		e.Ecosystems = ecosystemsOf(e, meta["ecosystem"])
	}
	return e, true
}

func (s *Store) write(e Entry) error {
	if err := os.MkdirAll(filepath.Dir(e.Path), 0o700); err != nil {
		return err
	}
	meta := map[string]string{
		"name": e.Name, "description": e.Description, "type": e.Type, "source": e.Source,
		"created": e.Created.UTC().Format(time.RFC3339), "updated": e.Updated.UTC().Format(time.RFC3339),
	}
	if e.Uses > 0 {
		meta["uses"] = strconv.Itoa(e.Uses)
	}
	if len(e.Ecosystems) > 0 {
		meta["ecosystem"] = strings.Join(e.Ecosystems, ",")
	}
	return fsutil.AtomicWrite(e.Path, []byte(md.Render(meta, e.Body)))
}

// Get finds an entry by name in either scope.
func (s *Store) Get(name string) (Entry, bool) {
	name = Slug(name)
	for _, e := range s.Entries() {
		if e.Name == name {
			return e, true
		}
	}
	return Entry{}, false
}

// Status says what Put did.
type Status int

const (
	Unchanged Status = iota // an equal entry already existed
	Created
	Updated
)

// Put creates or updates an entry. An entry of the same name is updated;
// otherwise one whose description says nearly the same thing is updated
// instead of duplicated.
func (s *Store) Put(e Entry) (Entry, Status, error) {
	e.Description = oneLine(e.Description, MaxIndexLine)
	e.Body = strings.TrimSpace(e.Body)
	if len(e.Body) > MaxBody {
		e.Body = textfmt.ClipBytes(e.Body, MaxBody)
	}
	if e.Description == "" && e.Body == "" {
		return Entry{}, Unchanged, errors.New("nothing to remember")
	}
	// Memory is written to disk and read back into every later prompt,
	// so a credential that lands here is a credential that keeps being
	// sent. The entry is refused rather than scrubbed: what was worth
	// remembering was where the value lives, not the value.
	if what := ScanSecret(e.Description + "\n" + e.Body); what != "" {
		return Entry{}, Unchanged, errSecret(what)
	}
	if e.Description == "" {
		e.Description = oneLine(e.Body, MaxIndexLine)
	}
	if e.Body == "" {
		e.Body = e.Description
	}
	if e.Name == "" {
		e.Name = nameFor(e.Description)
	}
	e.Name = Slug(e.Name)
	if !validType(e.Type) {
		e.Type = guessType(e.Description + " " + e.Body)
	}
	if e.Scope != "global" {
		e.Scope = "project"
	}
	if e.Source == "" {
		e.Source = "user"
	}
	if e.Scope == "global" {
		e.Ecosystems = ecosystemsOf(e, strings.Join(e.Ecosystems, ","))
	} else {
		e.Ecosystems = nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	existing := s.read()
	now := time.Now()
	for _, old := range existing {
		if old.Scope == e.Scope && (old.Name == e.Name || similar(old.Description, e.Description)) {
			if old.Name != e.Name {
				// Merge into the older entry; keep its name and history.
				e.Name = old.Name
			}
			if strings.EqualFold(old.Body, e.Body) && strings.EqualFold(old.Description, e.Description) && old.Type == e.Type && strings.Join(old.Ecosystems, ",") == strings.Join(e.Ecosystems, ",") {
				return old, Unchanged, nil
			}
			e.Created, e.Uses, e.Updated, e.Path = old.Created, old.Uses, now, old.Path
			return e, Updated, s.write(e)
		}
	}
	e.Created, e.Updated = now, now
	e.Path = filepath.Join(s.dir(e.Scope), e.Name+".md")
	if err := s.write(e); err != nil {
		return e, Unchanged, err
	}
	s.trim(e.Scope, existing)
	return e, Created, nil
}

// trim keeps a scope under MaxEntries by dropping the least used, oldest
// entries.
func (s *Store) trim(scope string, all []Entry) {
	var in []Entry
	for _, e := range all {
		if e.Scope == scope {
			in = append(in, e)
		}
	}
	if len(in)+1 <= MaxEntries {
		return
	}
	sort.SliceStable(in, func(i, j int) bool {
		if in[i].Uses != in[j].Uses {
			return in[i].Uses < in[j].Uses
		}
		return in[i].Updated.Before(in[j].Updated)
	})
	for _, e := range in[:len(in)+1-MaxEntries] {
		os.Remove(e.Path)
	}
}

// Remove forgets an entry by name (or by its number in the listing).
func (s *Store) Remove(ref string) (Entry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	all := s.read()
	if n, err := strconv.Atoi(ref); err == nil {
		if n < 1 || n > len(all) {
			return Entry{}, fmt.Errorf("no memory entry %d (there are %d)", n, len(all))
		}
		return all[n-1], os.Remove(all[n-1].Path)
	}
	name := Slug(ref)
	for _, e := range all {
		if e.Name == name {
			return e, os.Remove(e.Path)
		}
	}
	return Entry{}, fmt.Errorf("no memory entry named %q", ref)
}

// Touch records that entries were used, so trimming keeps useful ones.
func (s *Store) Touch(entries ...Entry) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, e := range entries {
		if cur, ok := readEntry(e.Path, e.Scope); ok {
			cur.Uses++
			s.write(cur)
		}
	}
}

// Index renders names and descriptions for the system prompt, or "" when
// nothing is remembered. It stays within IndexBudget; bodies are not
// included, the model recalls them when needed.
func (s *Store) Index() string { return s.IndexWithin(IndexBudget) }

// BudgetFor is how many bytes of index a model with this context window
// should give up to the things it might need rather than the things it
// asked for. IndexBudget is a flat 8 KB — 2,000 tokens — which is a
// twenty-fifth of a 200k window and a sixteenth of a 32k one. Two per
// cent of the window is the same share either way; the flat figure stays
// as the ceiling, since past a point a longer index is not more useful,
// just longer. A window that is not known keeps the ceiling.
func BudgetFor(contextWindow int) int {
	if contextWindow <= 0 {
		return IndexBudget
	}
	b := contextWindow * 4 / 50 // 2% of the window, at four bytes a token
	if b > IndexBudget {
		return IndexBudget
	}
	if b < 1024 {
		// Below this an index is not worth having at all, but the entries
		// the user wrote by hand should not vanish silently either.
		return 1024
	}
	return b
}

// IndexWithin is Index with an explicit byte budget.
func (s *Store) IndexWithin(budget int) string {
	entries := s.Entries()
	if len(entries) == 0 {
		return ""
	}
	if budget <= 0 {
		budget = IndexBudget
	}
	var sb strings.Builder
	sb.WriteString("\n# Memory\nWhat you learned in earlier sessions, as name (type): description. Feedback and user entries say how the user wants you to work: follow them. Relevant entries are attached to a request automatically; for any other, call memory {\"action\":\"recall\"} before relying on it. When the user tells you something worth keeping (a preference, a correction, a decision, a fact about the project), save it with memory {\"action\":\"remember\"}.\n")
	visible := entries[:0:0]
	for _, e := range entries {
		// An automatic global entry nobody tagged may be one project's
		// rule; it waits for a strong match instead of sitting in every
		// project's prompt.
		if h, strong := s.hidden(e); !h && !strong {
			visible = append(visible, e)
		}
	}
	entries = visible
	if len(entries) == 0 {
		return ""
	}
	shown := 0
	for _, e := range entries {
		line := fmt.Sprintf("- %s (%s%s): %s\n", e.Name, e.Type, scopeTag(e), e.Description)
		if sb.Len()+len(line) > budget {
			break
		}
		sb.WriteString(line)
		shown++
	}
	if shown < len(entries) {
		fmt.Fprintf(&sb, "- (%d more; memory recall finds them)\n", len(entries)-shown)
	}
	return sb.String()
}

func scopeTag(e Entry) string {
	if e.Scope == "global" {
		return ", global"
	}
	return ""
}

// Recall returns the entries most relevant to text, best first, at most
// max, using word overlap with names, descriptions and bodies. Entries
// need a clear signal so unrelated turns attach nothing.
func (s *Store) Recall(text string, max int) []Entry { return s.rank(text, max, 4) }

// Search is Recall for an explicit query: any overlap counts.
func (s *Store) Search(text string, max int) []Entry { return s.rank(text, max, 1) }

func (s *Store) rank(text string, max, min int) []Entry {
	if max <= 0 {
		max = MaxRecall
	}
	ws := Words(text)
	if len(ws) == 0 {
		return nil
	}
	have := map[string]bool{}
	for _, w := range ws {
		have[w] = true
	}
	type scored struct {
		e     Entry
		score int
	}
	var hits []scored
	for _, e := range s.Entries() {
		hide, strong := s.hidden(e)
		if hide {
			continue
		}
		need := min
		if strong && need < strongRecall {
			need = strongRecall
		}
		score := 0
		seen := map[string]bool{}
		for _, w := range Words(e.Name + " " + e.Description) {
			if have[w] && !seen[w] && !stop[w] {
				seen[w] = true
				score += 3
			}
		}
		for _, w := range Words(e.Body) {
			if have[w] && !seen[w] && !stop[w] {
				seen[w] = true
				score++
			}
		}
		if score >= need {
			hits = append(hits, scored{e, score})
		}
	}
	sort.SliceStable(hits, func(i, j int) bool { return hits[i].score > hits[j].score })
	if len(hits) > max {
		hits = hits[:max]
	}
	out := make([]Entry, len(hits))
	for i, h := range hits {
		out[i] = h.e
	}
	return out
}

// strongRecall is the word score an untagged automatic global entry needs
// to be recalled: two words of its name or description, not one.
const strongRecall = 6

// hidden reports whether e must not reach this project at all — a global
// entry about another ecosystem (a Python project's rule in a Go project)
// — and, when it may, whether it needs a strong match first: a global
// entry that names no ecosystem and was not written by the user, which
// could as easily be one project's rule as a preference. Project entries
// and globals the user wrote with /remember -g are neither.
//
// It takes s.mu to read the project's ecosystem, so a caller must not
// already hold it.
func (s *Store) hidden(e Entry) (hide, strong bool) {
	if e.Scope != "global" {
		return false, false
	}
	if len(e.Ecosystems) > 0 {
		here := s.ecosystem()
		for _, eco := range e.Ecosystems {
			if eco == here {
				return false, false
			}
		}
		return true, false
	}
	return false, e.Source != "user"
}

// ecosystemPatterns recognise the languages and toolchains a memory is
// about by the words it uses. "go" alone is English, so Go needs its
// toolchain's words.
var ecosystemPatterns = map[string]*regexp.Regexp{
	"go":     regexp.MustCompile(`(?i)\b(golang|gofmt|gopls|goroutines?|go\.mod|go (test|build|vet|run|mod|generate|install))\b`),
	"python": regexp.MustCompile(`(?i)\b(python3?|pip|pytest|poetry|pipenv|virtualenv|venv|pyproject|mypy|ruff|django|flask)\b`),
	"node":   regexp.MustCompile(`(?i)\b(node(\.js|js)?|npm|npx|yarn|pnpm|javascript|typescript|eslint|jest|package\.json)\b`),
	"rust":   regexp.MustCompile(`(?i)\b(rust|cargo|rustc|clippy|rustfmt|crates?\.io)\b`),
}

// ecosystemsOf is the sorted union of the comma-separated tag recorded on
// an entry and the ecosystems its text names.
func ecosystemsOf(e Entry, tag string) []string {
	set := map[string]bool{}
	for _, t := range strings.Split(tag, ",") {
		if t = strings.ToLower(strings.TrimSpace(t)); t != "" {
			set[t] = true
		}
	}
	text := e.Name + " " + e.Description + " " + e.Body
	for eco, re := range ecosystemPatterns {
		if re.MatchString(text) {
			set[eco] = true
		}
	}
	if len(set) == 0 {
		return nil
	}
	out := make([]string, 0, len(set))
	for eco := range set {
		out = append(out, eco)
	}
	sort.Strings(out)
	return out
}

// Render formats an entry for the model.
func Render(e Entry) string {
	return fmt.Sprintf("<memory name=%q type=%q scope=%q>\n%s\n</memory>", e.Name, e.Type, e.Scope, e.Body)
}

// migrate converts the flat "<key>.md" list an earlier version wrote into
// entries, then renames it out of the way.
func (s *Store) migrate() {
	old := filepath.Join(s.Root, s.Key+".md")
	data, err := os.ReadFile(old)
	if err != nil {
		return
	}
	for _, line := range strings.Split(string(data), "\n") {
		if line = strings.TrimSpace(line); strings.HasPrefix(line, "- ") {
			s.Put(Entry{Description: strings.TrimSpace(line[2:]), Source: "auto"})
		}
	}
	os.Rename(old, old+".migrated")
}

// --- text helpers ---

var wordRe = regexp.MustCompile(`[a-z0-9][a-z0-9_.+-]{2,}`)

// Words tokenises text for matching: lower-case, at least three characters,
// plurals folded.
func Words(s string) []string {
	ws := wordRe.FindAllString(strings.ToLower(s), -1)
	for i, w := range ws {
		ws[i] = stem(w)
	}
	return ws
}

func stem(w string) string {
	switch {
	case strings.HasSuffix(w, "ies") && len(w) > 4:
		return w[:len(w)-3] + "y"
	case strings.HasSuffix(w, "ss"):
		return w
	case strings.HasSuffix(w, "s") && len(w) > 3:
		return w[:len(w)-1]
	}
	return w
}

var stop = map[string]bool{
	"the": true, "and": true, "for": true, "with": true, "this": true, "that": true, "from": true,
	"you": true, "are": true, "use": true, "when": true, "how": true, "what": true, "into": true,
	"file": true, "code": true, "project": true, "run": true, "make": true, "new": true, "should": true,
	"add": true, "all": true, "any": true, "can": true, "not": true, "but": true, "its": true, "user": true,
	"want": true, "never": true, "always": true, "please": true, "just": true, "like": true, "need": true,
}

// similar reports whether two descriptions say nearly the same thing
// (Jaccard overlap of their words above 0.6).
func similar(a, b string) bool {
	as, bs := map[string]bool{}, map[string]bool{}
	for _, w := range Words(a) {
		if !stop[w] {
			as[w] = true
		}
	}
	for _, w := range Words(b) {
		if !stop[w] {
			bs[w] = true
		}
	}
	if len(as) < 3 || len(bs) < 3 {
		return strings.EqualFold(oneLine(a, 0), oneLine(b, 0))
	}
	both := 0
	for w := range as {
		if bs[w] {
			both++
		}
	}
	return float64(both)/float64(len(as)+len(bs)-both) >= 0.6
}

func validType(t string) bool {
	for _, v := range Types {
		if v == t {
			return true
		}
	}
	return false
}

var feedbackRe = regexp.MustCompile(`(?i)\b(never|always|don'?t|do not|prefer|instead of|stop |from now on|rather than|must)\b`)
var referenceRe = regexp.MustCompile(`(?i)https?://|\b(ticket|issue #|dashboard|jira|url)\b`)
var decisionRe = regexp.MustCompile(`(?i)\b(decided|decision|chose|because|we will|going with)\b`)

func guessType(text string) string {
	switch {
	case referenceRe.MatchString(text):
		return TypeReference
	case feedbackRe.MatchString(text):
		return TypeFeedback
	case decisionRe.MatchString(text):
		return TypeDecision
	}
	return TypeProject
}

// LooksLikeFeedback reports whether user text sounds like a preference or
// correction worth extracting even when nothing was changed.
func LooksLikeFeedback(text string) bool { return feedbackRe.MatchString(text) }

func nameFor(desc string) string {
	var parts []string
	for _, w := range wordRe.FindAllString(strings.ToLower(desc), -1) {
		if !stop[stem(w)] {
			parts = append(parts, w)
		}
		if len(parts) == 5 {
			break
		}
	}
	if len(parts) == 0 {
		parts = []string{"note", strconv.FormatInt(time.Now().Unix(), 36)}
	}
	return strings.Join(parts, "-")
}

// oneLine flattens s to one line (see textfmt.Squash), dropping a leading
// "- " list bullet, and cuts it to max bytes with "…" when max > 0.
func oneLine(s string, max int) string {
	s = textfmt.Squash(strings.TrimPrefix(strings.TrimSpace(s), "- "))
	if max > 0 {
		s = textfmt.ClipBytes(s, max)
	}
	return s
}

// firstLine is s's first line without Markdown heading markers (see
// textfmt.HeadingLine), flattened and capped as an index description.
func firstLine(s string) string {
	return oneLine(textfmt.HeadingLine(s, 0), MaxIndexLine)
}

// Clean normalises free text into a one-line description.
func Clean(text string) string { return oneLine(text, MaxIndexLine) }

// Rekey points the store at another project: memory is per project, so a
// session that moves with /cd reads and writes the new project's entries.
// The global scope is unaffected — it is global on purpose.
func (s *Store) Rekey(cwd string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Key = Key(cwd)
	s.Ecosystem = ""
	if p, ok := project.Detect(cwd); ok {
		s.Ecosystem = strings.ToLower(p.Kind)
	}
	s.vecs = nil
}
