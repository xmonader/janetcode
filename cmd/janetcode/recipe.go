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
)

// runRecipeFlag implements `janetcode --recipe <name> [k=v ...]`: it
// replays a saved recipe's tool calls through Agent.RunRecipe — no model
// call, no tokens spent, unless a step fails. Approvals still apply, the
// same way -p asks on the terminal; NewStdinApprover is the same approver
// -p installs. A failed step hands over to the model exactly like -p
// running on to satisfy a failing --until check: one more turn, with
// agent.RecipeHandover as the prompt, through headless.Run so its output
// is reported the same way. Without a configured provider there is
// nothing to hand over to, so the run just reports the failure and exits
// non-zero.
func runRecipeFlag(ctx context.Context, ag *agent.Agent, cwd, name string, kv []string, stdout, stderr io.Writer, stdin *os.File) error {
	if ag.Approver == nil {
		ag.Approver = headless.NewStdinApprover(stdin, stderr)
	}
	args, err := parseRecipeArgs(kv)
	if err != nil {
		return fmt.Errorf("--recipe: %w", err)
	}
	r, err := recipe.Load(cwd, name)
	if err != nil {
		return fmt.Errorf("--recipe: %w", err)
	}
	report, failed, err := ag.RunRecipe(ctx, r, args, nil)
	if report != "" {
		fmt.Fprintln(stdout, report)
	}
	if err != nil {
		return fmt.Errorf("--recipe %s: %w", name, err)
	}
	if failed == nil {
		return nil
	}
	if ag.Provider == nil {
		return fmt.Errorf("--recipe %s: step %d (%s) failed and no model is configured to finish the job", name, failed.Step, failed.Tool)
	}
	fmt.Fprintf(stderr, "[recipe] step %d (%s) failed; handing over to the model\n", failed.Step, failed.Tool)
	return headless.Run(ctx, headless.Options{
		Agent: ag, Prompt: agent.RecipeHandover(r, *failed),
		Stdout: stdout, Stderr: stderr, Stdin: stdin, Cwd: cwd,
	})
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
