package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/xmonader/janetcode/internal/config"
	"github.com/xmonader/janetcode/internal/headless"
	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/textfmt"
	"github.com/xmonader/janetcode/internal/treesitter"
	"github.com/xmonader/janetcode/internal/whatif"
)

// whatIfOptions gathers --what-if's flags for runWhatIf.
type whatIfOptions struct {
	session, off, deny, prompt string
	fromTurn                   int
	keep, json                 bool
}

// runWhatIf builds internal/whatif's Options from cfg and prov -- the
// same provider and model any other run of this process would use, so
// --model, --provider, --max-turns and --compact-at apply to --what-if
// exactly as they do everywhere else -- calls whatif.Run, and prints the
// report. All of the actual work (resolving the session, preparing the
// worktree, replaying, going live, folding the comparison) is
// internal/whatif's; this only wires flags to it and prints what comes
// back.
func runWhatIf(ctx context.Context, cfg *config.Config, prov llm.Provider, o whatIfOptions) error {
	report, err := whatif.Run(ctx, whatif.Options{
		SessionArg: o.session, Symbols: treesitter.Parser(),
		FromTurn: o.fromTurn,
		Config:   cfg,
		Provider: prov,
		Off:      textfmt.SplitCSV(o.off),
		Deny:     textfmt.SplitCSV(o.deny),
		Prompt:   o.prompt,
		Keep:     o.keep,
		Log:      func(format string, args ...any) { fmt.Fprintf(os.Stderr, format+"\n", args...) },
	})
	if errors.Is(err, whatif.ErrBadArgument) {
		return headless.BadInput("%v", err)
	}
	if err != nil {
		return err
	}
	if o.json {
		data, jerr := report.JSON()
		if jerr != nil {
			return jerr
		}
		_, err = os.Stdout.Write(append(data, '\n'))
		return err
	}
	_, err = io.WriteString(os.Stdout, report.Text())
	return err
}
