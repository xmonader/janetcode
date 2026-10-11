package depcheck

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"
	"unicode"
)

// HTTPRegistry looks names up in the public registries: the Go module
// proxy, the npm registry and PyPI's JSON API. The base URLs are fields
// so a test can point them at a fake server.
type HTTPRegistry struct {
	Client  *http.Client
	GoProxy string // default https://proxy.golang.org
	NPM     string // default https://registry.npmjs.org
	PyPI    string // default https://pypi.org
}

// errStatus is a registry answer that says nothing about the name.
var errStatus = errors.New("registry answered with an unexpected status")

func (r *HTTPRegistry) client() *http.Client {
	if r.Client != nil {
		return r.Client
	}
	return http.DefaultClient
}

func orDefault(s, d string) string {
	if s == "" {
		return d
	}
	return strings.TrimRight(s, "/")
}

// Lookup implements Registry.
func (r *HTTPRegistry) Lookup(ctx context.Context, eco, name string) (Info, error) {
	switch eco {
	case Go:
		return r.lookupGo(ctx, name)
	case NPM:
		return r.lookupNPM(ctx, name)
	case PyPI:
		return r.lookupPyPI(ctx, name)
	}
	return Info{}, fmt.Errorf("unknown ecosystem %q", eco)
}

// get fetches u. found is false for 404 and 410, the two answers that
// mean "no such name"; any other non-200 is an error.
func (r *HTTPRegistry) get(ctx context.Context, u string) (body []byte, found bool, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, false, err
	}
	resp, err := r.client().Do(req)
	if err != nil {
		return nil, false, err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
		b, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
		return b, true, err
	case http.StatusNotFound, http.StatusGone:
		return nil, false, nil
	}
	return nil, false, fmt.Errorf("%w: %s %d", errStatus, u, resp.StatusCode)
}

// lookupGo asks the module proxy for the module's tagged versions and
// dates the lowest one. A module with no tags is dated by its latest
// pseudo-version, which is the newest commit rather than the first, so
// it is not dated at all.
func (r *HTTPRegistry) lookupGo(ctx context.Context, mod string) (Info, error) {
	base := orDefault(r.GoProxy, "https://proxy.golang.org") + "/" + escapeModule(mod)
	body, found, err := r.get(ctx, base+"/@v/list")
	if err != nil || !found {
		return Info{Exists: found}, err
	}
	var versions []string
	for _, v := range strings.Fields(string(body)) {
		if strings.HasPrefix(v, "v") {
			versions = append(versions, v)
		}
	}
	if len(versions) == 0 {
		// No tags: the proxy still has the module if @latest answers.
		_, found, err := r.get(ctx, base+"/@latest")
		return Info{Exists: found}, err
	}
	first := versions[0]
	for _, v := range versions[1:] {
		if semverLess(v, first) {
			first = v
		}
	}
	body, found, err = r.get(ctx, base+"/@v/"+first+".info")
	if err != nil || !found {
		return Info{Exists: true}, nil
	}
	var info struct{ Time time.Time }
	_ = json.Unmarshal(body, &info)
	return Info{Exists: true, FirstPublished: info.Time}, nil
}

// escapeModule is the proxy's case encoding: an upper-case letter is "!"
// and its lower case.
func escapeModule(mod string) string {
	var sb strings.Builder
	for _, c := range mod {
		if unicode.IsUpper(c) {
			sb.WriteByte('!')
			sb.WriteRune(unicode.ToLower(c))
			continue
		}
		sb.WriteRune(c)
	}
	return sb.String()
}

// semverLess orders vMAJOR.MINOR.PATCH[-pre] well enough to find the
// first release: numbers compared as numbers, a pre-release before its
// release.
func semverLess(a, b string) bool {
	pa, prea := splitSemver(a)
	pb, preb := splitSemver(b)
	for i := 0; i < 3; i++ {
		if pa[i] != pb[i] {
			return pa[i] < pb[i]
		}
	}
	switch {
	case prea == preb:
		return false
	case prea == "":
		return false
	case preb == "":
		return true
	}
	return prea < preb
}

func splitSemver(v string) ([3]int, string) {
	v = strings.TrimPrefix(v, "v")
	v, _, _ = strings.Cut(v, "+")
	core, pre, _ := strings.Cut(v, "-")
	var n [3]int
	for i, part := range strings.SplitN(core, ".", 3) {
		n[i], _ = strconv.Atoi(part)
	}
	return n, pre
}

// lookupNPM reads the package document's time.created.
func (r *HTTPRegistry) lookupNPM(ctx context.Context, name string) (Info, error) {
	u := orDefault(r.NPM, "https://registry.npmjs.org") + "/" + url.PathEscape(name)
	body, found, err := r.get(ctx, u)
	if err != nil || !found {
		return Info{Exists: found}, err
	}
	var doc struct {
		Time map[string]string `json:"time"`
	}
	_ = json.Unmarshal(body, &doc)
	t, _ := time.Parse(time.RFC3339, doc.Time["created"])
	return Info{Exists: true, FirstPublished: t}, nil
}

// lookupPyPI dates the earliest file of any release.
func (r *HTTPRegistry) lookupPyPI(ctx context.Context, name string) (Info, error) {
	u := orDefault(r.PyPI, "https://pypi.org") + "/pypi/" + url.PathEscape(name) + "/json"
	body, found, err := r.get(ctx, u)
	if err != nil || !found {
		return Info{Exists: found}, err
	}
	var doc struct {
		Releases map[string][]struct {
			Upload time.Time `json:"upload_time_iso_8601"`
		} `json:"releases"`
	}
	_ = json.Unmarshal(body, &doc)
	var first time.Time
	for _, files := range doc.Releases {
		for _, f := range files {
			if !f.Upload.IsZero() && (first.IsZero() || f.Upload.Before(first)) {
				first = f.Upload
			}
		}
	}
	return Info{Exists: true, FirstPublished: first}, nil
}

// GoPrivate reports whether a module path matches a GOPRIVATE or
// GONOPROXY pattern (comma-separated globs matched against the path's
// leading elements, as the go command does). Those modules are not on
// the public proxy by design, so they are not looked up.
func GoPrivate(getenv func(string) string, mod string) bool {
	for _, key := range []string{"GOPRIVATE", "GONOPROXY"} {
		for _, pat := range strings.Split(getenv(key), ",") {
			if pat = strings.TrimSpace(pat); pat != "" && matchPrefix(pat, mod) {
				return true
			}
		}
	}
	return false
}

// matchPrefix matches glob against the first as many elements of target
// as the glob has.
func matchPrefix(glob, target string) bool {
	n := strings.Count(glob, "/") + 1
	parts := strings.SplitN(target, "/", n+1)
	if len(parts) < n {
		return false
	}
	ok, _ := path.Match(glob, strings.Join(parts[:n], "/"))
	return ok
}

// GoProxyURL is the first proxy GOPROXY names, "" for the default, or
// "off" when the go command itself would not use one.
func GoProxyURL(getenv func(string) string) string {
	for _, p := range strings.FieldsFunc(getenv("GOPROXY"), func(r rune) bool { return r == ',' || r == '|' }) {
		switch p = strings.TrimSpace(p); p {
		case "direct", "off":
			return "off"
		case "":
			continue
		default:
			return p
		}
	}
	return ""
}
