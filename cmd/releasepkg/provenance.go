package main

import (
	"flag"
	"fmt"
	"path/filepath"
	"strings"
)

// Provenance is the record of how the artifacts were made. Every claim
// in it is read from the artifacts or from git, then cross-checked: a
// binary whose embedded revision is not the commit being released, or
// that was built from a modified tree, makes the record refuse to exist.
type Provenance struct {
	Schema     string     `json:"schema"`
	Version    string     `json:"version"`
	Source     string     `json:"source"`
	Commit     string     `json:"commit"`
	CommitTime string     `json:"commit_time"`
	Go         string     `json:"go_version"`
	Build      Build      `json:"build"`
	Artifacts  []Artifact `json:"artifacts"`
}

// Build is how the binaries were built.
type Build struct {
	Command string `json:"command"`
	Flags   string `json:"flags"`
	// Reproduce is the command that rebuilds them and compares.
	Reproduce string `json:"reproduce"`
}

const provenanceSchema = "janetcode.release-provenance/1"

func runProvenance(args []string) error {
	fs := flag.NewFlagSet("provenance", flag.ContinueOnError)
	dist := fs.String("dist", "dist", "directory holding the release binaries")
	out := fs.String("out", "", "output file (default <dist>/provenance.json)")
	version := fs.String("version", "", "release version, e.g. v0.3.0 (required)")
	repo := fs.String("source", "https://github.com/xmonader/janetcode", "source repository URL")
	allowDirty := fs.Bool("allow-dirty", false, "accept binaries built from a modified tree (never for a release)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *version == "" {
		return fmt.Errorf("-version is required")
	}
	if *out == "" {
		*out = filepath.Join(*dist, "provenance.json")
	}
	p, err := buildProvenance(*dist, *version, *repo, *allowDirty)
	if err != nil {
		return err
	}
	return writeJSON(*out, p)
}

func buildProvenance(dist, version, source string, allowDirty bool) (Provenance, error) {
	arts, err := readArtifacts(dist)
	if err != nil {
		return Provenance{}, err
	}
	commit, err := git("rev-parse", "HEAD")
	if err != nil {
		return Provenance{}, err
	}
	when, err := git("show", "-s", "--format=%cI", "HEAD")
	if err != nil {
		return Provenance{}, err
	}
	for _, a := range arts {
		switch {
		case a.Revision == "":
			return Provenance{}, fmt.Errorf("%s has no embedded VCS revision: it was built outside a git checkout or with -buildvcs=false, so its source cannot be shown", a.Name)
		case a.Revision != commit:
			return Provenance{}, fmt.Errorf("%s was built from %s but HEAD is %s: rebuild with make dist", a.Name, a.Revision, commit)
		case a.Modified && !allowDirty:
			return Provenance{}, fmt.Errorf("%s was built from a tree with uncommitted changes (vcs.modified=true)", a.Name)
		case !a.Trimpath:
			return Provenance{}, fmt.Errorf("%s was not built with -trimpath, so it is not reproducible from another directory", a.Name)
		case a.CGO != "0":
			return Provenance{}, fmt.Errorf("%s was built with CGO_ENABLED=%q; release binaries are static (0)", a.Name, a.CGO)
		}
	}
	goVer := arts[0].Go
	for _, a := range arts {
		if a.Go != goVer {
			return Provenance{}, fmt.Errorf("binaries were built by different toolchains: %s and %s", goVer, a.Go)
		}
	}
	flags := arts[0].LDFlags
	return Provenance{
		Schema: provenanceSchema, Version: version, Source: source, Commit: commit, CommitTime: when, Go: goVer,
		Build: Build{
			Command:   "make dist VERSION=" + version,
			Flags:     "CGO_ENABLED=0 go build -trimpath -ldflags " + strings.TrimSpace(quoteIf(flags)) + " ./cmd/janetcode",
			Reproduce: "make repro-check VERSION=" + version,
		},
		Artifacts: arts,
	}, nil
}

func quoteIf(s string) string {
	if s == "" {
		return `""`
	}
	return `"` + s + `"`
}
