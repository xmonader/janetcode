package main

import (
	"bytes"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// Gate is one check a release must pass, with the exact command that
// runs it. This table is the single statement of the gates: the
// documentation lists the same commands and a test fails when the two
// differ, and `releasepkg gates` runs these and records the commit each
// one ran on.
type Gate struct {
	Name    string `json:"name"`
	Command string `json:"command"`
	// Needs are programs that must be on PATH; a gate whose program is
	// missing is recorded as skipped, with the reason, never as passed.
	Needs []string `json:"needs,omitempty"`
	What  string   `json:"what"`
}

// Gates are the release gates, in the order they run.
var Gates = []Gate{
	{Name: "unit", Command: "go test ./...", What: "every package's tests, including the dependency-direction and release-metadata drift tests"},
	{Name: "race", Command: "go test -race ./...", What: "the same suite under the race detector"},
	{Name: "lint", Command: "make lint", What: "go vet, and staticcheck when it is installed"},
	{Name: "cross", Command: "make cross", What: "build for windows/amd64 and darwin/arm64, which the Linux run only compiles"},
	{Name: "security", Command: "go test ./internal/shell ./internal/perm ./internal/secret ./internal/extension ./internal/secreport ./internal/trust ./internal/egress ./internal/archtest", What: "the sandbox, permission, secret-scanning, extension-trust, security-report, trust-store, egress and architecture tests on their own"},
	{Name: "security-doctor", Command: "make build && ./bin/janetcode --doctor security", What: "the installed binary's own security report; it exits non-zero when a check fails"},
	{Name: "ui", Command: "make ui-test ui-smoke", Needs: []string{"node", "chrome"}, What: "the web UI in headless Chrome: interactions and a console-error smoke test"},
	{Name: "ui-visual", Command: "make ui-visual", Needs: []string{"node", "chrome"}, What: "pixel comparison of every web UI state with the committed baseline"},
	{Name: "reproducible", Command: "make repro-check", What: "rebuild the release binaries from a fresh clone with an empty build cache and compare them byte for byte"},
	{Name: "supply-chain", Command: "make release-evidence verify-dist", What: "SBOM, licence inventory and provenance generated from the binaries, then verified against SHA256SUMS"},
}

// GateResult is one gate's outcome.
type GateResult struct {
	Gate
	Status     string `json:"status"` // pass, fail or skipped
	ExitCode   int    `json:"exit_code"`
	Started    string `json:"started,omitempty"`
	DurationMS int64  `json:"duration_ms"`
	Skipped    string `json:"skipped_because,omitempty"`
	// Tail is the last lines of output, for a failed gate.
	Tail string `json:"output_tail,omitempty"`
}

// GateRun is the record `releasepkg gates` writes.
type GateRun struct {
	Schema string       `json:"schema"`
	Commit string       `json:"commit"`
	Dirty  bool         `json:"working_tree_modified"`
	Go     string       `json:"go_version"`
	Host   string       `json:"host"`
	Gates  []GateResult `json:"gates"`
}

const gatesSchema = "janetcode.release-gates/1"

func have(tool string) bool {
	if tool == "chrome" {
		for _, c := range []string{os.Getenv("CHROME"), "google-chrome", "chromium", "chromium-browser"} {
			if c == "" {
				continue
			}
			if _, err := exec.LookPath(c); err == nil {
				return true
			}
		}
		return false
	}
	_, err := exec.LookPath(tool)
	return err == nil
}

func runGates(args []string) error {
	fs := flag.NewFlagSet("gates", flag.ContinueOnError)
	out := fs.String("out", filepath.Join("dist", "gates.json"), "where to write the record")
	only := fs.String("only", "", "comma-separated gate names to run (default all)")
	list := fs.Bool("list", false, "print the gates and their commands, run nothing")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *list {
		for _, g := range Gates {
			fmt.Printf("%-16s %s\n", g.Name, g.Command)
		}
		return nil
	}
	want := map[string]bool{}
	for _, n := range strings.Split(*only, ",") {
		if n = strings.TrimSpace(n); n != "" {
			want[n] = true
		}
	}
	for n := range want {
		found := false
		for _, g := range Gates {
			found = found || g.Name == n
		}
		if !found {
			return fmt.Errorf("no gate named %q (releasepkg gates -list names them)", n)
		}
	}
	commit, err := git("rev-parse", "HEAD")
	if err != nil {
		return err
	}
	status, err := git("status", "--porcelain")
	if err != nil {
		return err
	}
	rec := GateRun{Schema: gatesSchema, Commit: commit, Dirty: status != "", Go: runtime.Version(), Host: runtime.GOOS + "/" + runtime.GOARCH}
	failed := 0
	for _, g := range Gates {
		if len(want) > 0 && !want[g.Name] {
			continue
		}
		res := GateResult{Gate: g}
		if missing := missingTool(g); missing != "" {
			res.Status, res.Skipped = "skipped", missing+" is not on PATH"
			fmt.Printf("  skip  %-16s %s\n", g.Name, res.Skipped)
			rec.Gates = append(rec.Gates, res)
			continue
		}
		start := time.Now()
		res.Started = start.UTC().Format(time.RFC3339)
		cmd := exec.Command("sh", "-c", g.Command)
		var buf bytes.Buffer
		cmd.Stdout, cmd.Stderr = &buf, &buf
		err := cmd.Run()
		res.DurationMS = time.Since(start).Milliseconds()
		if err != nil {
			res.Status, res.ExitCode = "fail", 1
			if ee, ok := err.(*exec.ExitError); ok {
				res.ExitCode = ee.ExitCode()
			}
			res.Tail = tail(buf.String(), 30)
			failed++
			fmt.Printf("  FAIL  %-16s %s (exit %d)\n", g.Name, g.Command, res.ExitCode)
		} else {
			res.Status = "pass"
			fmt.Printf("  pass  %-16s %s (%s)\n", g.Name, g.Command, time.Since(start).Round(time.Second))
		}
		rec.Gates = append(rec.Gates, res)
	}
	if err := os.MkdirAll(filepath.Dir(*out), 0o755); err != nil {
		return err
	}
	if err := writeJSON(*out, rec); err != nil {
		return err
	}
	if failed > 0 {
		return fmt.Errorf("%d gate(s) failed; see %s", failed, *out)
	}
	return nil
}

func missingTool(g Gate) string {
	for _, n := range g.Needs {
		if !have(n) {
			return n
		}
	}
	return ""
}

func tail(s string, lines int) string {
	parts := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(parts) > lines {
		parts = parts[len(parts)-lines:]
	}
	return strings.Join(parts, "\n")
}
