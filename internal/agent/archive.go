package agent

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/xmonader/janetcode/internal/session"
	"github.com/xmonader/janetcode/internal/textfmt"
	"github.com/xmonader/janetcode/internal/tool"
)

// InlineOutput is the largest tool result that goes to the model verbatim.
// Longer outputs are archived in full and the model sees both ends plus an
// id it can hand to expand_output to read the rest.
const InlineOutput = 24 * 1024

// ArchiveCap bounds the archive on disk; the oldest entries are dropped
// past it. It is generous on purpose: the archive is what compaction
// leaves the model to read back, and an entry evicted to save disk is a
// fact the model can no longer retrieve, so a few large build logs must
// not push out the outputs a session is still working from.
const ArchiveCap = 256 << 20

// ErrArchiveMissing is returned for an id that was never archived or has
// been evicted.
var ErrArchiveMissing = errors.New("no archived output with that id (it may have been evicted)")

// Entry describes one archived tool output.
type Entry struct {
	ID      string `json:"-"`
	ToolUse string `json:"tool_use"`
	Name    string `json:"name"`
	// Subject is what the call was about — the path read, the command
	// run, the pattern searched — so a model looking for "the earlier
	// read of parse.py" can tell the entries apart without opening them.
	Subject string    `json:"subject,omitempty"`
	Size    int       `json:"size"`
	Created time.Time `json:"created"`
}

// Archive keeps full tool outputs on disk under ids like ctx-12, one file
// per entry (a JSON header line, then the body). It is shared by every
// session of a project, so ids stay valid across resumes and compaction.
type Archive struct {
	Dir string

	mu    sync.Mutex
	next  int
	byUse map[string]string // tool_use id -> archive id
	ready bool
	// bytes is what this process believes the archive holds, seeded at
	// load and grown by each Put. It only decides whether the cap could
	// have been reached; the eviction pass itself re-reads the directory,
	// which is what keeps it right when another process shares it.
	bytes int64
	// scans counts directory walks, so a test can hold the "storing an
	// output does not re-read the archive" property that bytes exists for.
	scans int
}

// NewArchive opens (or creates on first Put) the archive at dir.
func NewArchive(dir string) *Archive { return &Archive{Dir: dir} }

func (ar *Archive) load() error {
	if ar.ready {
		return nil
	}
	ar.byUse = map[string]string{}
	if err := os.MkdirAll(ar.Dir, 0o700); err != nil {
		return err
	}
	ar.bytes = 0
	for _, e := range ar.scan() {
		if n := seq(e.ID); n >= ar.next {
			ar.next = n + 1
		}
		if e.ToolUse != "" {
			ar.byUse[e.ToolUse] = e.ID
		}
		ar.bytes += int64(e.Size)
	}
	if ar.next == 0 {
		ar.next = 1
	}
	ar.ready = true
	return nil
}

// scan reads the header of every entry on disk, oldest first.
func (ar *Archive) scan() []Entry {
	ar.scans++
	names, _ := filepath.Glob(filepath.Join(ar.Dir, "ctx-*.txt"))
	var out []Entry
	for _, p := range names {
		id := strings.TrimSuffix(filepath.Base(p), ".txt")
		if seq(id) == 0 {
			continue
		}
		e, err := readHeader(p)
		if err != nil {
			continue
		}
		e.ID = id
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool { return seq(out[i].ID) < seq(out[j].ID) })
	return out
}

func seq(id string) int {
	n, err := strconv.Atoi(strings.TrimPrefix(id, "ctx-"))
	if err != nil || n <= 0 {
		return 0
	}
	return n
}

func readHeader(p string) (Entry, error) {
	f, err := os.Open(p)
	if err != nil {
		return Entry{}, err
	}
	defer f.Close()
	line, err := bufio.NewReader(f).ReadBytes('\n')
	if err != nil && len(line) == 0 {
		return Entry{}, err
	}
	var e Entry
	if err := json.Unmarshal(line, &e); err != nil {
		return Entry{}, err
	}
	return e, nil
}

// Put stores body and returns its id. Ids are allocated with exclusive
// file creation so several janetcode processes can share a directory.
func (ar *Archive) Put(toolUse, name, body string) (string, error) {
	return ar.PutSubject(toolUse, name, "", body)
}

// PutSubject is Put with the call's subject recorded in the header.
func (ar *Archive) PutSubject(toolUse, name, subject, body string) (string, error) {
	ar.mu.Lock()
	defer ar.mu.Unlock()
	if err := ar.load(); err != nil {
		return "", err
	}
	if id := ar.byUse[toolUse]; toolUse != "" && id != "" {
		return id, nil
	}
	hdr, _ := json.Marshal(Entry{ToolUse: toolUse, Name: name, Subject: subject, Size: len(body), Created: time.Now().UTC()})
	for attempt := 0; attempt < 1000; attempt++ {
		id := "ctx-" + strconv.Itoa(ar.next)
		ar.next++
		f, err := os.OpenFile(filepath.Join(ar.Dir, id+".txt"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if errors.Is(err, os.ErrExist) {
			continue
		}
		if err != nil {
			return "", err
		}
		w := bufio.NewWriter(f)
		w.Write(hdr)
		w.WriteByte('\n')
		w.WriteString(body)
		if err := w.Flush(); err != nil {
			f.Close()
			os.Remove(f.Name())
			return "", err
		}
		if err := f.Close(); err != nil {
			os.Remove(f.Name())
			return "", err
		}
		if toolUse != "" {
			ar.byUse[toolUse] = id
		}
		ar.bytes += int64(len(body))
		ar.evict()
		return id, nil
	}
	return "", errors.New("archive: could not allocate an id")
}

// evict drops the oldest entries until the archive fits ArchiveCap.
//
// The running byte count is checked first so the common case costs
// nothing: scanning means opening the header of every file in the
// directory, and doing that on every archived output made a long session
// quadratic in the number of entries it had spilled. Once the count says
// the cap could have been reached the directory is read for real, which
// is also how entries another process wrote are accounted for.
func (ar *Archive) evict() {
	if ar.bytes <= ArchiveCap {
		return
	}
	entries := ar.scan()
	total := int64(0)
	for _, e := range entries {
		total += int64(e.Size)
	}
	for _, e := range entries {
		if total <= ArchiveCap {
			break
		}
		if os.Remove(filepath.Join(ar.Dir, e.ID+".txt")) == nil {
			total -= int64(e.Size)
			delete(ar.byUse, e.ToolUse)
		}
	}
	ar.bytes = total
}

// IDFor returns the archive id holding the result of a tool call, or "".
func (ar *Archive) IDFor(toolUse string) string {
	ar.mu.Lock()
	defer ar.mu.Unlock()
	if ar.load() != nil {
		return ""
	}
	return ar.byUse[toolUse]
}

// Get returns an entry and its full body.
func (ar *Archive) Get(id string) (Entry, string, error) {
	if seq(id) == 0 {
		return Entry{}, "", fmt.Errorf("%w: %q", ErrArchiveMissing, id)
	}
	data, err := os.ReadFile(filepath.Join(ar.Dir, id+".txt"))
	if errors.Is(err, os.ErrNotExist) {
		return Entry{}, "", fmt.Errorf("%w: %s", ErrArchiveMissing, id)
	}
	if err != nil {
		return Entry{}, "", err
	}
	hdr, body, _ := strings.Cut(string(data), "\n")
	var e Entry
	if err := json.Unmarshal([]byte(hdr), &e); err != nil {
		return Entry{}, "", fmt.Errorf("%s: corrupt header: %w", id, err)
	}
	e.ID = id
	return e, body, nil
}

// List returns every entry, newest first.
func (ar *Archive) List() []Entry {
	ar.mu.Lock()
	defer ar.mu.Unlock()
	if ar.load() != nil {
		return nil
	}
	es := ar.scan()
	for i, j := 0, len(es)-1; i < j; i, j = i+1, j-1 {
		es[i], es[j] = es[j], es[i]
	}
	return es
}

// stash decides what the model sees of a tool result: short outputs go
// through untouched; long ones are archived and replaced by a preview of
// both ends that names the archive id. When the receipts consumer is on,
// a verified evidence-preserving receipt stands in the preview's place
// instead (same archive note, the quotes in it are byte-exact); any
// failure of that path leaves the preview exactly as it was.
func (a *Agent) stash(toolUse, name, subject, out string) string {
	budget := inlineBudget(name)
	if len(out) <= budget || name == (&ExpandOutput{}).Name() {
		return out
	}
	if a.Archive == nil {
		return textfmt.TruncateTo(out, budget)
	}
	id, err := a.Archive.PutSubject(toolUse, name, subject, out)
	if err != nil {
		return textfmt.TruncateTo(out, budget) + "\n[archive unavailable: " + err.Error() + "]"
	}
	a.logEvent(session.Event{Type: session.TypeSpillSave, Name: name, CallID: toolUse, Output: id, Tokens: len(out)})
	// The preview is untrusted text standing where a whole result was, so
	// it cannot be allowed to close or open one of the harness's own tags.
	return archiveNote(id, len(out), budget, outputDigest(out)) + tool.EscapeDelimiters(textfmt.TruncateTo(out, budget))
}

// inlineBudget is how much of a tool's output stays in the conversation;
// the rest goes to the archive. File contents are what the model edits
// against, so they get the most; listings and command output are usually
// skimmed for a few lines and the rest fetched on demand.
func inlineBudget(name string) int {
	switch name {
	case "bash", "job":
		return 12 * 1024
	case "grep", "glob", "list_dir":
		return 8 * 1024
	}
	return InlineOutput
}

func archiveNote(id string, size, shown int, digest string) string {
	return fmt.Sprintf("[output is %s (sha256 %s); about %s of the middle is omitted here. The full output is saved as %s: expand_output {\"id\":%q,\"grep\":\"<regexp>\"} finds lines in it, {\"id\":%q,\"offset\":<line>,\"limit\":<lines>} reads a slice.]\n", textfmt.Bytes(int64(size)), digest, textfmt.Bytes(int64(size-shown)), id, id, id)
}

// outputDigest identifies the output the stub stands for. Two calls that
// produced the same megabyte of output leave stubs that differ only in
// their archive id, so without this a reader — a person, a diff of two
// runs, the model itself — cannot tell "the same output again" from "a
// different output of the same size". Twelve hex digits is plenty to
// tell two results of one session apart and short enough to read.
//
// It deliberately plays no part in loop detection, which compares calls
// and not results: the identical call repeated often returns outputs
// that differ in a line count or a timestamp, and a model going nowhere
// is a model repeating the call, whatever comes back.
func outputDigest(out string) string {
	sum := sha256.Sum256([]byte(out))
	return hex.EncodeToString(sum[:])[:12]
}

// Drop removes one archived output and forgets the tool call it came
// from. It is how a conversation rewind (see rewind.go) takes the
// outputs of the turns it drops off the disk: the messages that named
// those ids are gone, so nothing can ask for them again, and leaving
// them would let a later expand_output hand the model the output of work
// that no longer exists. A missing entry is not an error — the archive
// is shared by a project's sessions and evicts on its own.
func (ar *Archive) Drop(id string) {
	if ar == nil || seq(id) == 0 {
		return
	}
	ar.mu.Lock()
	defer ar.mu.Unlock()
	if ar.load() != nil {
		return
	}
	p := filepath.Join(ar.Dir, id+".txt")
	e, herr := readHeader(p)
	if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
		return
	}
	if herr == nil {
		ar.bytes -= int64(e.Size)
		if ar.bytes < 0 {
			ar.bytes = 0
		}
	}
	for use, got := range ar.byUse {
		if got == id {
			delete(ar.byUse, use)
		}
	}
}
