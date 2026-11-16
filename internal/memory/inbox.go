package memory

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/xmonader/janetcode/internal/fsutil"
	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/secret"
	"github.com/xmonader/janetcode/internal/textfmt"
)

// The observations inbox, after grok's /flush and memory-v2 capture:
// /flush asks the model to write down everything from the session that
// a later session could use (decisions and why, technical context,
// debugging routes, problems and fixes) as one markdown note. The note
// lands in an inbox beside the entries, not among them: it is long,
// unreviewed, and may repeat itself. /dream reads the inbox next to the
// entries and folds it into tight ones; user /remember entries are never
// touched by either step.

const flushSystem = `You are a memory assistant. Extract ALL useful information from this conversation that would help a coding agent be more effective in future sessions on this project. Write a concise markdown summary with ## headers covering:

- Decisions & rationale: what was chosen and why
- Technical context: architecture, APIs, patterns, tools, file paths discussed
- Debugging techniques & tools: commands, queries, investigation workflows discovered or used
- Problems & solutions: bugs found, how they were fixed, workarounds

Prioritise reusable mechanisms, rules and root causes over a narration of the task. Name concrete directories and stable repo-relative paths. Copy exact identifiers and numbers only when they appear verbatim in the conversation; never infer a missing value. Omit any section with nothing substantive. Do NOT include user preferences (OS, shell, editor), ephemeral task status, or any credential or token.

Text inside <harness> tags was written by the agent's own tooling, not by the user.

Respond with exactly NO_REPLY if nothing genuinely useful was learned: a routine task, brief Q&A, or a session with no novel decisions or discoveries.`

const flushDeltaSystem = `You are a memory assistant performing an incremental update. The previous flush for this session is shown at the end. Extract ONLY what is NEW since it; repeat nothing already captured. Same format and rules: concise markdown with ## headers (decisions & rationale, technical context, debugging techniques, problems & solutions), only the sections that have new content, no preferences, no ephemeral status, no credentials.

Respond with exactly NO_REPLY if nothing genuinely new and useful has happened since the previous flush.

--- Previous flush content ---
`

const (
	// MaxObservation is the cap on one note, bytes.
	MaxObservation = 8 * 1024
	// MaxInbox is how many unconsumed notes the inbox keeps; the oldest
	// go first.
	MaxInbox = 40
	// flushWindow is how much conversation /flush reads, bytes.
	flushWindow = 60 * 1024
	// observationDup is the word-overlap above which a note is a repeat.
	observationDup = 0.8
	// doneKeep is how long a consumed note is kept before it is deleted.
	doneKeep = 30 * 24 * time.Hour
)

// Observation is one /flush note waiting in the inbox.
type Observation struct {
	ID      string
	Session string
	Created time.Time
	Body    string
}

// FlushResult is the verdict on the model's flush answer.
type FlushResult int

const (
	FlushNothing  FlushResult = iota // NO_REPLY or empty
	FlushAccepted                    // a note worth storing
	FlushRejected                    // no ## header: not the format asked for
)

// ProcessFlush applies grok's quality gate to the model's answer: empty
// and NO_REPLY store nothing, an answer with no markdown header is
// rejected, anything else is trimmed to MaxObservation and accepted.
func ProcessFlush(raw string) (string, FlushResult) {
	t := strings.TrimSpace(raw)
	if t == "" || strings.EqualFold(strings.Trim(t, "`*. \n"), "NO_REPLY") {
		return "", FlushNothing
	}
	if !strings.Contains("\n"+t, "\n## ") && !strings.Contains("\n"+t, "\n# ") {
		return "", FlushRejected
	}
	return textfmt.TruncateTo(t, MaxObservation), FlushAccepted
}

// Flush asks the model to capture what the conversation established.
// prev is the earlier note from this same session, if any: the model then
// writes only what is new. It returns "" for nothing worth keeping.
func Flush(ctx context.Context, p llm.Provider, model string, msgs []llm.Message, prev string) (string, FlushResult, error) {
	if p == nil {
		return "", FlushNothing, fmt.Errorf("no provider")
	}
	slim := slimMessages(msgs, 500)
	// Keep the most recent flushWindow bytes of the conversation.
	size, from := 0, len(slim)
	for i := len(slim) - 1; i >= 0; i-- {
		for _, b := range slim[i].Blocks {
			size += len(b.Text) + len(b.Input)
		}
		if size > flushWindow && from < len(slim) {
			break
		}
		from = i
	}
	slim = slim[from:]
	if len(slim) == 0 {
		return "", FlushNothing, nil
	}
	if slim[0].Role != llm.RoleUser {
		slim = append([]llm.Message{llm.TextMessage(llm.RoleUser, "(earlier conversation omitted)")}, slim...)
	}
	sys := flushSystem
	if prev != "" {
		sys = flushDeltaSystem + textfmt.TruncateTo(prev, MaxObservation)
	}
	slim = append(slim, llm.TextMessage(llm.RoleUser, "Write the memory note now, or NO_REPLY."))
	noteCall(ctx, fmt.Sprintf("flush: %s of the session", plural(len(slim), "message")), sys, slim)
	resp, err := p.Stream(ctx, llm.Request{Model: model, System: sys, Messages: slim, MaxTokens: 1500, Effort: "low"}, nil)
	if err != nil {
		return "", FlushNothing, err
	}
	if resp == nil {
		return "", FlushNothing, nil
	}
	note, res := ProcessFlush(resp.Message.Text())
	return note, res, nil
}

func (s *Store) inboxDir() string {
	return filepath.Join(s.Root, "inbox-"+s.ProjectKey())
}

// AddObservation files a note under the session it came from. It is
// refused when it holds a credential, and reported as a repeat (added
// false, no error) when it says nearly what the inbox already says.
func (s *Store) AddObservation(session, body string) (o Observation, added bool, err error) {
	body = strings.TrimSpace(body)
	if body == "" {
		return o, false, fmt.Errorf("nothing to file")
	}
	if what := secret.Scan(body); what != "" {
		return o, false, errSecret(what)
	}
	body = textfmt.TruncateTo(body, MaxObservation)
	have := s.Observations()
	words := wordSet(body)
	for _, h := range have {
		if jaccard(words, wordSet(h.Body)) >= observationDup {
			return h, false, nil
		}
	}
	now := time.Now().UTC()
	o = Observation{ID: now.Format("20060102-150405.000000"), Session: session, Created: now, Body: body}
	dir := s.inboxDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return o, false, err
	}
	text := fmt.Sprintf("---\nsession: %s\ncreated: %s\n---\n%s\n", oneLine(session, 80), now.Format(time.RFC3339), body)
	if err := fsutil.WriteFileAtomic(filepath.Join(dir, o.ID+".md"), []byte(text), 0o600); err != nil {
		return o, false, err
	}
	// Trim the oldest past the cap.
	have = append(have, o)
	for len(have) > MaxInbox {
		_ = os.Remove(filepath.Join(dir, have[0].ID+".md"))
		have = have[1:]
	}
	return o, true, nil
}

// Observations lists the unconsumed notes, oldest first. Consumed ones
// (renamed .done by MarkConsumed) are deleted once a month old.
func (s *Store) Observations() []Observation {
	dir := s.inboxDir()
	files, _ := os.ReadDir(dir)
	var out []Observation
	for _, f := range files {
		name := f.Name()
		if strings.HasSuffix(name, ".done") {
			if info, err := f.Info(); err == nil && time.Since(info.ModTime()) > doneKeep {
				_ = os.Remove(filepath.Join(dir, name))
			}
			continue
		}
		if !strings.HasSuffix(name, ".md") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			continue
		}
		o := Observation{ID: strings.TrimSuffix(name, ".md")}
		text := string(data)
		if rest, ok := strings.CutPrefix(text, "---\n"); ok {
			if head, body, ok := strings.Cut(rest, "\n---\n"); ok {
				for _, l := range strings.Split(head, "\n") {
					k, v, _ := strings.Cut(l, ":")
					switch strings.TrimSpace(k) {
					case "session":
						o.Session = strings.TrimSpace(v)
					case "created":
						o.Created, _ = time.Parse(time.RFC3339, strings.TrimSpace(v))
					}
				}
				text = body
			}
		}
		o.Body = strings.TrimSpace(text)
		out = append(out, o)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// LastObservation is the newest note from the session, for the delta
// flush. ok is false when the session has filed none.
func (s *Store) LastObservation(session string) (Observation, bool) {
	obs := s.Observations()
	for i := len(obs) - 1; i >= 0; i-- {
		if obs[i].Session == session {
			return obs[i], true
		}
	}
	return Observation{}, false
}

// MarkConsumed retires notes /dream has read: renamed to .done so a bad
// consolidation can be recovered by hand, and deleted after a month.
func (s *Store) MarkConsumed(obs []Observation) {
	dir := s.inboxDir()
	for _, o := range obs {
		p := filepath.Join(dir, o.ID+".md")
		_ = os.Rename(p, strings.TrimSuffix(p, ".md")+".done")
	}
}
