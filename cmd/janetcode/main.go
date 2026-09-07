// Command janetcode is an interactive terminal coding agent.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"golang.org/x/term"

	"github.com/xmonader/janetcode/internal/agent"
	"github.com/xmonader/janetcode/internal/config"
	"github.com/xmonader/janetcode/internal/headless"
	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/llm/anthropic"
	"github.com/xmonader/janetcode/internal/llm/openai"
	"github.com/xmonader/janetcode/internal/prompt"
	"github.com/xmonader/janetcode/internal/session"
	"github.com/xmonader/janetcode/internal/tool"
	"github.com/xmonader/janetcode/internal/tui"
)

// version is set at build time via -ldflags "-X main.version=...".
var version = "dev"

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "janetcode:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	fs := flag.NewFlagSet("janetcode", flag.ContinueOnError)
	fs.Usage = func() {
		fmt.Fprintf(fs.Output(), `janetcode %s - a terminal coding agent

Usage: janetcode [flags]            interactive session in the current directory
       janetcode -p "prompt" [flags] run one prompt headless and exit

Flags:
`, version)
		fs.PrintDefaults()
		fmt.Fprintf(fs.Output(), `
Environment: ANTHROPIC_API_KEY, ANTHROPIC_BASE_URL, OPENAI_API_KEY, OPENAI_BASE_URL,
             JANETCODE_PROVIDER, JANETCODE_MODEL, JANETCODE_EFFORT
Config file: $XDG_CONFIG_HOME/janetcode/config.json (default ~/.config/janetcode/config.json)
`)
	}
	var (
		promptText  = fs.String("p", "", "run this prompt headless and exit")
		provider    = fs.String("provider", "", "anthropic or openai (default from config, else anthropic)")
		model       = fs.String("model", "", "model id (default claude-opus-5 for anthropic)")
		effort      = fs.String("effort", "", "anthropic effort: low, medium, high, xhigh, max")
		yolo        = fs.Bool("yolo", false, "auto-approve every tool call")
		cont        = fs.Bool("continue", false, "resume the latest session for this directory")
		resume      = fs.String("resume", "", "resume the session with this id")
		showVersion = fs.Bool("version", false, "print version and exit")
		listSess    = fs.Bool("sessions", false, "list saved sessions for this directory and exit")
		initCfg     = fs.Bool("init", false, "write an example config file and exit")
	)
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if *showVersion {
		fmt.Println("janetcode", version)
		return nil
	}
	if *initCfg {
		p, err := config.WriteExample()
		if err != nil {
			return err
		}
		fmt.Println("wrote", p)
		return nil
	}
	if *listSess {
		return printSessions()
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("unexpected argument %q (use -p \"prompt\" for a one-shot run)", fs.Arg(0))
	}

	cfg, err := config.Load(config.Overrides{Provider: *provider, Model: *model, Effort: *effort, AutoApprove: *yolo})
	if err != nil {
		return err
	}
	prov, err := buildProvider(cfg)
	if err != nil {
		return err
	}
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}

	sessDir, err := session.Dir()
	if err != nil {
		return err
	}
	var (
		sess     *session.Session
		history  []llm.Message
		resumeID = *resume
	)
	if *cont && resumeID == "" {
		if resumeID, err = session.Latest(sessDir, cwd); err != nil {
			return err
		}
	}
	if resumeID != "" {
		if sess, history, err = session.Open(sessDir, resumeID); err != nil {
			return fmt.Errorf("resume: %w", err)
		}
	} else {
		if sess, err = session.New(sessDir, cwd, cfg.Provider, cfg.Model); err != nil {
			return fmt.Errorf("create session: %w", err)
		}
	}

	ag := &agent.Agent{
		Provider: prov,
		Tools: tool.NewRegistry(
			tool.ReadFile{Root: cwd},
			tool.WriteFile{Root: cwd},
			tool.EditFile{Root: cwd},
			tool.ListDir{Root: cwd},
			tool.Glob{Root: cwd},
			tool.Grep{Root: cwd},
			tool.Bash{Root: cwd},
		),
		System:        prompt.System(cwd),
		Model:         cfg.Model,
		MaxTokens:     cfg.MaxTokens,
		MaxIterations: cfg.MaxIterations,
		AutoApprove:   cfg.AutoApprove,
		CompactAt:     cfg.CompactAt,
		Messages:      history,
	}

	if *promptText != "" {
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		text := *promptText
		if extra := pipedStdin(); extra != "" {
			text += "\n\n<stdin>\n" + extra + "\n</stdin>"
		}
		return headless.Run(ctx, headless.Options{
			Agent: ag, Session: sess, Prompt: text,
			Stdout: os.Stdout, Stderr: os.Stderr, Stdin: os.Stdin,
		})
	}
	return tui.Run(tui.Options{
		Agent: ag, Session: sess, Provider: cfg.Provider, Cwd: cwd,
		Resumed: len(history), Version: version, Theme: cfg.Theme,
	})
}

// pipedStdin returns piped input (e.g. `git diff | janetcode -p "review"`),
// or "" when stdin is a terminal or empty. Capped at 1 MB.
func pipedStdin() string {
	if term.IsTerminal(int(os.Stdin.Fd())) {
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
		fmt.Printf("%s  %s  %-3d msgs  %s/%s  %s\n", in.Meta.ID, in.Meta.Created.Local().Format("2006-01-02 15:04"), in.Messages, in.Meta.Provider, in.Meta.Model, in.Preview)
	}
	fmt.Println("\nresume with: janetcode --resume <id>   (or --continue for the newest)")
	return nil
}

func buildProvider(cfg *config.Config) (llm.Provider, error) {
	switch cfg.Provider {
	case "anthropic":
		return anthropic.New(anthropic.Config{
			APIKey:   cfg.Anthropic.APIKey,
			BaseURL:  cfg.Anthropic.BaseURL,
			Thinking: cfg.ThinkingEnabled(),
			Effort:   cfg.Effort,
		})
	case "openai":
		return openai.New(openai.Config{
			APIKey:  cfg.OpenAI.APIKey,
			BaseURL: cfg.OpenAI.BaseURL,
		})
	}
	return nil, fmt.Errorf("unknown provider %q", cfg.Provider)
}
