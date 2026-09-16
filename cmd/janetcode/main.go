// Command janetcode is an interactive terminal coding agent.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"regexp"
	"runtime/debug"
	"strings"

	"golang.org/x/term"

	"github.com/xmonader/janetcode/internal/config"
	"github.com/xmonader/janetcode/internal/headless"
	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/session"
)

// version is set at build time via -ldflags "-X main.version=...". A
// plain `go install .../cmd/janetcode@v0.2.0` has no ldflags, so the
// module version or the commit the Go toolchain recorded stands in.
var version = "dev"

// pseudoVersionRe matches a Go pseudo-version: a timestamp and a commit
// where a tag would be.
var pseudoVersionRe = regexp.MustCompile(`\.\d{14}-[0-9a-f]{12}`)

func init() {
	if version != "dev" {
		return
	}
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return
	}
	// A tagged module version is the release; a pseudo-version
	// (v0.2.1-0.20260910193945-02799f51583f) is a local build and the
	// commit it names says more, so it falls through to the VCS fields.
	if v := bi.Main.Version; v != "" && v != "(devel)" && !pseudoVersionRe.MatchString(v) {
		version = v
		return
	}
	var rev, dirty string
	for _, s := range bi.Settings {
		switch s.Key {
		case "vcs.revision":
			if len(s.Value) >= 12 {
				rev = s.Value[:12]
			}
		case "vcs.modified":
			if s.Value == "true" {
				dirty = "-dirty"
			}
		}
	}
	if rev != "" {
		version = "dev-" + rev + dirty
	}
}

func main() {
	err := run(os.Args[1:])
	if err != nil {
		fmt.Fprintln(os.Stderr, "janetcode:", err)
		if headless.KeyRejected(err) {
			fmt.Fprintln(os.Stderr, "janetcode: the endpoint rejected the API key; check the key for the provider in use (janetcode --doctor shows which key each provider has)")
		}
	}
	// The status says which of the handful of things that can go wrong
	// went wrong, so a script can tell a missing key from a turn limit
	// without reading the message.
	os.Exit(headless.ExitCode(err))
}

// printFlags is flag.PrintDefaults for the flags keep selects, so the
// help can show the everyday flags apart from the eval ones.
func printFlags(fs *flag.FlagSet, keep func(*flag.Flag) bool) {
	fs.VisitAll(func(f *flag.Flag) {
		if !keep(f) {
			return
		}
		name, usage := flag.UnquoteUsage(f)
		line := "  -" + f.Name
		if name != "" {
			line += " " + name
		}
		line += "\n    \t" + strings.ReplaceAll(usage, "\n", "\n    \t")
		if f.DefValue != "" && f.DefValue != "false" && f.DefValue != "0" {
			if name == "string" {
				line += fmt.Sprintf(" (default %q)", f.DefValue)
			} else {
				line += fmt.Sprintf(" (default %s)", f.DefValue)
			}
		}
		fmt.Fprintln(fs.Output(), line)
	})
}

// startup is the state the stages before the composition root hand each
// other. Each stage fills in the part it is responsible for and the next
// one reads it, so the sequence flags -> config -> offline modes ->
// provider -> worktree -> session -> agent is written down rather than
// implied by the order of a hundred statements.
type startup struct {
	args    []string
	fs      *flag.FlagSet
	opts    *options
	initial string
	format  headless.Format

	cfg *config.Config
	// cfgErr is what config.Load said. It is kept rather than returned
	// because several stages read it: missing credentials with a terminal
	// to ask in is not a failure, and --inspect answers without one.
	cfgErr error
	// needsConnect is the interactive first run: no credentials, but a
	// terminal, so the TUI opens on /connect instead of the process
	// failing for want of a key.
	needsConnect bool
	prov         llm.Provider

	cwd     string
	sessDir string
	sess    *session.Session
	history []llm.Message
	root    *root

	// cleanups run in reverse on the way out, the order the defers they
	// replace ran in.
	cleanups []func()
}

func (s *startup) onClose(f func()) { s.cleanups = append(s.cleanups, f) }

func (s *startup) close() {
	for i := len(s.cleanups) - 1; i >= 0; i-- {
		s.cleanups[i]()
	}
	s.cleanups = nil
}

// startupStage is one step from the command line to a wired session. done
// means the stage was the whole run -- a mode that prints something and
// exits, or one that needs no agent -- and err is what it ended with.
type startupStage struct {
	name string
	fn   func(*startup) (done bool, err error)
}

// startupStages is the order a run is assembled in. Every early exit is a
// stage that reports done; the last stage builds the composition root,
// and what follows it is the dispatch table in modes.go.
var startupStages = []startupStage{
	{"flags", (*startup).stageFlags},
	{"mode checks", (*startup).stageModeChecks},
	{"config", (*startup).stageConfig},
	{"provider", (*startup).stageProvider},
	{"worktree", (*startup).stageWorktree},
	{"replay modes", (*startup).stageReplayModes},
	{"session", (*startup).stageSession},
	{"composition root", (*startup).stageRoot},
}

func run(args []string) error {
	s := &startup{args: args}
	defer s.close()
	for _, stage := range startupStages {
		done, err := stage.fn(s)
		if done || err != nil {
			return err
		}
	}
	return s.root.dispatch()
}

// stageFlags parses the command line, settles the short-flag aliases, and
// runs the modes that print something and exit without loading anything.
func (s *startup) stageFlags() (bool, error) {
	s.fs, s.opts = newFlagSet()
	if err := s.fs.Parse(s.args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return true, nil
		}
		return true, err
	}
	if s.opts.continueC {
		s.opts.cont = true
	}
	if s.opts.resumeR != "" {
		s.opts.resume = s.opts.resumeR
	}
	return earlyMode(s.fs, s.opts)
}

// stageModeChecks rejects flag combinations that do not go together and
// then runs the modes that only read what is already on disk. Neither
// needs a configuration, which is the point: re-reading saved results
// must work when the configuration is the problem.
func (s *startup) stageModeChecks() (bool, error) {
	s.initial = strings.TrimSpace(strings.Join(s.fs.Args(), " "))
	format, err := headless.ParseFormat(s.opts.outFormat)
	if err != nil {
		return true, fmt.Errorf("%w: %v", headless.ErrBadInput, err)
	}
	s.format = format
	if err := checkModes(s.opts, s.initial, format); err != nil {
		return true, err
	}
	return offlineMode(s.opts)
}

// stageConfig loads the configuration, answers --inspect when there is no
// usable one, and starts the background price refresh.
func (s *startup) stageConfig() (bool, error) {
	s.cfg, s.needsConnect, s.cfgErr = loadConfig(s.opts)
	if done, err := offlineInspect(s.fs, s.opts, s.cfg, s.cfgErr); done {
		return true, err
	}
	if s.cfgErr == nil {
		s.onClose(startRates(s.cfg))
		if s.opts.promptText != "" {
			for _, n := range s.cfg.Notes {
				fmt.Fprintln(os.Stderr, "janetcode:", n)
			}
		}
	}
	if s.cfgErr != nil && !s.needsConnect {
		// A config file that cannot be used is a different thing from
		// credentials that are merely missing, and exits differently.
		// config.Load says which it is in the error's type, so nothing
		// here has to guess from the wording.
		return true, s.cfgErr
	}
	return false, nil
}

// stageProvider builds the backend, unless this is the first run with no
// credentials at all -- then the TUI opens on /connect and builds one
// once there is a key.
func (s *startup) stageProvider() (bool, error) {
	if s.needsConnect {
		return false, nil
	}
	prov, err := buildProvider(s.cfg)
	if err != nil {
		return true, err
	}
	s.prov = prov
	return false, nil
}

// stageWorktree settles the directory everything downstream is rooted at:
// the process's own, or the git worktree --worktree asked for, which the
// process changes into.
func (s *startup) stageWorktree() (bool, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return true, err
	}
	cwd, report, err := openWorktree(s.opts, cwd)
	s.onClose(report)
	s.cwd = cwd
	return err != nil, err
}

// stageReplayModes runs the two modes that drive a provider but build no
// session: the counterfactual replay and the eval runner.
func (s *startup) stageReplayModes() (bool, error) {
	if done, err := whatIfMode(s.opts, s.cfg, s.prov); done {
		return true, err
	}
	return evalMode(s.opts, s.cfg, s.prov)
}

// stageSession opens the conversation this run continues or starts, and
// hands it to a detached child when --bg asked for one.
func (s *startup) stageSession() (bool, error) {
	sessDir, err := session.Dir()
	if err != nil {
		return true, err
	}
	s.sessDir = sessDir
	if s.sess, s.history, err = openSession(s.opts, s.cfg, sessDir, s.cwd); err != nil {
		return true, err
	}
	return bgMode(s.opts, s.sess, s.initial)
}

// stageRoot builds the agent and everything around it, and folds in the
// project's own files as far as they are trusted. --mcp-auth is the one
// mode that wants the wiring but not a session, so it ends here.
func (s *startup) stageRoot() (bool, error) {
	r, err := newRoot(s.fs, s.opts, s.cfg, s.prov, s.needsConnect, s.cwd, s.sessDir, s.initial, s.format, s.sess, s.history)
	// newRoot returns the root even when it fails, so whatever it already
	// started is closed on the way out.
	s.root = r
	s.onClose(r.close)
	if err != nil {
		return true, err
	}
	if s.opts.mcpAuth != "" {
		return true, authorizeMCP(s.opts.mcpAuth, s.cfg.MCPServers, r.projectServers, r.tokens)
	}
	if err := r.wireProject(); err != nil {
		return true, err
	}
	return false, nil
}

// pipedStdin returns piped input (e.g. `git diff | janetcode -p "review"`),
// or "" when stdin is a terminal or empty. Capped at 1 MB.
// pipedStdin returns text piped or redirected into the process. Only pipes
// and regular files are read: a terminal is interactive, and a socket or
// other device held open by a parent process would block forever.
func pipedStdin() string {
	if term.IsTerminal(int(os.Stdin.Fd())) {
		return ""
	}
	fi, err := os.Stdin.Stat()
	if err != nil || (fi.Mode()&os.ModeNamedPipe == 0 && !fi.Mode().IsRegular()) {
		return ""
	}
	data, err := io.ReadAll(io.LimitReader(os.Stdin, 1<<20))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

func printSessions() error {
	dir, err := session.Dir()
	if err != nil {
		return err
	}
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	infos, err := session.List(dir, cwd)
	if err != nil {
		return err
	}
	if len(infos) == 0 {
		fmt.Println("no sessions for", cwd)
		return nil
	}
	// Forks are listed under the session they were copied from, the same
	// shape /resume's picker draws (see session.Lineage).
	forked := false
	for _, n := range session.Lineage(infos, "") {
		in := n.Info
		aside := ""
		if m := n.Mark(); m != "" {
			aside = "  (" + m + ")"
		}
		if note := n.Note(); note != "" {
			aside += "  (" + note + ")"
		}
		forked = forked || n.Depth > 0 || n.Orphan
		fmt.Printf("%s%s  %s  %-3d msgs  %s/%s  %s%s\n", n.Prefix(), in.Meta.ID, in.Meta.Created.Local().Format("2006-01-02 15:04"), in.Messages, in.Meta.Provider, in.Meta.Model, in.Label(), aside)
	}
	if forked {
		fmt.Println("\nindented sessions are forks of the one above them")
	}
	fmt.Println("\nresume with: janetcode --resume <id>   (or --continue for the newest)")
	return nil
}
