package repomap

import (
	"bufio"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

// A plan for a Go repository names standard-library API the code does not
// call yet — `FindAllStringSubmatchIndex` for a regexp fix — and the index,
// which only knows the repository's own files, called it missing
// (realrepo hugo-replacere). The toolchain lists every exported name of
// the standard library in $GOROOT/api; a name found there is known.

var (
	goAPIOnce  sync.Once
	goAPINames map[string]bool
	// goAPIName takes the name out of an api line: "pkg regexp, method
	// (*Regexp) Expand(...)", "pkg io, func Copy(...)", "pkg os, const
	// O_RDONLY int", "pkg net/http, type Client struct, Jar CookieJar".
	goAPIName = regexp.MustCompile(`^pkg [^,]+, (?:method \([^)]*\) |func |const |var |type )([A-Za-z_][A-Za-z0-9_]*)(?:[^,]*, ([A-Za-z_][A-Za-z0-9_]*))?`)
)

// goStdName reports whether name is exported API of the Go standard
// library of the toolchain on PATH. Without one it knows nothing.
func goStdName(name string) bool {
	goAPIOnce.Do(func() { goAPINames = loadGoAPI(goRoot()) })
	return goAPINames[name]
}

// goRoot finds a toolchain whose api files to read. GOTOOLCHAIN=local:
// in a module that asks for a newer Go, a plain `go env` tries to fetch
// that toolchain, which a sandbox without network refuses, and the plan
// check then knew no standard library at all (realrepo hugo). Any
// toolchain's api lists do: the names only grow.
func goRoot() string {
	if r := os.Getenv("GOROOT"); r != "" {
		return r
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "go", "env", "GOROOT")
	cmd.Env = append(os.Environ(), "GOTOOLCHAIN=local")
	if out, err := cmd.Output(); err == nil {
		if r := strings.TrimSpace(string(out)); r != "" {
			return r
		}
	}
	for _, r := range []string{"/usr/local/go", "/usr/lib/go", "/opt/homebrew/opt/go/libexec"} {
		if _, err := os.Stat(filepath.Join(r, "api")); err == nil {
			return r
		}
	}
	return ""
}

func loadGoAPI(root string) map[string]bool {
	names := map[string]bool{}
	if root == "" {
		return names
	}
	files, _ := filepath.Glob(filepath.Join(root, "api", "go1*.txt"))
	for _, p := range files {
		f, err := os.Open(p)
		if err != nil {
			continue
		}
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			if m := goAPIName.FindStringSubmatch(sc.Text()); m != nil {
				names[m[1]] = true
				if m[2] != "" {
					names[m[2]] = true // a struct field or interface method
				}
			}
		}
		f.Close()
	}
	return names
}
