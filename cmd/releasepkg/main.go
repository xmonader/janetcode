// Command releasepkg builds and checks the release-level security and
// reproducibility evidence that ships beside the binaries: the
// provenance record (source commit, toolchain and flags, each artifact's
// digest and embedded build information), the SPDX software bill of
// materials, the licence inventory, the record of which gates ran on
// which commit, and the verification of all of it.
//
// It is standard library only, and reads what the Go toolchain already
// embeds in every binary (debug/buildinfo), so the evidence is derived
// from the artifacts themselves rather than from a second description of
// them. `make release-evidence` and `make verify-dist` are its callers.
package main

import (
	"fmt"
	"os"
)

const usage = `usage: releasepkg <command> [flags]

  provenance  write provenance.json for the binaries in a dist directory
  sbom        write sbom.spdx.json (SPDX 2.3) from the dependencies linked into them
  licenses    write licenses.json and THIRD-PARTY-LICENSES.txt; fail on copyleft or unknown licences
  sums        write SHA256SUMS over every file in the directory
  gates       run the release gates and write gates.json
  report      write RELEASE-EVIDENCE.md from the files above
  verify      check a dist directory: checksums, provenance, embedded build info, signature
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	cmd, args := os.Args[1], os.Args[2:]
	var err error
	switch cmd {
	case "provenance":
		err = runProvenance(args)
	case "sbom":
		err = runSBOM(args)
	case "licenses":
		err = runLicenses(args)
	case "sums":
		err = runSums(args)
	case "gates":
		err = runGates(args)
	case "report":
		err = runReport(args)
	case "verify":
		err = runVerify(args)
	case "-h", "--help", "help":
		fmt.Print(usage)
		return
	default:
		fmt.Fprintf(os.Stderr, "releasepkg: unknown command %q\n%s", cmd, usage)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "releasepkg %s: %v\n", cmd, err)
		os.Exit(1)
	}
}
