package session

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strconv"
	"strings"
)

// The audit log. Every event the session appends — each tool call and
// its result, each permission decision, each file a tool changed, each
// model call with its model, provider, usage and route — also gets one
// record in <id>.audit beside the session, and each record carries
// the hash of the one before it. Changing, removing or inserting an event
// in the session file after it was written, or editing the audit file
// itself, breaks the chain or the match between the two, and VerifyAudit
// says where. Both files live in janetcode's data directory, which the
// file tools and the command guard already treat as janetcode's own (see
// tool.SelfPath): a model has to ask to touch them.
//
// A record costs one sha256 of the event and one small append, next to
// the append the event itself already makes.
//
// What a hash chain cannot show on its own is a tail cut off both files
// together; the head hash is printed by --audit so it can be kept
// elsewhere (a commit's proof note, a ticket) and compared later.

// AuditBegin is the first record of every audit log. From is the first
// event seq the log covers: a session recorded before the audit log
// existed has events before it that nothing vouches for.
const AuditBegin = "audit/begin"

// AuditRecord is one line of the audit log.
type AuditRecord struct {
	N      int    `json:"n"`
	Type   string `json:"t"`
	Seq    int    `json:"seq,omitempty"`
	Digest string `json:"digest,omitempty"`
	From   int    `json:"from,omitempty"`
	Prev   string `json:"prev"`
	Hash   string `json:"hash"`
}

// chain is the record's hash: over every field but Hash, and the previous
// record's hash, so no record can be changed without every later one.
func (r AuditRecord) chain() string {
	h := sha256.New()
	fmt.Fprintf(h, "%d\x00%s\x00%d\x00%s\x00%d\x00%s", r.N, r.Type, r.Seq, r.Digest, r.From, r.Prev)
	return hex.EncodeToString(h.Sum(nil))
}

// AuditPath is the audit log of the session file at sessionPath.
func AuditPath(sessionPath string) string { return sidecar(sessionPath, ".audit") }

// eventDigest is what an audit record says an event was: the sha256 of
// its line as the session writes it. The verifier recomputes it from the
// parsed event, so a rewrite of the session file (a new title, a pin)
// keeps every digest.
func eventDigest(e Event) (string, error) {
	b, err := json.Marshal(eventLine{Type: "event", Event: e})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}

// auditLocked records every event in s.ev the audit log does not cover
// yet. The caller holds s.mu and has already written the events to the
// session file. A failure is returned but the event stands: the session
// is the record of what happened, and VerifyAudit reports the gap.
func (s *Session) auditLocked() error {
	if !s.auditLoaded {
		if err := s.loadAuditLocked(); err != nil {
			return err
		}
	}
	if s.audited >= len(s.ev) && s.auditN > 0 {
		return nil
	}
	var buf bytes.Buffer
	add := func(r AuditRecord) error {
		r.N = s.auditN + 1
		r.Prev = s.auditHead
		r.Hash = r.chain()
		b, err := json.Marshal(r)
		if err != nil {
			return err
		}
		buf.Write(b)
		buf.WriteByte('\n')
		s.auditN, s.auditHead = r.N, r.Hash
		return nil
	}
	if s.auditN == 0 {
		from := s.nextSeq + 1
		if s.audited < len(s.ev) {
			from = s.ev[s.audited].Seq
		}
		if err := add(AuditRecord{Type: AuditBegin, From: from}); err != nil {
			return err
		}
	}
	for ; s.audited < len(s.ev); s.audited++ {
		e := s.ev[s.audited]
		d := s.lastDigest
		if s.audited != len(s.ev)-1 || d == "" {
			var err error
			if d, err = eventDigest(e); err != nil {
				return err
			}
		}
		if err := add(AuditRecord{Type: e.Type, Seq: e.Seq, Digest: d}); err != nil {
			return err
		}
	}
	f, err := os.OpenFile(AuditPath(s.path), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(buf.Bytes()); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// loadAuditLocked picks the chain up where the file on disk left it. A
// session with no audit log starts one at the events this process adds:
// the ones Open read were written before, by a janetcode that kept none.
func (s *Session) loadAuditLocked() error {
	s.auditLoaded = true
	b, err := os.ReadFile(AuditPath(s.path))
	if errors.Is(err, fs.ErrNotExist) {
		s.audited = s.opened
		return nil
	}
	if err != nil {
		return err
	}
	// A record cut short by a crash is dropped before the next one is
	// appended, or the two would be glued into one unreadable line.
	if n := len(b); n > 0 && b[n-1] != '\n' {
		keep := bytes.LastIndexByte(b, '\n') + 1
		if err := os.Truncate(AuditPath(s.path), int64(keep)); err != nil {
			return err
		}
		b = b[:keep]
	}
	lastSeq := 0
	for _, line := range bytes.Split(b, []byte("\n")) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var r AuditRecord
		if json.Unmarshal(line, &r) != nil {
			continue
		}
		s.auditN, s.auditHead = r.N, r.Hash
		if r.Type != AuditBegin && r.Seq > lastSeq {
			lastSeq = r.Seq
		}
	}
	s.audited = len(s.ev)
	for i, e := range s.ev {
		if e.Seq > lastSeq {
			s.audited = i
			break
		}
	}
	return nil
}

// AuditReport is what VerifyAudit found.
type AuditReport struct {
	// Records is how many records the log holds; Head is the last one's
	// hash.
	Records int
	Head    string
	// From is the first event seq the log covers, and Before how many
	// events of the session predate it.
	From   int
	Before int
	// Events is how many of the session's events the log vouches for.
	Events int
	// Problems is every break found, in order; none means the session
	// file and its audit log agree record for record.
	Problems []string
}

// OK reports whether the chain and the session agree.
func (r AuditReport) OK() bool { return len(r.Problems) == 0 }

// VerifyAudit checks the audit log of the session file at sessionPath:
// every record chains to the one before, and every event the session
// holds from the log's start on has a record with the same digest, in the
// same order, with none left over.
func VerifyAudit(sessionPath string) (AuditReport, error) {
	var rep AuditReport
	s, err := readSessionFile(sessionPath)
	if err != nil {
		return rep, err
	}
	f, err := os.Open(AuditPath(sessionPath))
	if errors.Is(err, fs.ErrNotExist) {
		rep.Before = len(s.ev)
		rep.Problems = append(rep.Problems, "no audit log: the session was recorded without one")
		return rep, nil
	}
	if err != nil {
		return rep, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 4096), 1<<20)
	prev := ""
	digests := map[int]string{}
	var order []int
	line := 0
	for sc.Scan() {
		line++
		raw := bytes.TrimSpace(sc.Bytes())
		if len(raw) == 0 {
			continue
		}
		var r AuditRecord
		if err := json.Unmarshal(raw, &r); err != nil {
			rep.Problems = append(rep.Problems, fmt.Sprintf("audit line %d is not a record: %v", line, err))
			continue
		}
		rep.Records++
		switch {
		case r.N != rep.Records:
			rep.Problems = append(rep.Problems, fmt.Sprintf("audit line %d is record %d, expected %d: a record was removed or inserted", line, r.N, rep.Records))
		case r.Prev != prev:
			rep.Problems = append(rep.Problems, fmt.Sprintf("record %d does not follow record %d: the chain was cut", r.N, r.N-1))
		case r.Hash != r.chain():
			rep.Problems = append(rep.Problems, fmt.Sprintf("record %d was edited after it was written", r.N))
		}
		prev, rep.Head = r.Hash, r.Hash
		if r.Type == AuditBegin {
			if rep.Records == 1 {
				rep.From = r.From
			}
			continue
		}
		if _, dup := digests[r.Seq]; dup {
			rep.Problems = append(rep.Problems, fmt.Sprintf("event seq %d has two audit records", r.Seq))
		}
		digests[r.Seq] = r.Digest
		order = append(order, r.Seq)
	}
	if err := sc.Err(); err != nil {
		return rep, err
	}
	seen := map[int]bool{}
	for _, e := range s.ev {
		if e.Seq < rep.From {
			rep.Before++
			continue
		}
		seen[e.Seq] = true
		want, ok := digests[e.Seq]
		if !ok {
			rep.Problems = append(rep.Problems, fmt.Sprintf("event seq %d (%s) has no audit record: it was added to the session afterwards", e.Seq, e.Type))
			continue
		}
		got, err := eventDigest(e)
		if err != nil {
			return rep, err
		}
		if got != want {
			rep.Problems = append(rep.Problems, fmt.Sprintf("event seq %d (%s) was changed after it was written", e.Seq, e.Type))
			continue
		}
		rep.Events++
	}
	for _, seq := range order {
		if !seen[seq] {
			rep.Problems = append(rep.Problems, "event seq "+strconv.Itoa(seq)+" is in the audit log but no longer in the session: it was removed")
		}
	}
	return rep, nil
}

// readSessionFile reads a session file by path, whatever its id.
func readSessionFile(path string) (*Session, error) {
	id := strings.TrimSuffix(pathBase(path), ".jsonl")
	s, _, _, err := open(path, id, nil)
	return s, err
}

func pathBase(p string) string {
	if i := strings.LastIndexAny(p, `/\`); i >= 0 {
		return p[i+1:]
	}
	return p
}
