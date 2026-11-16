package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// LicenseRow is one module's licence as found in its own source.
type LicenseRow struct {
	Module  string `json:"module"`
	Version string `json:"version"`
	License string `json:"license"`
	// File is the licence file inside the module, "" when it has none.
	File string `json:"file,omitempty"`
}

// classify names a licence from its text. It recognises the licences a
// Go dependency tree realistically carries by their standard wording,
// and answers UNKNOWN for everything else rather than guessing: an
// unknown licence fails the release until a person reads it.
func classify(text string) string {
	t := strings.ToLower(strings.Join(strings.Fields(text), " "))
	has := func(s string) bool { return strings.Contains(t, s) }
	switch {
	case has("gnu affero general public license"):
		return "AGPL-3.0"
	case has("gnu lesser general public license"):
		return "LGPL"
	case has("gnu general public license"):
		return "GPL"
	case has("apache license") && has("version 2.0"):
		return "Apache-2.0"
	case has("mozilla public license") && has("2.0"):
		return "MPL-2.0"
	case has("permission is hereby granted, free of charge"):
		return "MIT"
	case has("permission to use, copy, modify, and/or distribute this software for any purpose") ||
		has("permission to use, copy, modify, and distribute this software for any purpose"):
		return "ISC"
	case has("redistribution and use in source and binary forms") && has("neither the name"):
		return "BSD-3-Clause"
	case has("redistribution and use in source and binary forms"):
		return "BSD-2-Clause"
	case has("this is free and unencumbered software released into the public domain"):
		return "Unlicense"
	}
	return "UNKNOWN"
}

// copyleft reports the licences a release of a statically linked binary
// must not carry without a decision by a person.
func copyleft(l string) bool { return l == "GPL" || l == "LGPL" || l == "AGPL-3.0" }

// licenseFile picks the file in a module directory that holds its licence.
func licenseFile(dir string) string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	var names []string
	for _, e := range entries {
		n := strings.ToUpper(e.Name())
		if e.IsDir() {
			continue
		}
		for _, p := range []string{"LICENSE", "LICENCE", "COPYING", "UNLICENSE"} {
			if strings.HasPrefix(n, p) {
				names = append(names, e.Name())
				break
			}
		}
	}
	sort.Strings(names)
	if len(names) == 0 {
		return ""
	}
	return names[0]
}

// moduleDir finds a module's source in the module cache, downloading it
// when this machine does not have it yet.
func moduleDir(path, version string) (string, error) {
	out, err := exec.Command("go", "mod", "download", "-json", path+"@"+version).Output()
	if err != nil {
		return "", fmt.Errorf("go mod download %s@%s: %w", path, version, err)
	}
	var m struct{ Dir, Error string }
	if err := json.Unmarshal(out, &m); err != nil {
		return "", err
	}
	if m.Error != "" {
		return "", fmt.Errorf("%s@%s: %s", path, version, m.Error)
	}
	return m.Dir, nil
}

func runLicenses(args []string) error {
	fs := flag.NewFlagSet("licenses", flag.ContinueOnError)
	dist := fs.String("dist", "dist", "directory holding the release binaries")
	allow := fs.String("allow-unknown", "", "comma-separated module paths whose licence a person has read and accepted")
	if err := fs.Parse(args); err != nil {
		return err
	}
	arts, err := readArtifacts(*dist)
	if err != nil {
		return err
	}
	accepted := map[string]bool{}
	for _, m := range strings.Split(*allow, ",") {
		if m = strings.TrimSpace(m); m != "" {
			accepted[m] = true
		}
	}
	var rows []LicenseRow
	var texts strings.Builder
	var problems []string
	for _, d := range collectDeps(arts) {
		dir, err := moduleDir(d.Path, d.Version)
		if err != nil {
			return err
		}
		row := LicenseRow{Module: d.Path, Version: d.Version, License: "UNKNOWN"}
		if f := licenseFile(dir); f != "" {
			data, err := os.ReadFile(filepath.Join(dir, f))
			if err != nil {
				return err
			}
			row.File, row.License = f, classify(string(data))
			fmt.Fprintf(&texts, "================================================================================\n%s %s — %s\n================================================================================\n%s\n\n", d.Path, d.Version, row.License, strings.TrimSpace(string(data)))
		}
		switch {
		case copyleft(row.License):
			problems = append(problems, fmt.Sprintf("%s %s is %s (copyleft) and is linked into the binary", d.Path, d.Version, row.License))
		case row.License == "UNKNOWN" && !accepted[d.Path]:
			problems = append(problems, fmt.Sprintf("%s %s: no licence recognised (file %q); read it, then pass -allow-unknown %s", d.Path, d.Version, row.File, d.Path))
		}
		rows = append(rows, row)
	}
	if err := writeJSON(filepath.Join(*dist, "licenses.json"), struct {
		Modules []LicenseRow `json:"modules"`
	}{rows}); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(*dist, "THIRD-PARTY-LICENSES.txt"), []byte(texts.String()), 0o644); err != nil {
		return err
	}
	counts := map[string]int{}
	for _, r := range rows {
		counts[r.License]++
	}
	var keys []string
	for k := range counts {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Printf("  %-14s %d\n", k, counts[k])
	}
	if len(problems) > 0 {
		return fmt.Errorf("%d licence problem(s):\n  %s", len(problems), strings.Join(problems, "\n  "))
	}
	return nil
}

// loadLicenseIndex reads licenses.json into module@version -> licence.
// A missing or unreadable file gives an empty index: the SBOM then says
// NOASSERTION rather than failing, and the licences gate is where a
// missing inventory is an error.
func loadLicenseIndex(path string) map[string]string {
	idx := map[string]string{}
	data, err := os.ReadFile(path)
	if err != nil {
		return idx
	}
	var f struct{ Modules []LicenseRow }
	if json.Unmarshal(data, &f) != nil {
		return idx
	}
	for _, r := range f.Modules {
		idx[r.Module+"@"+r.Version] = r.License
	}
	return idx
}
