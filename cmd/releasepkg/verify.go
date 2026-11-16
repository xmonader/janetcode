package main

import (
	"bufio"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// sumsFile and sigFile are the checksum list and its detached signature.
const (
	sumsFile = "SHA256SUMS"
	sigFile  = "SHA256SUMS.asc"
)

// evidenceFiles must be listed in SHA256SUMS: a checksum list that does
// not cover the provenance and the bill of materials lets either be
// swapped without a sum failing.
var evidenceFiles = []string{"provenance.json", "sbom.spdx.json", "licenses.json", "THIRD-PARTY-LICENSES.txt"}

func runSums(args []string) error {
	fs := flag.NewFlagSet("sums", flag.ContinueOnError)
	dist := fs.String("dist", "dist", "directory to sum")
	if err := fs.Parse(args); err != nil {
		return err
	}
	entries, err := os.ReadDir(*dist)
	if err != nil {
		return err
	}
	var lines []string
	for _, e := range entries {
		if e.IsDir() || e.Name() == sumsFile || e.Name() == sigFile {
			continue
		}
		sum, _, err := fileSHA256(filepath.Join(*dist, e.Name()))
		if err != nil {
			return err
		}
		lines = append(lines, sum+"  "+e.Name())
	}
	sort.Slice(lines, func(i, j int) bool {
		return strings.SplitN(lines[i], "  ", 2)[1] < strings.SplitN(lines[j], "  ", 2)[1]
	})
	// A signature over the old list does not cover the new one.
	_ = os.Remove(filepath.Join(*dist, sigFile))
	return os.WriteFile(filepath.Join(*dist, sumsFile), []byte(strings.Join(lines, "\n")+"\n"), 0o644)
}

// readSums parses a sha256sum-format file.
func readSums(path string) (map[string]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	out := map[string]string{}
	sc := bufio.NewScanner(f)
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		sum, name, ok := strings.Cut(line, "  ")
		if !ok || len(sum) != 64 || strings.ContainsAny(name, "/\\") {
			return nil, fmt.Errorf("%s:%d: not a \"<sha256>  <file name>\" line", path, n)
		}
		name = strings.TrimPrefix(name, "*")
		if _, dup := out[name]; dup {
			return nil, fmt.Errorf("%s:%d: %s is listed twice", path, n, name)
		}
		out[name] = sum
	}
	return out, sc.Err()
}

func runVerify(args []string) error {
	fs := flag.NewFlagSet("verify", flag.ContinueOnError)
	dist := fs.String("dist", "dist", "directory to verify")
	requireSig := fs.Bool("require-signature", false, "fail when SHA256SUMS.asc is missing")
	fingerprint := fs.String("fingerprint", "", "the signing key's fingerprint; the signature must be by this key, not merely by a key in the keyring")
	if err := fs.Parse(args); err != nil {
		return err
	}
	problems, notes := verifyDir(*dist, *requireSig, *fingerprint)
	for _, n := range notes {
		fmt.Println("  ok   ", n)
	}
	if len(problems) > 0 {
		for _, p := range problems {
			fmt.Println("  FAIL ", p)
		}
		return fmt.Errorf("%d verification problem(s)", len(problems))
	}
	return nil
}

// verifyDir checks a dist directory and returns what is wrong and what
// was confirmed. Every check that can run does, so one failure does not
// hide the next.
func verifyDir(dir string, requireSig bool, fingerprint string) (problems, notes []string) {
	fail := func(f string, a ...any) { problems = append(problems, fmt.Sprintf(f, a...)) }
	ok := func(f string, a ...any) { notes = append(notes, fmt.Sprintf(f, a...)) }

	sums, err := readSums(filepath.Join(dir, sumsFile))
	if err != nil {
		fail("%v", err)
		return
	}
	names := make([]string, 0, len(sums))
	for n := range sums {
		names = append(names, n)
	}
	sort.Strings(names)
	checked := 0
	for _, n := range names {
		got, _, err := fileSHA256(filepath.Join(dir, n))
		switch {
		case err != nil:
			fail("%s is listed in %s but cannot be read: %v", n, sumsFile, err)
		case got != sums[n]:
			fail("%s: sha256 is %s, %s says %s", n, got, sumsFile, sums[n])
		default:
			checked++
		}
	}
	ok("%d files match %s", checked, sumsFile)

	// Every file in the directory is covered by the list.
	if entries, err := os.ReadDir(dir); err == nil {
		for _, e := range entries {
			if e.IsDir() || e.Name() == sumsFile || e.Name() == sigFile {
				continue
			}
			if _, listed := sums[e.Name()]; !listed {
				fail("%s is in the directory but not in %s, so nothing vouches for it", e.Name(), sumsFile)
			}
		}
	}
	for _, f := range evidenceFiles {
		if _, listed := sums[f]; !listed {
			fail("%s is not covered by %s: the evidence could be swapped without a sum failing", f, sumsFile)
		}
	}

	// The provenance agrees with the binaries.
	prov, err := readProvenance(filepath.Join(dir, "provenance.json"))
	if err != nil {
		fail("%v", err)
	} else {
		arts, err := readArtifacts(dir)
		if err != nil {
			fail("%v", err)
		} else {
			recorded := map[string]Artifact{}
			for _, a := range prov.Artifacts {
				recorded[a.Name] = a
			}
			for _, a := range arts {
				r, found := recorded[a.Name]
				switch {
				case !found:
					fail("%s is not in provenance.json", a.Name)
				case r.SHA256 != a.SHA256:
					fail("%s: provenance.json records sha256 %s, the file is %s", a.Name, r.SHA256, a.SHA256)
				case a.Revision != prov.Commit:
					fail("%s embeds revision %s, provenance.json says the release is %s", a.Name, a.Revision, prov.Commit)
				case a.Modified:
					fail("%s was built from a modified tree", a.Name)
				case !a.Trimpath || a.CGO != "0":
					fail("%s was not built with -trimpath and CGO_ENABLED=0", a.Name)
				}
			}
			if len(recorded) != len(arts) {
				fail("provenance.json lists %d binaries, the directory has %d", len(recorded), len(arts))
			}
			ok("%d binaries embed commit %s, built by %s", len(arts), prov.Commit, prov.Go)
		}
	}

	// The signature, when there is one.
	sig := filepath.Join(dir, sigFile)
	switch _, serr := os.Stat(sig); {
	case serr != nil && requireSig:
		fail("%s is missing and a signature is required", sigFile)
	case serr != nil:
		ok("%s: not signed (checksums only)", sumsFile)
	default:
		if msg, err := gpgVerify(sig, filepath.Join(dir, sumsFile), fingerprint); err != nil {
			fail("%s: %v", sigFile, err)
		} else {
			ok("%s", msg)
		}
	}
	return problems, notes
}

// gpgVerify checks a detached signature with gpg and, when fingerprint is
// given, that the key that made it is that one. The status lines are
// read, not the human text, which changes between versions.
func gpgVerify(sig, file, fingerprint string) (string, error) {
	if _, err := exec.LookPath("gpg"); err != nil {
		return "", fmt.Errorf("gpg is not installed, so the signature cannot be checked")
	}
	out, err := exec.Command("gpg", "--status-fd", "1", "--verify", sig, file).CombinedOutput()
	text := string(out)
	if err != nil || !strings.Contains(text, "[GNUPG:] VALIDSIG") {
		return "", fmt.Errorf("signature does not verify: %s", strings.TrimSpace(lastLines(text, 3)))
	}
	var fp string
	for _, line := range strings.Split(text, "\n") {
		if f := strings.Fields(line); len(f) > 2 && f[1] == "VALIDSIG" {
			fp = f[2]
		}
	}
	if fingerprint != "" && !strings.EqualFold(strings.ReplaceAll(fingerprint, " ", ""), fp) {
		return "", fmt.Errorf("signed by key %s, not the expected %s", fp, fingerprint)
	}
	return "signature verifies (key " + fp + ")", nil
}

func lastLines(s string, n int) string { return tail(s, n) }
