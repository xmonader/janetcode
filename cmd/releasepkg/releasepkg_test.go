package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestClassify(t *testing.T) {
	for want, text := range map[string]string{
		"MIT":          "MIT License\n\nPermission is hereby granted, free of charge, to any person obtaining a copy",
		"Apache-2.0":   "Apache License\nVersion 2.0, January 2004",
		"BSD-3-Clause": "Redistribution and use in source and binary forms, with or without modification, are permitted provided that ... Neither the name of the copyright holder",
		"BSD-2-Clause": "Redistribution and use in source and binary forms, with or without modification, are permitted",
		"ISC":          "Permission to use, copy, modify, and/or distribute this software for any purpose with or without fee",
		"MPL-2.0":      "Mozilla Public License Version 2.0",
		"GPL":          "GNU GENERAL PUBLIC LICENSE Version 3",
		"LGPL":         "GNU Lesser General Public License",
		"AGPL-3.0":     "GNU AFFERO GENERAL PUBLIC LICENSE",
		"UNKNOWN":      "all rights reserved",
	} {
		if got := classify(text); got != want {
			t.Errorf("classify(%q) = %s, want %s", text[:20], got, want)
		}
	}
	if !copyleft("GPL") || !copyleft("AGPL-3.0") || copyleft("MIT") || copyleft("MPL-2.0") {
		t.Error("copyleft classification wrong")
	}
}

func TestReadSumsRejectsMalformed(t *testing.T) {
	dir := t.TempDir()
	good := strings.Repeat("a", 64)
	for name, body := range map[string]string{
		"short sum": "abc  file\n",
		"path":      good + "  ../etc/passwd\n",
		"duplicate": good + "  f\n" + good + "  f\n",
		"one space": good + " f\n",
	} {
		p := filepath.Join(dir, "S")
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := readSums(p); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	p := filepath.Join(dir, "S")
	os.WriteFile(p, []byte(good+"  f\n\n"), 0o644)
	if m, err := readSums(p); err != nil || m["f"] != good {
		t.Fatalf("good = %v %v", m, err)
	}
}

// tinyBinary builds a real Go binary (so debug/buildinfo can read it)
// into dir under a release name.
func tinyBinary(t *testing.T, dir, name string) {
	t.Helper()
	src := t.TempDir()
	os.WriteFile(filepath.Join(src, "go.mod"), []byte("module example.test/tiny\n\ngo 1.25\n"), 0o644)
	os.WriteFile(filepath.Join(src, "main.go"), []byte("package main\nfunc main() {}\n"), 0o644)
	cmd := exec.Command("go", "build", "-trimpath", "-buildvcs=false", "-o", filepath.Join(dir, name), ".")
	cmd.Dir = src
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
}

func TestSumsAndVerifyCatchTampering(t *testing.T) {
	dir := t.TempDir()
	tinyBinary(t, dir, "janetcode-v0-linux-amd64")
	os.WriteFile(filepath.Join(dir, "provenance.json"), []byte("{}"), 0o644)
	if err := runSums([]string{"-dist", dir}); err != nil {
		t.Fatal(err)
	}
	// The provenance is malformed and the evidence files are missing: all reported.
	problems, _ := verifyDir(dir, false, "")
	joined := strings.Join(problems, "\n")
	for _, want := range []string{"sbom.spdx.json is not covered", "schema"} {
		if !strings.Contains(joined, want) {
			t.Errorf("problems lack %q:\n%s", want, joined)
		}
	}
	// Tamper with the binary after summing.
	f := filepath.Join(dir, "janetcode-v0-linux-amd64")
	data, _ := os.ReadFile(f)
	data[len(data)-1] ^= 0xff
	os.WriteFile(f, data, 0o755)
	problems, _ = verifyDir(dir, false, "")
	if !strings.Contains(strings.Join(problems, "\n"), "janetcode-v0-linux-amd64: sha256 is") {
		t.Errorf("tampered binary not caught:\n%v", problems)
	}
	// A file nobody vouches for.
	os.WriteFile(filepath.Join(dir, "extra.txt"), []byte("x"), 0o644)
	problems, _ = verifyDir(dir, false, "")
	if !strings.Contains(strings.Join(problems, "\n"), "extra.txt is in the directory but not in SHA256SUMS") {
		t.Errorf("unlisted file not caught:\n%v", problems)
	}
	// A signature is required when asked for.
	problems, _ = verifyDir(dir, true, "")
	if !strings.Contains(strings.Join(problems, "\n"), "signature is required") {
		t.Errorf("missing signature not caught:\n%v", problems)
	}
}

func TestProvenanceRefusesUnstampedBinary(t *testing.T) {
	dir := t.TempDir()
	tinyBinary(t, dir, "janetcode-v0-linux-amd64")
	_, err := buildProvenance(dir, "v0", "src", false)
	if err == nil || !strings.Contains(err.Error(), "no embedded VCS revision") {
		t.Fatalf("err = %v", err)
	}
}

func TestSBOMIsSPDXAndDeterministic(t *testing.T) {
	dir := t.TempDir()
	tinyBinary(t, dir, "janetcode-v0-linux-amd64")
	arts, err := readArtifacts(dir)
	if err != nil {
		t.Fatal(err)
	}
	deps := []dep{{Path: "github.com/x/y", Version: "v1.2.3", Sum: "h1:abc", Targets: []string{"linux/amd64"}}}
	a := buildSPDX("v0", "deadbeef", "2026-01-01T00:00:00Z", arts[0].info.Main, deps, map[string]string{"github.com/x/y@v1.2.3": "MIT"})
	b := buildSPDX("v0", "deadbeef", "2026-01-01T00:00:00Z", arts[0].info.Main, deps, map[string]string{"github.com/x/y@v1.2.3": "MIT"})
	if a.SPDXVersion != "SPDX-2.3" || len(a.Packages) != 2 || a.Packages[1].LicenseConcluded != "MIT" {
		t.Fatalf("doc = %+v", a)
	}
	if a.Packages[1].ExternalRefs[0].Locator != "pkg:golang/github.com/x/y@v1.2.3" || a.Relationships[1].Type != "DEPENDS_ON" {
		t.Fatalf("refs = %+v %+v", a.Packages[1].ExternalRefs, a.Relationships)
	}
	if a.DocumentNamespace != b.DocumentNamespace || a.CreationInfo.Created != "2026-01-01T00:00:00Z" || a.CreationInfo.Created != b.CreationInfo.Created {
		t.Fatal("not deterministic")
	}
	// An unknown licence is NOASSERTION, never a guess.
	c := buildSPDX("v0", "d", "t", arts[0].info.Main, deps, map[string]string{"github.com/x/y@v1.2.3": "UNKNOWN"})
	if c.Packages[1].LicenseConcluded != "NOASSERTION" {
		t.Fatalf("unknown = %s", c.Packages[1].LicenseConcluded)
	}
}
