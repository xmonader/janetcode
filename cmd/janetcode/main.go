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

	"github.com/xmonader/janetcode/internal/headless"
	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/llm/anthropic"
	"github.com/xmonader/janetcode/internal/llm/openai"
	"github.com/xmonader/janetcode/internal/session"
	_ "github.com/xmonader/janetcode/internal/treesitter" // registers the grammar parser when built with -tags treesitter
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
		if keyRejected(err) {
			fmt.Fprintln(os.Stderr, "janetcode: the endpoint rejected the API key; check the key for the provider in use (janetcode --doctor shows which key each provider has)")
		}
	}
	// The status says which of the handful of things that can go wrong
	// went wrong, so a script can tell a missing key from a turn limit
	// without reading the message.
	os.Exit(headless.ExitCode(err))
}

// keyRejected reports whether err is a provider's 401 or 403: the key was
// sent and refused, as opposed to missing.
func keyRejected(err error) bool {
	status := 0
	if oe := (*openai.APIError)(nil); errors.As(err, &oe) {
		status = oe.Status
	} else if ae := (*anthropic.APIError)(nil); errors.As(err, &ae) {
		status = ae.Status
	}
	return status == 401 || status == 403
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

func run(args []string) error {
	fs, o := newFlagSet()
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if o.continueC {
		o.cont = true
	}
	if o.resumeR != "" {
		o.resume = o.resumeR
	}
	if done, err := earlyMode(fs, o); done {
		return err
	}
	initial := strings.TrimSpace(strings.Join(fs.Args(), " "))
	format, err := headless.ParseFormat(o.outFormat)
	if err != nil {
		return fmt.Errorf("%w: %v", headless.ErrBadInput, err)
	}
	if err := checkModes(o, initial, format); err != nil {
		return err
	}
	if done, err := offlineMode(o); done {
		return err
	}
	cfg, needsConnect, cerr := loadConfig(o)
	err = cerr
	if done, ierr := offlineInspect(fs, o, cfg, cerr); done {
		return ierr
	}
	if err == nil {
		defer startRates(cfg)()
		if o.promptText != "" {
			for _, n := range cfg.Notes {
				fmt.Fprintln(os.Stderr, "janetcode:", n)
			}
		}
	}
	if err != nil && !needsConnect {
		// A config file that cannot be used is a different thing from
		// credentials that are merely missing, and exits differently.
		// config.Load says which it is in the error's type, so nothing
		// here has to guess from the wording.
		return err
	}
	var prov llm.Provider
	if !needsConnect {
		if prov, err = buildProvider(cfg); err != nil {
			return err
		}
	}
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	cwd, reportWorktree, err := openWorktree(o, cwd)
	defer reportWorktree()
	if err != nil {
		return err
	}
	if done, werr := whatIfMode(o, cfg, prov); done {
		return werr
	}
	if done, eerr := evalMode(o, cfg, prov); done {
		return eerr
	}

	sessDir, err := session.Dir()
	if err != nil {
		return err
	}
	sess, history, err := openSession(o, cfg, sessDir, cwd)
	if err != nil {
		return err
	}
	if done, berr := bgMode(o, sess, initial); done {
		return berr
	}

	r, err := newRoot(fs, o, cfg, prov, needsConnect, cwd, sessDir, initial, format, sess, history)
	defer r.close()
	if err != nil {
		return err
	}
	if o.mcpAuth != "" {
		return authorizeMCP(o.mcpAuth, cfg.MCPServers, r.projectServers, r.tokens)
	}
	if err := r.wireProject(); err != nil {
		return err
	}
	return r.dispatch()
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
	for _, in := range infos {
		fork := ""
		if in.Meta.ForkOf != "" {
			fork = " (fork of " + in.Meta.ForkOf + ")"
		}
		fmt.Printf("%s  %s  %-3d msgs  %s/%s  %s%s\n", in.Meta.ID, in.Meta.Created.Local().Format("2006-01-02 15:04"), in.Messages, in.Meta.Provider, in.Meta.Model, in.Label(), fork)
	}
	fmt.Println("\nresume with: janetcode --resume <id>   (or --continue for the newest)")
	return nil
}
