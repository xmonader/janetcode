package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"text/tabwriter"
	"time"

	"github.com/xmonader/janetcode/internal/headless"
	"github.com/xmonader/janetcode/internal/llm"
)

// listModels is --list-models: what the configured provider serves, as
// the provider reports it, for a script choosing --model or a person
// checking a name before typing it.
func listModels(ctx context.Context, w io.Writer, prov llm.Provider, format headless.Format) error {
	if prov == nil {
		return fmt.Errorf("%w: no provider is configured; run janetcode to /connect one, or set a key in the environment", llm.ErrNoAPIKey)
	}
	lister, ok := prov.(llm.ModelLister)
	if !ok {
		return fmt.Errorf("the %s provider cannot list its models", prov.Name())
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	models, err := lister.ListModels(ctx)
	if err != nil {
		return fmt.Errorf("listing %s's models: %w", prov.Name(), err)
	}
	if format == headless.FormatJSON {
		type row struct {
			ID            string `json:"id"`
			Name          string `json:"name,omitempty"`
			ContextWindow int    `json:"context_window,omitempty"`
			MaxOutput     int    `json:"max_output,omitempty"`
		}
		rows := make([]row, 0, len(models))
		for _, m := range models {
			rows = append(rows, row{m.ID, m.Name, m.ContextWindow, m.MaxOutput})
		}
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(rows)
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "MODEL\tCONTEXT\tMAX OUTPUT")
	for _, m := range models {
		fmt.Fprintf(tw, "%s\t%s\t%s\n", m.ID, tokens(m.ContextWindow), tokens(m.MaxOutput))
	}
	return tw.Flush()
}

func tokens(n int) string {
	switch {
	case n <= 0:
		return "-"
	case n >= 1000 && n%1000 == 0:
		return fmt.Sprintf("%dk", n/1000)
	case n >= 1000:
		return fmt.Sprintf("%.1fk", float64(n)/1000)
	}
	return fmt.Sprint(n)
}
