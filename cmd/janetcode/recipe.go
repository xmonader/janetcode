package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/xmonader/janetcode/internal/agent"
	"github.com/xmonader/janetcode/internal/headless"
	"github.com/xmonader/janetcode/internal/recipe"
	"github.com/xmonader/janetcode/internal/textfmt"
)

// runRecipeFlag implements `janetcode --recipe <name> [k=v ...]`: it
// replays a saved recipe's tool calls through Agent.RunRecipe — no model
// call, no tokens spent, unless a step fails or the project's verify gate
// does. Approvals still apply, the same way -p asks on the terminal;
// NewStdinApprover is the same approver -p installs. r is already loaded
// (the caller needs it first anyway, to decide whether MCP servers are
// worth starting for it — see NeedsMCP). A failure hands over to the
// model exactly like -p running on to satisfy a failing --until check:
// one more turn, with agent.RecipeHandover as the prompt, through
// headless.Run so its output is reported the same way. Without a
// configured provider there is nothing to hand over to, so the run just
// reports the failure and exits non-zero.
//
// Before any of that, preflightRecipe refuses to start at all when stdin
// cannot answer a permission prompt and the recipe has a mutating step
// that would need one: without it, every such step is auto-denied one at
// a time, each denial still hands over to the model, and the model's own
// retry of the exact same call is denied the exact same way — a run that
// spends tokens to arrive at nothing. And whenever a step or the verify
// gate does fail on a permission denial (RecipeFailure.Denied) rather
// than the tool's own merits, this reports it directly instead of handing
// over, for the same reason: the model would only be told no again.
func runRecipeFlag(ctx context.Context, ag *agent.Agent, cwd string, r recipe.Recipe, kv []string, stdout, stderr io.Writer, stdin *os.File) error {
	args, err := parseRecipeArgs(kv)
	if err != nil {
		// A malformed k=v is the caller's input, like an unknown recipe:
		// the usage status, not a failed run.
		return headless.BadInput("--recipe: %v", err)
	}
	if err := preflightRecipe(ag, r, args, stdin, stderr); err != nil {
		return err
	}
	if ag.Approver == nil {
		// The approver owns a goroutine reading stdin, so it is closed on
		// every way out of this function -- including the handover to
		// headless.Run below, which finds the approver already set and so
		// installs (and closes) none of its own.
		sa := headless.NewStdinApprover(stdin, stderr)
		defer func() {
			_ = sa.Close()
			ag.Approver = nil
		}()
		ag.Approver = sa
	}
	report, failed, err := ag.RunRecipe(ctx, r, args, nil)
	if report != "" {
		fmt.Fprintln(stdout, report)
	}
	if err != nil {
		return fmt.Errorf("--recipe %s: %w", r.Name, err)
	}
	if failed == nil {
		return nil
	}
	if failed.Denied {
		// The harness itself refused this, not the tool: handing it to
		// the model would only spend a turn learning what this already
		// knows. Report it and stop, the same way a bad --until check
		// would if there were nothing left to try.
		if failed.Verify {
			return fmt.Errorf("--recipe %s: the project's checks failed with a permission denial: %s", r.Name, textfmt.OneLine(failed.Output))
		}
		return fmt.Errorf("--recipe %s: step %d (%s) was denied: %s", r.Name, failed.Step, failed.Tool, textfmt.OneLine(failed.Output))
	}
	if ag.Provider == nil {
		if failed.Verify {
			return fmt.Errorf("--recipe %s: the project's checks failed and no model is configured to finish the job", r.Name)
		}
		return fmt.Errorf("--recipe %s: step %d (%s) failed and no model is configured to finish the job", r.Name, failed.Step, failed.Tool)
	}
	if failed.Verify {
		fmt.Fprintf(stderr, "[recipe] steps all ran, but the project's checks failed; handing over to the model\n")
	} else {
		fmt.Fprintf(stderr, "[recipe] step %d (%s) failed; handing over to the model\n", failed.Step, failed.Tool)
	}
	return headless.Run(ctx, headless.Options{
		Agent: ag, Prompt: agent.RecipeHandover(r, *failed),
		Stdout: stdout, Stderr: stderr, Stdin: stdin, Cwd: cwd,
	})
}

// preflightRecipe fails fast, before RunRecipe touches anything, when
// stdin cannot answer a permission prompt (no terminal, and --yolo was
// not given) and the recipe has at least one mutating step this agent's
// rules and auto modes would not already clear — see
// Agent.StepsNeedingApproval. An error from recipe.Expand is not this
// function's business to report: RunRecipe (or its own caller) will hit
// the same error and say so.
func preflightRecipe(ag *agent.Agent, r recipe.Recipe, args map[string]string, stdin *os.File, stderr io.Writer) error {
	if ag.Settings().AutoApprove || headless.StdinInteractive(stdin) {
		return nil
	}
	steps, err := recipe.Expand(r, args)
	if err != nil {
		return nil
	}
	need := ag.StepsNeedingApproval(steps)
	if len(need) == 0 {
		return nil
	}
	fmt.Fprintf(stderr, "--recipe %s: %s would need approval this run cannot ask for (stdin is not a terminal):\n", r.Name, textfmt.Plural(len(need), "step"))
	for _, n := range need {
		fmt.Fprintf(stderr, "  %s\n", n)
	}
	fmt.Fprintf(stderr, "Re-run with --yolo to auto-approve, or add a permission rule from an interactive session first (/permissions allow ...).\n")
	return fmt.Errorf("--recipe %s: needs approval this run cannot give", r.Name)
}

// parseRecipeArgs reads --recipe's positional k=v arguments.
func parseRecipeArgs(kv []string) (map[string]string, error) {
	out := map[string]string{}
	for _, tok := range kv {
		k, v, ok := strings.Cut(tok, "=")
		if !ok || k == "" {
			return nil, fmt.Errorf("bad argument %q, want k=v", tok)
		}
		out[k] = v
	}
	return out, nil
}
