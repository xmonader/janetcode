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

func goRoot() string {
	if r := os.Getenv("GOROOT"); r != "" {
		return r
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "go", "env", "GOROOT").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
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
