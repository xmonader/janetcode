package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func readProvenance(path string) (Provenance, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Provenance{}, err
	}
	var p Provenance
	if err := json.Unmarshal(data, &p); err != nil {
		return Provenance{}, fmt.Errorf("%s: %w", path, err)
	}
	if p.Schema != provenanceSchema {
		return Provenance{}, fmt.Errorf("%s: schema %q, want %q", path, p.Schema, provenanceSchema)
	}
	return p, nil
}

// runReport writes RELEASE-EVIDENCE.md: the human-readable summary that
// names the commit and the exact command behind each gate.
func runReport(args []string) error {
	fs := flag.NewFlagSet("report", flag.ContinueOnError)
	dist := fs.String("dist", "dist", "release directory")
	if err := fs.Parse(args); err != nil {
		return err
	}
	prov, err := readProvenance(filepath.Join(*dist, "provenance.json"))
	if err != nil {
		return err
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "# janetcode %s release evidence\n\n", prov.Version)
	fmt.Fprintf(&sb, "Source commit: `%s` (%s)\nToolchain: %s\nRebuild and compare: `%s`\n\n", prov.Commit, prov.CommitTime, prov.Go, prov.Build.Reproduce)
	sb.WriteString("## Artifacts\n\n| file | sha256 |\n|---|---|\n")
	for _, a := range prov.Artifacts {
		fmt.Fprintf(&sb, "| %s | `%s` |\n", a.Name, a.SHA256)
	}
	if data, err := os.ReadFile(filepath.Join(*dist, "gates.json")); err == nil {
		var g GateRun
		if err := json.Unmarshal(data, &g); err != nil {
			return fmt.Errorf("gates.json: %w", err)
		}
		fmt.Fprintf(&sb, "\n## Gates\n\nRun on commit `%s` (%s, %s); working tree modified: %v.\n\n| gate | command | result |\n|---|---|---|\n", g.Commit, g.Go, g.Host, g.Dirty)
		for _, r := range g.Gates {
			res := r.Status
			if r.Skipped != "" {
				res += ": " + r.Skipped
			}
			fmt.Fprintf(&sb, "| %s | `%s` | %s |\n", r.Name, r.Command, res)
		}
	} else {
		sb.WriteString("\n## Gates\n\nNot recorded (run `make release-gates`).\n")
	}
	sb.WriteString("\n## Verify\n\n```\nsha256sum -c SHA256SUMS\n```\n\nSee docs/manual/security.md for the full clean-machine procedure.\n")
	return os.WriteFile(filepath.Join(*dist, "RELEASE-EVIDENCE.md"), []byte(sb.String()), 0o644)
}
