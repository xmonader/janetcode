package janetref

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// Sessions as refs: a session's transcript, the evidence the harness kept
// about it and the probes it left in the probe memory are saved under
// SessionsPrefix+<id>, so they can be pushed with the branch and a
// reviewer can open the session behind a pull request — `janetcode
// --show` and `--what-if` read it like a local one once it is opened —
// and re-run its evidence (`janetcode --proof verify`).
//
// A record is held under MaxSessionBytes. The transcript is the only part
// that grows: when it does not fit, every long string in it — tool
// output, file contents in messages, base64 images — is clipped, harder
// each round, until it does. The structure (every event, every message,
// every call) is always kept whole.

// SessionsPrefix is where saved sessions live.
const SessionsPrefix = Prefix + "sessions/"

// MaxSessionBytes is the default bound on one saved session's files.
const MaxSessionBytes = 1 << 20

// Record file names.
const (
	TranscriptFile = "transcript.jsonl"
	EvidenceFile   = "evidence.json"
	SessionProbes  = "probes.json"
	SessionInfo    = "session.json"
)

// clipSteps are the string lengths the transcript is clipped to, in
// order, until it fits; 0 is "not clipped".
var clipSteps = []int{0, 16 << 10, 4 << 10, 1 << 10, 256}

// sessionIDRe is a session id as session.New makes it (and nothing a ref
// name or a path could be tricked with).
var sessionIDRe = regexp.MustCompile(`^[0-9A-Za-z][0-9A-Za-z._-]{0,79}$`)

// ValidID reports whether id can name a saved session.
func ValidID(id string) bool { return sessionIDRe.MatchString(id) && !strings.Contains(id, "..") }

// SessionInfoRecord is session.json: what a listing shows without reading
// the transcript.
type SessionInfoRecord struct {
	ID      string    `json:"id"`
	Title   string    `json:"title,omitempty"`
	Model   string    `json:"model,omitempty"`
	Created time.Time `json:"created,omitempty"`
	Saved   time.Time `json:"saved"`
	// Head is the commit the session started from, when it was recorded.
	Head string `json:"head,omitempty"`
	// Clipped is the length long strings in the transcript were clipped
	// to, 0 when it was saved whole; RawBytes its size before.
	Clipped  int `json:"clipped,omitempty"`
	RawBytes int `json:"raw_bytes"`
}

// SessionParts is what SaveSession records.
type SessionParts struct {
	// Transcript is the session's JSONL file as it is on disk.
	Transcript []byte
	// Evidence is any JSON the caller wants kept beside it (proof records
	// of the commits it made); nil for none.
	Evidence []byte
	// Probes are the probe-memory entries the session left; nil for none.
	Probes []StoredProbe
}

// SaveSession writes a session's record to its ref, replacing an earlier
// save of the same session (the previous one stays as the parent commit).
// It refuses rather than writing a record over max bytes (0 is
// MaxSessionBytes) that clipping could not bring under it.
func SaveSession(ctx context.Context, repo, id string, p SessionParts, max int) (SessionInfoRecord, error) {
	if !ValidID(id) {
		return SessionInfoRecord{}, fmt.Errorf("%q is not a session id", id)
	}
	if max <= 0 {
		max = MaxSessionBytes
	}
	info := infoFromTranscript(id, p.Transcript)
	info.Saved = time.Now().UTC()
	info.RawBytes = len(p.Transcript)
	files := map[string][]byte{}
	fixed := 0
	if len(p.Evidence) > 0 {
		files[EvidenceFile] = p.Evidence
		fixed += len(p.Evidence)
	}
	if len(p.Probes) > 0 {
		data, err := json.MarshalIndent(probeRecord{Version: probesVersion, Probes: p.Probes}, "", "  ")
		if err != nil {
			return info, err
		}
		files[SessionProbes] = data
		fixed += len(data)
	}
	transcript, clip, err := FitTranscript(p.Transcript, max-fixed-1024)
	if err != nil {
		return info, err
	}
	info.Clipped = clip
	files[TranscriptFile] = transcript
	infoData, _ := json.MarshalIndent(info, "", "  ")
	files[SessionInfo] = infoData
	ref := SessionsPrefix + id
	for range 3 {
		old, err := Tip(ctx, repo, ref)
		if err != nil {
			return info, err
		}
		if _, err = Write(ctx, repo, ref, old, "session "+id, files); err != ErrConflict {
			return info, err
		}
	}
	return info, ErrConflict
}

// FitTranscript returns the transcript, clipped as little as clipSteps
// allows to fit in budget bytes, and the clip length used (0: whole).
func FitTranscript(raw []byte, budget int) ([]byte, int, error) {
	for _, n := range clipSteps {
		out := raw
		if n > 0 {
			out = ClipTranscript(raw, n)
		}
		if len(out) <= budget {
			return out, n, nil
		}
	}
	return nil, 0, fmt.Errorf("the transcript is %d bytes and does not fit in %d even with every string clipped to %d bytes", len(raw), budget, clipSteps[len(clipSteps)-1])
}

// ClipTranscript clips every JSON string longer than n bytes in each line
// of a JSONL transcript, keeping its head and a marker. A line that is
// not JSON is kept as it is, clipped as text.
func ClipTranscript(raw []byte, n int) []byte {
	var out bytes.Buffer
	sc := bufio.NewScanner(bytes.NewReader(raw))
	sc.Buffer(make([]byte, 0, 1<<20), 256<<20)
	for sc.Scan() {
		line := sc.Bytes()
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var v any
		d := json.NewDecoder(bytes.NewReader(line))
		d.UseNumber()
		if err := d.Decode(&v); err != nil {
			out.WriteString(clipString(string(line), n))
			out.WriteByte('\n')
			continue
		}
		data, err := json.Marshal(clipValue(v, n))
		if err != nil {
			out.Write(line)
		} else {
			out.Write(data)
		}
		out.WriteByte('\n')
	}
	return out.Bytes()
}

func clipValue(v any, n int) any {
	switch x := v.(type) {
	case string:
		return clipString(x, n)
	case []any:
		for i := range x {
			x[i] = clipValue(x[i], n)
		}
		return x
	case map[string]any:
		for k, e := range x {
			x[k] = clipValue(e, n)
		}
		return x
	}
	return v
}

// clipString keeps the first n bytes of s (whole runes) and says how much
// went.
func clipString(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := n
	for cut > 0 && cut < len(s) && s[cut]&0xC0 == 0x80 {
		cut--
	}
	return s[:cut] + fmt.Sprintf("…[clipped %d bytes]", len(s)-cut)
}

// infoFromTranscript reads the meta line a session file starts with.
func infoFromTranscript(id string, raw []byte) SessionInfoRecord {
	info := SessionInfoRecord{ID: id}
	first, _, _ := bytes.Cut(raw, []byte("\n"))
	var line struct {
		Type string `json:"type"`
		Meta struct {
			Title   string    `json:"title"`
			Model   string    `json:"model"`
			Created time.Time `json:"created"`
			Start   struct {
				Head string `json:"head"`
			} `json:"start"`
		} `json:"meta"`
	}
	if json.Unmarshal(first, &line) == nil && line.Type == "meta" {
		info.Title, info.Model, info.Created, info.Head = line.Meta.Title, line.Meta.Model, line.Meta.Created, line.Meta.Start.Head
	}
	return info
}

// SavedSession is one entry of ListSessions.
type SavedSession struct {
	SessionInfoRecord
	Ref    string
	Commit string
	Bytes  int64 // the record's files, uncompressed
}

// ListSessions lists the saved sessions, newest save first.
func ListSessions(ctx context.Context, repo string) ([]SavedSession, error) {
	refs, err := Refs(ctx, repo, SessionsPrefix)
	if err != nil {
		return nil, err
	}
	var out []SavedSession
	for _, ref := range refs {
		tip, err := Tip(ctx, repo, ref)
		if err != nil || tip == "" {
			continue
		}
		s := SavedSession{Ref: ref, Commit: tip}
		s.ID = strings.TrimPrefix(ref, SessionsPrefix)
		if data, ok, _ := ReadAt(ctx, repo, tip, SessionInfo); ok {
			_ = json.Unmarshal(data, &s.SessionInfoRecord)
		}
		if files, err := Files(ctx, repo, tip); err == nil {
			for _, n := range files {
				s.Bytes += n
			}
		}
		out = append(out, s)
	}
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j].Saved.After(out[j-1].Saved); j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out, nil
}

// OpenSession writes a saved session's transcript into dir (the local
// sessions directory) as <id>.jsonl, so the tools that read local
// sessions read it. It never overwrites a session already there.
func OpenSession(ctx context.Context, repo, id, dir string) (string, error) {
	if !ValidID(id) {
		return "", fmt.Errorf("%q is not a session id", id)
	}
	data, ok, err := Read(ctx, repo, SessionsPrefix+id, TranscriptFile)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", fmt.Errorf("no saved session %s (janetcode --session-ref list; fetch with --session-ref fetch)", id)
	}
	path := filepath.Join(dir, id+".jsonl")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if os.IsExist(err) {
			return path, fmt.Errorf("a session %s is already here (%s); not overwriting it", id, path)
		}
		return "", err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return "", err
	}
	return path, f.Close()
}

// FetchSessions fetches remote's saved sessions (all, or the one named).
func FetchSessions(ctx context.Context, repo, remote, id string) error {
	if id != "" {
		if !ValidID(id) {
			return fmt.Errorf("%q is not a session id", id)
		}
		return Fetch(ctx, repo, remote, "+"+SessionsPrefix+id, SessionsPrefix+id)
	}
	return Fetch(ctx, repo, remote, "+"+SessionsPrefix+"*", SessionsPrefix+"*")
}
