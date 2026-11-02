package proof

// Signed evidence: the proof note (proof.go) says what a session did and
// what the harness saw demonstrating it, in a form a person reads. The
// evidence note here says the same thing in a form a machine re-checks:
// an in-toto Statement v1 whose subject is the commit's tree, carrying
// every verification command the contributing turns ran with its exit
// code and a hash of its normalised output, the model that wrote the work
// (with what the serving-model provenance check said about it), the
// prompt and system prompt hashes, the test files' content before the
// commit, the sandbox profile commands ran under, and who approved each
// gated mutation. It is wrapped in a DSSE envelope signed with a local
// ed25519 key (see dsse.go) and stored as a git note under EvidenceRef.
// `janetcode --proof verify <commit>` (reexec.go) checks the signature,
// re-runs the recorded commands in a throwaway checkout of the commit
// under the recorded sandbox profile, and diffs the outcomes.
//
// The evidence is a product artefact for a reviewer, not something the
// model sees: nothing here reaches a prompt.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/project"
	"github.com/xmonader/janetcode/internal/session"
	"github.com/xmonader/janetcode/internal/shell"
)

// EvidenceRef is where signed evidence envelopes are stored, as git
// notes. It is separate from NotesRef so the human-readable proof note
// keeps its shape and a repository can carry one without the other.
const EvidenceRef = "refs/notes/janetcode-evidence"

// The in-toto and DSSE identifiers. StatementType and PayloadType are the
// in-toto attestation framework's own; PredicateType is janetcode's, and
// its version suffix moves when the predicate's meaning does.
const (
	StatementType = "https://in-toto.io/Statement/v1"
	PredicateType = "https://janetcode.dev/change-evidence/v1"
	PayloadType   = "application/vnd.in-toto+json"
)

// SubjectName names the one subject an evidence statement has. The digest
// is what identifies it; the name only says what kind of thing it is.
const SubjectName = "git-tree"

// Statement is an in-toto Statement v1 with janetcode's change-evidence
// predicate.
type Statement struct {
	Type          string    `json:"_type"`
	Subject       []Subject `json:"subject"`
	PredicateType string    `json:"predicateType"`
	Predicate     Evidence  `json:"predicate"`
}

// Subject is an in-toto ResourceDescriptor: here always the commit's tree,
// as {"gitTree": <sha>}. The tree rather than the commit, because the
// tree is what the evidence is about: an amend that only rewords the
// message keeps it, and one that changes a file does not.
type Subject struct {
	Name   string            `json:"name"`
	Digest map[string]string `json:"digest"`
}

// Evidence is the predicate: everything recorded about how the commit's
// content came to be and what demonstrated it.
type Evidence struct {
	Version int `json:"version"`
	// Commit is the commit the statement was made for, for display; the
	// signed claim is the subject's tree.
	Commit  string    `json:"commit"`
	Created time.Time `json:"created"`
	// TestsBefore is the sha256 of every committed test file as it was in
	// the commit's parent ("absent" for a test the commit added), so a
	// reviewer can tell a suite that was green before from one rewritten
	// to be green.
	TestsBefore []FileDigest `json:"tests_before,omitempty"`
	// Sessions is each session's contribution, one per session id; a
	// second session attaching to the same commit adds its own entry.
	Sessions []SessionEvidence `json:"sessions"`
	// Extensions carries fields later mechanisms add without a predicate
	// version bump: an unknown key is kept through a merge and ignored by
	// verify. "integrity" is reserved for the test-integrity ledger's
	// counts (assertions modified, tests added, pre-image replays).
	Extensions map[string]json.RawMessage `json:"extensions,omitempty"`
}

// EvidenceVersion is the current predicate schema version.
const EvidenceVersion = 1

// FileDigest is one file's content hash: "sha256:<hex>", or "absent".
type FileDigest struct {
	Path   string `json:"path"`
	Digest string `json:"digest"`
}

// SessionEvidence is one session's part of a commit's evidence.
type SessionEvidence struct {
	Session string `json:"session"`
	// Workdir is where the session's commands ran, relative to the
	// repository's top level ("." for the top itself); verify runs them
	// in the same place inside its checkout.
	Workdir            string          `json:"workdir"`
	Model              ModelEvidence   `json:"model"`
	SystemPromptSHA256 string          `json:"system_prompt_sha256,omitempty"`
	Sandbox            SandboxEvidence `json:"sandbox"`
	Turns              []TurnEvidence  `json:"turns"`
	// Extensions: see Evidence.Extensions.
	Extensions map[string]json.RawMessage `json:"extensions,omitempty"`
}

// ModelEvidence is which model wrote the work and what the responses
// said about who served them.
type ModelEvidence struct {
	Requested string `json:"requested"`
	// Calls are the distinct (model, provider, served) combinations the
	// contributing turns' model calls reported, with how many calls each.
	Calls []ModelCall `json:"calls,omitempty"`
	// Provenance is internal/provenance's verdict folded over those
	// calls: "match" when some response said who served it and none named
	// another vendor, "mismatch" when one did, "unknown" when no response
	// said anything.
	Provenance string   `json:"provenance"`
	Mismatches []string `json:"mismatches,omitempty"`
}

// ModelCall is one combination of the model asked and what served it.
type ModelCall struct {
	Model    string          `json:"model"`
	Provider string          `json:"provider,omitempty"`
	Served   *llm.Provenance `json:"served,omitempty"`
	Count    int             `json:"count"`
}

// SandboxEvidence is the sandbox the session's shell ran under when the
// evidence was made. Profile is "none" when the sandbox was off. SHA256
// hashes the profile's definition and the backend, so verify can say
// when the profile of that name no longer means what it meant.
type SandboxEvidence struct {
	Profile string `json:"profile"`
	Backend string `json:"backend,omitempty"`
	SHA256  string `json:"sha256,omitempty"`
}

// NoSandbox is SandboxEvidence.Profile and CommandEvidence.Sandbox for a
// command that ran outside any sandbox.
const NoSandbox = "none"

// TurnEvidence is one contributing turn.
type TurnEvidence struct {
	Turn int `json:"turn"`
	// PromptSHA256 hashes the user's messages of the turn, in order.
	PromptSHA256 string `json:"prompt_sha256,omitempty"`
	Verdict      string `json:"verdict,omitempty"`
	// Claimed is what the model said about verification, when it said
	// anything a check could have shown (internal/agent's
	// TurnOutcome.Claimed). With no command recorded for the turn it is
	// an unverified assertion.
	Claimed   string            `json:"claimed,omitempty"`
	Commands  []CommandEvidence `json:"commands,omitempty"`
	Approvals []Approval        `json:"approvals,omitempty"`
}

// CommandEvidence is the latest run, in its turn, of one verification
// command.
type CommandEvidence struct {
	Command string `json:"command"`
	// Source is who ran it: "bash" for the model's own call, or the gate
	// (session.GateVerify, session.GateAccept).
	Source string `json:"source"`
	// Exit is the exit code; ExitUnknownFailure when the run failed and
	// the record does not say with what.
	Exit int `json:"exit"`
	// Output is OutputDigest of what it printed, "" when nothing was
	// kept. OutputPartial says the kept output was cut, so it cannot be
	// compared byte for byte with a re-run.
	Output        string `json:"output,omitempty"`
	OutputPartial bool   `json:"output_partial,omitempty"`
	// Sandbox is the profile it ran under, NoSandbox for none (a gate
	// runs outside the sandbox).
	Sandbox string `json:"sandbox"`
	// Stale is set when the turn changed a file after this run: it ran
	// against an earlier tree than the committed one, so a re-run may
	// legitimately differ.
	Stale bool `json:"stale,omitempty"`
	// Withheld marks a command whose text is not recorded — a held-out
	// acceptance gate's script, whose path is what a run must not see —
	// so it cannot be re-run.
	Withheld bool `json:"withheld,omitempty"`
}

// ExitUnknownFailure is CommandEvidence.Exit for a failed run whose exit
// code was not recorded; it matches any non-zero code.
const ExitUnknownFailure = -1

// Approval is how one call that changed a committed file was authorized:
// the basis the permission record gives ("rule edit(src/**)", "mode:
// auto-edits", "the user: allow", ...).
type Approval struct {
	Call  string   `json:"call"`
	Tool  string   `json:"tool"`
	Paths []string `json:"paths"`
	Basis string   `json:"basis"`
}

// EvidenceInput is what the caller (internal/agent) hands over to build a
// session's evidence: the facts that live in the session, not in git.
type EvidenceInput struct {
	Session      string
	Model        string
	SystemPrompt string
	// RepoRoot is the repository's top level; WorkRoot is where the
	// session's commands ran (the workspace root, which may be below it).
	RepoRoot, WorkRoot string
	Events             []session.Event
	Sandbox            SandboxEvidence
	// Claims maps a turn to what the model claimed about verification.
	Claims map[int]string
}

// GateRun is the Extra a turn-end gate writes on its session.TypeGate
// events (see internal/agent's logGate): the exit code and output digest
// of one command it ran. A gate event without it — recorded before this
// existed — still counts, with the exit inferred from IsError and no
// output to compare.
type GateRun struct {
	Exit          int    `json:"exit"`
	Output        string `json:"out,omitempty"`
	OutputPartial bool   `json:"partial,omitempty"`
}

// BuildEvidence assembles the statement for commit (whose tree is tree)
// from one session's record r — already limited by Build to what landed
// in the commit — the committed files' versions, and the session's
// events. It does no I/O.
func BuildEvidence(commit, tree string, r Record, versions map[string]FileVersions, in EvidenceInput) Statement {
	ev := Evidence{
		Version: EvidenceVersion, Commit: commit, Created: time.Now().UTC(),
		TestsBefore: testsBefore(versions),
	}
	ev.Sessions = []SessionEvidence{SessionFromEvents(r, in)}
	return Statement{
		Type:          StatementType,
		Subject:       []Subject{{Name: SubjectName, Digest: map[string]string{"gitTree": tree}}},
		PredicateType: PredicateType,
		Predicate:     ev,
	}
}

// SessionFromEvents folds one session's events into its evidence, for the
// turns r says contributed to the commit.
func SessionFromEvents(r Record, in EvidenceInput) SessionEvidence {
	workdir := "."
	if in.RepoRoot != "" && in.WorkRoot != "" {
		if rel, err := filepath.Rel(in.RepoRoot, in.WorkRoot); err == nil && !strings.HasPrefix(rel, "..") {
			workdir = filepath.ToSlash(rel)
		}
	}
	committed := map[string]bool{}
	for _, f := range r.Files {
		committed[f.Path] = true
	}
	verdicts := map[int]string{}
	var turns []int
	for _, t := range r.Turns {
		verdicts[t.Turn] = t.Verdict
	}
	seen := map[int]bool{}
	for _, f := range r.Files {
		for _, c := range f.Changes {
			if !seen[c.Turn] {
				seen[c.Turn] = true
				turns = append(turns, c.Turn)
			}
		}
	}
	sort.Ints(turns)
	se := SessionEvidence{
		Session: in.Session, Workdir: workdir,
		Model:   foldModel(in.Events, seen, in.Model),
		Sandbox: in.Sandbox,
	}
	if se.Sandbox.Profile == "" {
		se.Sandbox.Profile = NoSandbox
	}
	if in.SystemPrompt != "" {
		se.SystemPromptSHA256 = sha256Hex([]byte(in.SystemPrompt))
	}
	for _, tn := range turns {
		te := TurnEvidence{
			Turn: tn, Verdict: verdicts[tn], Claimed: in.Claims[tn],
			PromptSHA256: promptHash(in.Events, tn),
			Commands:     foldCommands(in.Events, tn, in.WorkRoot, se.Sandbox.Profile),
			Approvals:    foldApprovals(in.Events, tn, in.RepoRoot, committed),
		}
		se.Turns = append(se.Turns, te)
	}
	return se
}

// testsBefore hashes every committed test file's content in the commit's
// parent.
func testsBefore(versions map[string]FileVersions) []FileDigest {
	var out []FileDigest
	for p, fv := range versions {
		if !project.IsTestFile(p) {
			continue
		}
		d := "absent"
		if fv.Parent != nil {
			d = "sha256:" + sha256Hex(fv.Parent)
		}
		out = append(out, FileDigest{Path: p, Digest: d})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

// promptHash hashes the user messages of turn, in log order; "" when the
// turn logged none.
func promptHash(events []session.Event, turn int) string {
	h := sha256.New()
	n := 0
	for _, e := range events {
		if e.Type != session.TypeUserMessage || e.Turn != turn || e.Message == nil {
			continue
		}
		if n > 0 {
			h.Write([]byte{0})
		}
		h.Write([]byte(e.Message.Text()))
		n++
	}
	if n == 0 {
		return ""
	}
	return hex.EncodeToString(h.Sum(nil))
}

// foldModel collects the model calls of the contributing turns. A
// response's Served is logged only when it changes, so the last one seen
// carries forward to the calls after it.
func foldModel(events []session.Event, turns map[int]bool, requested string) ModelEvidence {
	me := ModelEvidence{Requested: requested, Provenance: "unknown"}
	type key struct{ model, provider, served string }
	idx := map[key]int{}
	var served *llm.Provenance
	sawServed := false
	mism := map[string]bool{}
	for _, e := range events {
		if e.Type != session.TypeAssistant {
			continue
		}
		if e.Served != nil {
			s := *e.Served
			served = &s
		}
		if !turns[e.Turn] {
			continue
		}
		model := e.Model
		if model == "" {
			model = requested
		}
		var sk string
		if served != nil {
			sawServed = true
			b, _ := json.Marshal(served)
			sk = string(b)
		}
		k := key{model, e.Provider, sk}
		if i, ok := idx[k]; ok {
			me.Calls[i].Count++
		} else {
			idx[k] = len(me.Calls)
			mc := ModelCall{Model: model, Provider: e.Provider, Count: 1}
			if served != nil {
				s := *served
				mc.Served = &s
			}
			me.Calls = append(me.Calls, mc)
		}
		if e.Mismatch != "" && !mism[e.Mismatch] {
			mism[e.Mismatch] = true
			me.Mismatches = append(me.Mismatches, e.Mismatch)
		}
	}
	switch {
	case len(me.Mismatches) > 0:
		me.Provenance = "mismatch"
	case sawServed:
		me.Provenance = "match"
	}
	return me
}

// exitLineRe is the bash tool's "[exit code N]" line (internal/tool's
// exitNote).
var exitLineRe = regexp.MustCompile(`\[exit code (-?\d+)\]`)

// bashExit reads a bash result's exit code: the last "[exit code N]" line,
// else 0, or ExitUnknownFailure for a result the tool marked an error.
func bashExit(e session.Event) int {
	if m := exitLineRe.FindAllStringSubmatch(e.Output, -1); len(m) > 0 {
		if n, err := strconv.Atoi(m[len(m)-1][1]); err == nil {
			return n
		}
	}
	if e.IsError {
		return ExitUnknownFailure
	}
	return 0
}

// notFoundRe matches a shell saying the command itself is missing: a run
// that never started is not a check (as in internal/agent's neverRan).
var notFoundRe = regexp.MustCompile(`(?im)(command not found|: not found)\s*$`)

// editToolNames are the file tools (internal/agent's editTool).
var editToolNames = map[string]bool{"write_file": true, "edit_file": true, "notebook_edit": true, "ast_edit": true}

// foldCommands keeps the latest run in turn of each verification command:
// the model's bash checks (shell.IsCheck) and every command a turn-end
// gate ran. A refused call never ran and is not recorded.
func foldCommands(events []session.Event, turn int, root, sandbox string) []CommandEvidence {
	latest := map[string]int{} // command -> index into runs
	var runs []CommandEvidence
	var seqs []int
	lastEdit := 0
	put := func(c CommandEvidence, seq int) {
		if i, ok := latest[c.Command]; ok {
			runs[i] = c
			seqs[i] = seq
			return
		}
		latest[c.Command] = len(runs)
		runs = append(runs, c)
		seqs = append(seqs, seq)
	}
	for _, e := range events {
		if e.Turn != turn {
			continue
		}
		switch e.Type {
		case session.TypeFileChange:
			lastEdit = e.Seq
		case session.TypeGate:
			c := CommandEvidence{
				Command: shell.TrimWorkspaceCd(strings.TrimSpace(e.Name), root),
				Source:  e.Source, Sandbox: NoSandbox, Exit: 0,
			}
			if c.Source == "" {
				c.Source = session.GateVerify
			}
			var gr GateRun
			if len(e.Extra) > 0 && json.Unmarshal(e.Extra, &gr) == nil {
				c.Exit, c.Output, c.OutputPartial = gr.Exit, gr.Output, gr.OutputPartial
			} else if e.IsError {
				c.Exit = ExitUnknownFailure
				if e.Output != "" {
					c.Output, c.OutputPartial = OutputDigest(e.Output, root)
				}
			}
			if c.Source == session.GateAccept {
				// The accept gate's reported name is a label, not the
				// script that ran (see verify.Runner.Display).
				c.Withheld = true
			}
			put(c, e.Seq)
		case session.TypeToolResult:
			if editToolNames[e.Name] && !e.IsError && !e.Denied {
				// A session recorded before file/change events existed
				// says it edited only here.
				lastEdit = max(lastEdit, e.Seq)
				continue
			}
			if e.Name != "bash" || e.CallID == "" || e.Denied {
				continue
			}
			cmd := shell.TrimWorkspaceCd(shell.CommandOf(e.Arguments), root)
			if !shell.IsCheck(cmd) {
				continue
			}
			exit := bashExit(e)
			if exit == 127 && notFoundRe.MatchString(e.Output) {
				continue
			}
			c := CommandEvidence{Command: cmd, Source: "bash", Exit: exit, Sandbox: sandbox}
			c.Output, c.OutputPartial = OutputDigest(e.Output, root)
			put(c, e.Seq)
		}
	}
	for i := range runs {
		runs[i].Stale = lastEdit > seqs[i]
	}
	return runs
}

// foldApprovals pairs each permission record of turn with the files its
// call changed, keeping the calls that changed a committed file.
func foldApprovals(events []session.Event, turn int, repoRoot string, committed map[string]bool) []Approval {
	paths := map[string][]string{}
	for _, e := range events {
		if e.Type != session.TypeFileChange || e.CallID == "" {
			continue
		}
		p := normPath(repoRoot, e.Name)
		if committed[p] && !containsStr(paths[e.CallID], p) {
			paths[e.CallID] = append(paths[e.CallID], p)
		}
	}
	var out []Approval
	for _, e := range events {
		if e.Type != session.TypePermission || e.Turn != turn || e.Denied {
			continue
		}
		ps := paths[e.CallID]
		if len(ps) == 0 {
			continue
		}
		sort.Strings(ps)
		out = append(out, Approval{Call: e.CallID, Tool: e.Name, Paths: ps, Basis: e.Reason})
	}
	return out
}

func containsStr(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

// SandboxDigest hashes a sandbox profile's definition with the backend
// that enforces it.
func SandboxDigest(p shell.Profile, backend string) string {
	b, _ := json.Marshal(struct {
		Profile shell.Profile `json:"profile"`
		Backend string        `json:"backend"`
	}{p, backend})
	return sha256Hex(b)
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// mergeEvidence folds incoming into existing for the same commit: a
// session's entry replaces that session's earlier one (a turn-end refresh
// re-attaches with the gates' runs added), other sessions' entries stay,
// and extensions are unioned with incoming winning a key both carry.
func mergeEvidence(existing, incoming Statement) Statement {
	out := incoming
	byID := map[string]int{}
	var sessions []SessionEvidence
	for _, s := range existing.Predicate.Sessions {
		byID[s.Session] = len(sessions)
		sessions = append(sessions, s)
	}
	for _, s := range incoming.Predicate.Sessions {
		if i, ok := byID[s.Session]; ok {
			sessions[i] = s
			continue
		}
		byID[s.Session] = len(sessions)
		sessions = append(sessions, s)
	}
	out.Predicate.Sessions = sessions
	if !existing.Predicate.Created.IsZero() {
		out.Predicate.Created = existing.Predicate.Created
	}
	if len(existing.Predicate.Extensions) > 0 {
		ext := map[string]json.RawMessage{}
		for k, v := range existing.Predicate.Extensions {
			ext[k] = v
		}
		for k, v := range incoming.Predicate.Extensions {
			ext[k] = v
		}
		out.Predicate.Extensions = ext
	}
	return out
}

// subjectTree is the statement's gitTree digest, or "".
func (s Statement) subjectTree() string {
	for _, sub := range s.Subject {
		if t := sub.Digest["gitTree"]; t != "" {
			return t
		}
	}
	return ""
}

// EvidenceSummary is one line about a statement, for the /proof notice.
func EvidenceSummary(s Statement) string {
	n := 0
	for _, se := range s.Predicate.Sessions {
		for _, t := range se.Turns {
			n += len(t.Commands)
		}
	}
	return fmt.Sprintf("evidence · tree %s · %d command(s) recorded", Short(s.subjectTree()), n)
}
