package proof

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/xmonader/janetcode/internal/gitx"
	"github.com/xmonader/janetcode/internal/secret"
	"github.com/xmonader/janetcode/internal/shell"
	"github.com/xmonader/janetcode/internal/textfmt"
)

// ClaimStatus is what verify concluded about one claim.
type ClaimStatus string

const (
	// ClaimReproduced: the re-run exited the same and printed the same
	// normalised output.
	ClaimReproduced ClaimStatus = "reproduced"
	// ClaimExitOnly: the re-run exited the same; the recorded output was
	// cut or not kept, so there was nothing to compare it with.
	ClaimExitOnly ClaimStatus = "exit reproduced"
	// ClaimOutputDiffers: same exit, different normalised output.
	ClaimOutputDiffers ClaimStatus = "output differs"
	// ClaimDiverged: the re-run exited differently — a pass that now
	// fails, or the other way round.
	ClaimDiverged ClaimStatus = "diverged"
	// ClaimNotRun: recorded, but not re-run (a withheld command, a
	// sandbox this machine cannot provide, a static check).
	ClaimNotRun ClaimStatus = "not re-run"
	// ClaimUnverified: a claim with no recorded run behind it — the model
	// said it verified, or a proof note lists a check, and the evidence
	// has no command to re-run.
	ClaimUnverified ClaimStatus = "unverified assertion"
)

// Claim is one thing the evidence asserts, and what verify found.
type Claim struct {
	Session string
	Turn    int
	// What is the claim in words: the command, or the model's own claim.
	What   string
	Status ClaimStatus
	// Detail says why, in a few words ("exit 0, now 1", "sandbox strict
	// unavailable: ...").
	Detail string
	Stale  bool
}

// Report is the outcome of verifying one commit's evidence.
type Report struct {
	Commit    string
	Signature SigStatus
	KeyID     string
	// SubjectOK is whether the statement's tree is the commit's tree.
	SubjectOK bool
	// Tree is the commit's own tree; Subject the statement's.
	Tree, Subject string
	HasEvidence   bool
	HasProof      bool
	Claims        []Claim
	// Notes are things worth saying that are not claims (a sandbox
	// profile whose definition changed since the evidence was made).
	Notes []string
}

// Count is how many claims have status s.
func (r Report) Count(s ClaimStatus) int {
	n := 0
	for _, c := range r.Claims {
		if c.Status == s {
			n++
		}
	}
	return n
}

// OK reports whether the evidence holds: a valid signature over the
// commit's own tree, and no re-run that exited differently.
func (r Report) OK() bool {
	return r.HasEvidence && r.Signature == SigValid && r.SubjectOK && r.Count(ClaimDiverged) == 0
}

// Rate is the share of re-run claims that reproduced exactly (exit and
// normalised output), and the share whose exit code reproduced; both are
// 0 when nothing was re-run. Claims whose recorded output was not
// comparable count toward the exit rate only.
func (r Report) Rate() (exact, exit float64, rerun int) {
	var ex, ok int
	for _, c := range r.Claims {
		switch c.Status {
		case ClaimReproduced:
			ex++
			ok++
		case ClaimExitOnly, ClaimOutputDiffers:
			ok++
		case ClaimDiverged:
		default:
			continue
		}
		rerun++
	}
	if rerun == 0 {
		return 0, 0, 0
	}
	return float64(ex) / float64(rerun), float64(ok) / float64(rerun), rerun
}

// RunFunc runs command in dir under sandbox profile (NoSandbox for none,
// backend "" for the machine's choice) and returns its exit code and
// combined output. ExecCommand is the real one; a test passes its own.
type RunFunc func(ctx context.Context, dir, command, sandbox, backend string) (exit int, output string, err error)

// VerifyOptions configures VerifyEvidence.
type VerifyOptions struct {
	// Run re-executes the recorded commands; false checks only the
	// signature, the subject and which claims have a record.
	Run bool
	// Exec runs one command; nil means ExecCommand.
	Exec RunFunc
	// Trusted are the keys a signature may be from; nil means
	// TrustedKeys(DefaultKeyDir()).
	Trusted map[string]ed25519.PublicKey
	// Timeout bounds each command; zero means CommandTimeout.
	Timeout time.Duration
}

// CommandTimeout bounds one re-run command, as the verify gate bounds its
// own (verify.Runner's default).
const CommandTimeout = 10 * time.Minute

// VerifyEvidence checks the evidence on commit: the signature, that its
// subject is the commit's tree, and — with opts.Run — that each recorded
// command, re-run in a throwaway checkout of the commit under its
// recorded sandbox profile, exits the same and prints the same normalised
// output. Claims with no record (a check the proof note lists, or a turn
// whose model claimed to have verified, with no command recorded) are
// reported as unverified assertions.
func VerifyEvidence(ctx context.Context, repoDir, commit string, opts VerifyOptions) (Report, error) {
	sha, err := Resolve(ctx, repoDir, commit)
	if err != nil {
		return Report{}, notACommit(ctx, repoDir, commit, err)
	}
	rep := Report{Commit: sha, Signature: SigNone}
	if rep.Tree, err = TreeOf(ctx, repoDir, sha); err != nil {
		return rep, err
	}
	rec, hasProof, err := Read(ctx, repoDir, sha)
	if err != nil {
		return rep, err
	}
	rep.HasProof = hasProof
	env, ok, err := ReadEvidence(ctx, repoDir, sha)
	if err != nil {
		return rep, err
	}
	var st Statement
	if ok {
		if st, err = env.Statement(); err != nil {
			rep.Notes = append(rep.Notes, "evidence note unreadable: "+err.Error())
			ok = false
		}
	}
	if ok {
		rep.HasEvidence = true
		trusted := opts.Trusted
		if trusted == nil {
			dir, derr := DefaultKeyDir()
			if derr == nil {
				trusted, err = TrustedKeys(dir)
				if err != nil {
					return rep, err
				}
			}
		}
		rep.Signature, rep.KeyID = CheckSignature(env, trusted)
		rep.Subject = st.subjectTree()
		rep.SubjectOK = rep.Subject != "" && rep.Subject == rep.Tree
	}
	rerun := opts.Run && rep.HasEvidence && rep.SubjectOK
	var dir string
	if rerun && hasCommands(st) {
		top, cleanup, err := checkout(ctx, repoDir, sha)
		if err != nil {
			return rep, err
		}
		defer cleanup()
		dir = top
	}
	run := opts.Exec
	if run == nil {
		run = ExecCommand
	}
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = CommandTimeout
	}
	sandboxNoted := map[string]bool{}
	for _, se := range st.Predicate.Sessions {
		if se.Sandbox.Profile != NoSandbox && se.Sandbox.SHA256 != "" && !sandboxNoted[se.Sandbox.Profile] {
			sandboxNoted[se.Sandbox.Profile] = true
			if p, ok := shell.FindProfile(se.Sandbox.Profile); !ok {
				rep.Notes = append(rep.Notes, "sandbox profile "+se.Sandbox.Profile+" is not known here")
			} else if SandboxDigest(p, se.Sandbox.Backend) != se.Sandbox.SHA256 {
				rep.Notes = append(rep.Notes, "sandbox profile "+se.Sandbox.Profile+" is defined differently here than when the evidence was made")
			}
		}
		cwd := dir
		if dir != "" && se.Workdir != "" && se.Workdir != "." {
			cwd = filepath.Join(dir, filepath.FromSlash(se.Workdir))
		}
		for _, t := range se.Turns {
			if len(t.Commands) == 0 && t.Claimed != "" {
				rep.Claims = append(rep.Claims, Claim{Session: se.Session, Turn: t.Turn, What: "claimed: " + t.Claimed,
					Status: ClaimUnverified, Detail: "no command recorded for this turn"})
			}
			for _, c := range t.Commands {
				cl := Claim{Session: se.Session, Turn: t.Turn, What: c.Command, Stale: c.Stale}
				switch {
				case c.Withheld:
					cl.Status, cl.Detail = ClaimNotRun, "held-out gate; its command is not recorded"
				case !rerun:
					cl.Status, cl.Detail = ClaimNotRun, staticReason(opts.Run, rep)
				default:
					cl.Status, cl.Detail = rerunOne(ctx, run, cwd, c, se.Sandbox.Backend, timeout)
				}
				rep.Claims = append(rep.Claims, cl)
			}
		}
	}
	if hasProof {
		rep.Claims = append(rep.Claims, unbackedChecks(rec, st)...)
	}
	return rep, nil
}

// checkout checks commit out, detached, into a fresh temporary directory.
// It is internal/worktree's Detached without the patch: internal/agent
// imports this package and must not reach the worktree package
// (internal/archtest), so the dozen lines are repeated here.
func checkout(ctx context.Context, repoDir, commit string) (string, func(), error) {
	dir, err := os.MkdirTemp("", "janetcode-verify-*")
	if err != nil {
		return "", nil, err
	}
	// `git worktree add` refuses a path that exists, even empty.
	if err := os.Remove(dir); err != nil {
		return "", nil, err
	}
	if _, err := gitx.Run(ctx, repoDir, nil, "worktree", "add", "--detach", "-q", dir, commit); err != nil {
		_ = os.RemoveAll(dir)
		return "", nil, gitx.Wrapf(err, "git worktree add %s", Short(commit))
	}
	cleanup := func() {
		if _, err := gitx.Run(context.Background(), repoDir, nil, "worktree", "remove", "--force", dir); err != nil {
			_ = os.RemoveAll(dir)
			_, _ = gitx.Run(context.Background(), repoDir, nil, "worktree", "prune")
		}
		_ = os.RemoveAll(dir)
	}
	return dir, cleanup, nil
}

func hasCommands(st Statement) bool {
	for _, se := range st.Predicate.Sessions {
		for _, t := range se.Turns {
			for _, c := range t.Commands {
				if !c.Withheld {
					return true
				}
			}
		}
	}
	return false
}

func staticReason(asked bool, rep Report) string {
	switch {
	case !asked:
		return "static check"
	case !rep.SubjectOK:
		return "the evidence is not about this commit's tree"
	}
	return ""
}

// rerunOne re-runs one recorded command and compares.
func rerunOne(ctx context.Context, run RunFunc, dir string, c CommandEvidence, backend string, timeout time.Duration) (ClaimStatus, string) {
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	exit, out, err := run(cctx, dir, c.Command, c.Sandbox, backend)
	if err != nil {
		return ClaimNotRun, err.Error()
	}
	if c.Source == "bash" {
		// What the model saw was the bash tool's result: cut to the same
		// budget, so a recorded cut output compares with a cut re-run.
		out = textfmt.Truncate(out)
	}
	red, _ := secret.Redact(out)
	digest, partial := OutputDigest(red, dir)
	if !exitMatches(c.Exit, exit) {
		return ClaimDiverged, fmt.Sprintf("exit %s, now %d", exitWord(c.Exit), exit)
	}
	switch {
	case c.Output == "" || c.OutputPartial || partial:
		return ClaimExitOnly, fmt.Sprintf("exit %d; output not comparable", exit)
	case c.Output != digest:
		return ClaimOutputDiffers, fmt.Sprintf("exit %d; output hash %s, now %s", exit, shortDigest(c.Output), shortDigest(digest))
	}
	return ClaimReproduced, fmt.Sprintf("exit %d", exit)
}

func exitMatches(recorded, now int) bool {
	if recorded == ExitUnknownFailure {
		return now != 0
	}
	return recorded == now
}

func exitWord(code int) string {
	if code == ExitUnknownFailure {
		return "non-zero"
	}
	return fmt.Sprint(code)
}

func shortDigest(d string) string {
	d = strings.TrimPrefix(d, "sha256:")
	if len(d) > 12 {
		return d[:12]
	}
	return d
}

// unbackedChecks are the proof note's checks that no recorded command in
// the evidence stands behind: on a commit with no evidence at all, every
// one of them.
func unbackedChecks(rec Record, st Statement) []Claim {
	type key struct {
		session string
		turn    int
	}
	cmds := map[key][]string{}
	for _, se := range st.Predicate.Sessions {
		for _, t := range se.Turns {
			for _, c := range t.Commands {
				cmds[key{se.Session, t.Turn}] = append(cmds[key{se.Session, t.Turn}], c.Command)
			}
		}
	}
	var out []Claim
	for _, t := range rec.Turns {
		sess := t.Session
		if sess == "" {
			sess = rec.Session
		}
		for _, chk := range t.Checks {
			if !backed(chk, cmds[key{sess, t.Turn}]) {
				out = append(out, Claim{Session: sess, Turn: t.Turn, What: chk, Status: ClaimUnverified,
					Detail: "the proof note lists it; no run of it is recorded"})
			}
		}
	}
	return out
}

// backed reports whether a proof note's check label (internal/agent's
// checkLabel: folded onto one line, cut to a length) names one of cmds.
func backed(label string, cmds []string) bool {
	label = strings.TrimSuffix(strings.TrimSpace(label), "…")
	for _, c := range cmds {
		folded := strings.Join(strings.Fields(c), " ")
		if c == label || folded == label || strings.HasPrefix(folded, label) {
			return true
		}
	}
	return false
}

// maxRerunOutput bounds what one re-run command's output may cost in
// memory, as the bash tool bounds its own.
const maxRerunOutput = 8 << 20

// boundedBuffer keeps the first maxRerunOutput bytes written to it.
type boundedBuffer struct {
	buf     bytes.Buffer
	dropped int
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	n := len(p)
	if room := maxRerunOutput - b.buf.Len(); room < len(p) {
		if room < 0 {
			room = 0
		}
		b.dropped += len(p) - room
		p = p[:room]
	}
	b.buf.Write(p)
	return n, nil
}

func (b *boundedBuffer) String() string {
	if b.dropped == 0 {
		return b.buf.String()
	}
	return fmt.Sprintf("%s\n... [%d bytes omitted: the command printed more than is kept] ...\n", b.buf.String(), b.dropped)
}

// ExecCommand is the RunFunc verify uses: the command through the user's
// shell in dir, inside sandbox profile when one was recorded (and an
// error, so the claim is reported not re-run, when this machine cannot
// provide it), with the agent's credentials scrubbed from the environment
// as for any model-run command.
func ExecCommand(ctx context.Context, dir, command, sandbox, backend string) (int, string, error) {
	var cmd *exec.Cmd
	if sandbox == "" || sandbox == NoSandbox {
		cmd = shell.Command(ctx, command)
		cmd.Dir = dir
	} else {
		sb := &shell.Sandbox{}
		if err := sb.SetProfile(sandbox); err != nil {
			return 0, "", fmt.Errorf("sandbox %s: %w", sandbox, err)
		}
		// The recorded backend first; the profile is what the evidence
		// is about, so another backend that can carry it will do when
		// this machine lacks that one.
		on := backend != "" && sb.SetBackend(backend) == nil && sb.Enable() == nil
		if !on {
			if err := errors.Join(sb.SetBackend(shell.BackendAuto), sb.Enable()); err != nil {
				return 0, "", fmt.Errorf("sandbox %s unavailable here: %w", sandbox, err)
			}
		}
		c, err := sb.Command(ctx, dir, command)
		if err != nil {
			return 0, "", fmt.Errorf("sandbox %s: %w", sandbox, err)
		}
		cmd = c
		if cmd.Dir == "" {
			cmd.Dir = dir
		}
	}
	shell.Supervise(cmd)
	if cmd.Env == nil {
		cmd.Env = shell.ScrubEnv(os.Environ())
	}
	out := &boundedBuffer{}
	cmd.Stdout, cmd.Stderr = out, out
	err := cmd.Run()
	if errors.Is(err, exec.ErrWaitDelay) {
		err = nil
	}
	var ee *exec.ExitError
	switch {
	case err == nil:
		return 0, out.String(), nil
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		return 124, out.String(), nil
	case errors.As(err, &ee):
		return ee.ExitCode(), out.String(), nil
	default:
		return 0, "", err
	}
}

// VerifySummary renders a report for a person: the signature and subject
// first, then one line per claim, then the rates.
func VerifySummary(r Report) string {
	var b strings.Builder
	fmt.Fprintf(&b, "verify %s\n", Short(r.Commit))
	switch {
	case !r.HasEvidence:
		b.WriteString("✗ no signed evidence on this commit\n")
	default:
		mark := "✓"
		if r.Signature != SigValid {
			mark = "✗"
		}
		fmt.Fprintf(&b, "%s signature: %s", mark, r.Signature)
		if r.KeyID != "" {
			fmt.Fprintf(&b, " (%s)", r.KeyID)
		}
		b.WriteString("\n")
		if r.SubjectOK {
			fmt.Fprintf(&b, "✓ subject: tree %s is this commit's\n", Short(r.Tree))
		} else {
			fmt.Fprintf(&b, "✗ subject: evidence is about tree %s, this commit's is %s\n", orDash(Short(r.Subject)), Short(r.Tree))
		}
	}
	for _, n := range r.Notes {
		fmt.Fprintf(&b, "⚠ %s\n", n)
	}
	for _, c := range r.Claims {
		who := ""
		if c.Session != "" {
			who = " (" + c.Session + ")"
		}
		stale := ""
		if c.Stale {
			stale = " · ran before later edits in its turn"
		}
		detail := ""
		if c.Detail != "" {
			detail = " · " + c.Detail
		}
		fmt.Fprintf(&b, "%s turn %d%s: %s: %s%s%s\n", claimMark(c.Status), c.Turn, who, c.Status,
			textfmt.FirstLine(c.What, maxCheckLabel), detail, stale)
	}
	exact, exit, n := r.Rate()
	if n > 0 {
		fmt.Fprintf(&b, "re-ran %d · %.0f%% reproduced exactly · %.0f%% same exit", n, exact*100, exit*100)
	} else {
		b.WriteString("nothing re-run")
	}
	if u := r.Count(ClaimUnverified); u > 0 {
		fmt.Fprintf(&b, " · %d unverified assertion(s)", u)
	}
	return b.String()
}

// maxCheckLabel bounds a command shown on one claim line.
const maxCheckLabel = 120

func claimMark(s ClaimStatus) string {
	switch s {
	case ClaimReproduced, ClaimExitOnly:
		return "✓"
	case ClaimDiverged:
		return "✗"
	case ClaimNotRun:
		return "—"
	default:
		return "⚠"
	}
}
