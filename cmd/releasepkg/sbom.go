package main

import (
	"flag"
	"fmt"
	"path/filepath"
	"runtime/debug"
	"sort"
	"strings"
)

// dep is one module linked into at least one release binary.
type dep struct {
	Path, Version, Sum string
	// Targets lists the os/arch of the binaries it is linked into.
	Targets []string
}

// collectDeps is the union of the modules linked into the artifacts, by
// path and version: what ships, not what go.mod could have pulled in.
func collectDeps(arts []Artifact) []dep {
	byKey := map[string]*dep{}
	for _, a := range arts {
		for _, m := range a.info.Deps {
			if m.Replace != nil {
				m = m.Replace
			}
			k := m.Path + "@" + m.Version
			d := byKey[k]
			if d == nil {
				d = &dep{Path: m.Path, Version: m.Version, Sum: m.Sum}
				byKey[k] = d
			}
			d.Targets = append(d.Targets, a.GOOS+"/"+a.GOARCH)
		}
	}
	out := make([]dep, 0, len(byKey))
	for _, d := range byKey {
		sort.Strings(d.Targets)
		out = append(out, *d)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Path != out[j].Path {
			return out[i].Path < out[j].Path
		}
		return out[i].Version < out[j].Version
	})
	return out
}

// The SPDX 2.3 JSON document, with only the fields the format requires
// for a software bill of materials and the ones a consumer needs to
// match a package to an advisory (the purl).
type spdxDoc struct {
	SPDXVersion       string          `json:"spdxVersion"`
	DataLicense       string          `json:"dataLicense"`
	SPDXID            string          `json:"SPDXID"`
	Name              string          `json:"name"`
	DocumentNamespace string          `json:"documentNamespace"`
	CreationInfo      spdxCreation    `json:"creationInfo"`
	Packages          []spdxPackage   `json:"packages"`
	Relationships     []spdxRelations `json:"relationships"`
}

type spdxCreation struct {
	Created  string   `json:"created"`
	Creators []string `json:"creators"`
}

type spdxPackage struct {
	Name             string         `json:"name"`
	SPDXID           string         `json:"SPDXID"`
	VersionInfo      string         `json:"versionInfo"`
	DownloadLocation string         `json:"downloadLocation"`
	FilesAnalyzed    bool           `json:"filesAnalyzed"`
	LicenseConcluded string         `json:"licenseConcluded"`
	LicenseDeclared  string         `json:"licenseDeclared"`
	CopyrightText    string         `json:"copyrightText"`
	Comment          string         `json:"comment,omitempty"`
	ExternalRefs     []spdxExtRef   `json:"externalRefs,omitempty"`
	Checksums        []spdxChecksum `json:"checksums,omitempty"`
}

type spdxExtRef struct {
	Category string `json:"referenceCategory"`
	Type     string `json:"referenceType"`
	Locator  string `json:"referenceLocator"`
}

type spdxChecksum struct {
	Algorithm string `json:"algorithm"`
	Value     string `json:"checksumValue"`
}

type spdxRelations struct {
	Element string `json:"spdxElementId"`
	Type    string `json:"relationshipType"`
	Related string `json:"relatedSpdxElement"`
}

var spdxIDClean = strings.NewReplacer("/", "-", ".", "-", "_", "-", "~", "-", "+", "-", "!", "-", "@", "-")

func spdxID(path, version string) string {
	return "SPDXRef-Package-" + spdxIDClean.Replace(path+"-"+version)
}

func runSBOM(args []string) error {
	fs := flag.NewFlagSet("sbom", flag.ContinueOnError)
	dist := fs.String("dist", "dist", "directory holding the release binaries")
	out := fs.String("out", "", "output file (default <dist>/sbom.spdx.json)")
	version := fs.String("version", "", "release version (required)")
	licenses := fs.String("licenses", "", "licences.json written by releasepkg licenses (default <dist>/licenses.json when present)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *version == "" {
		return fmt.Errorf("-version is required")
	}
	if *out == "" {
		*out = filepath.Join(*dist, "sbom.spdx.json")
	}
	if *licenses == "" {
		*licenses = filepath.Join(*dist, "licenses.json")
	}
	arts, err := readArtifacts(*dist)
	if err != nil {
		return err
	}
	when, err := git("show", "-s", "--format=%cI", "HEAD")
	if err != nil {
		return err
	}
	commit, err := git("rev-parse", "HEAD")
	if err != nil {
		return err
	}
	lic := loadLicenseIndex(*licenses)
	return writeJSON(*out, buildSPDX(*version, commit, when, arts[0].info.Main, collectDeps(arts), lic))
}

func buildSPDX(version, commit, created string, main debug.Module, deps []dep, lic map[string]string) spdxDoc {
	root := spdxID(main.Path, version)
	doc := spdxDoc{
		SPDXVersion: "SPDX-2.3", DataLicense: "CC0-1.0", SPDXID: "SPDXRef-DOCUMENT",
		Name: "janetcode " + version,
		// A namespace unique to this commit; it is an identifier, not a URL that is fetched.
		DocumentNamespace: "https://github.com/xmonader/janetcode/spdx/" + commit,
		// The commit's own time, so rebuilding the document from the same
		// commit gives the same bytes.
		CreationInfo: spdxCreation{Created: created, Creators: []string{"Tool: janetcode-releasepkg"}},
	}
	doc.Packages = append(doc.Packages, spdxPackage{
		Name: main.Path, SPDXID: root, VersionInfo: version,
		DownloadLocation: "git+https://github.com/xmonader/janetcode@" + commit,
		LicenseConcluded: "NOASSERTION", LicenseDeclared: "NOASSERTION", CopyrightText: "NOASSERTION",
		Comment:      "the janetcode release binaries themselves",
		ExternalRefs: []spdxExtRef{{"PACKAGE-MANAGER", "purl", "pkg:golang/" + main.Path + "@" + version}},
	})
	doc.Relationships = append(doc.Relationships, spdxRelations{"SPDXRef-DOCUMENT", "DESCRIBES", root})
	for _, d := range deps {
		id := spdxID(d.Path, d.Version)
		license := "NOASSERTION"
		if l := lic[d.Path+"@"+d.Version]; l != "" && l != "UNKNOWN" {
			license = l
		}
		p := spdxPackage{
			Name: d.Path, SPDXID: id, VersionInfo: d.Version,
			DownloadLocation: "https://proxy.golang.org/" + d.Path + "/@v/" + d.Version + ".zip",
			LicenseConcluded: license, LicenseDeclared: license, CopyrightText: "NOASSERTION",
			Comment:      "linked into: " + strings.Join(d.Targets, ", "),
			ExternalRefs: []spdxExtRef{{"PACKAGE-MANAGER", "purl", "pkg:golang/" + d.Path + "@" + d.Version}},
		}
		if d.Sum != "" {
			// The go.sum hash is h1:<base64 sha256 of the module's file hashes>,
			// not a digest of the zip; it is kept as a note, not as a SPDX checksum.
			p.Comment += " · go.sum " + d.Sum
		}
		doc.Packages = append(doc.Packages, p)
		doc.Relationships = append(doc.Relationships, spdxRelations{root, "DEPENDS_ON", id})
	}
	return doc
}
