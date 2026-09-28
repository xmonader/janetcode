// Package update replaces the running janetcode with the latest release:
// it asks GitHub which tag is latest, downloads that release's binary for
// this platform with its SHA256SUMS, checks the one against the other,
// and swaps the new binary in beside the old one. install.sh does the
// same from a shell, for a machine that has no janetcode yet.
package update

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// Releases is where releases are published. JANETCODE_RELEASES points at
// a mirror with the same layout (<base>/releases/latest redirecting to
// <base>/releases/tag/<tag>, assets under <base>/releases/download/<tag>/).
const Releases = "https://github.com/xmonader/janetcode"

// maxBinary bounds a download, so a wrong URL cannot fill the disk.
const maxBinary = 256 << 20

// Base is the release location to use.
func Base() string {
	if v := strings.TrimSpace(os.Getenv("JANETCODE_RELEASES")); v != "" {
		return strings.TrimRight(v, "/")
	}
	return Releases
}

// Asset is the release file name for a tag on this platform, as make
// dist names it.
func Asset(tag, goos, goarch string) string {
	name := "janetcode-" + tag + "-" + goos + "-" + goarch
	if goos == "windows" {
		name += ".exe"
	}
	return name
}

// Latest returns the tag of the latest release. It reads the redirect
// GitHub answers /releases/latest with, which, unlike the API, has no
// rate limit for unauthenticated callers.
func Latest(ctx context.Context, client *http.Client, base string) (string, error) {
	c := *client
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/releases/latest", nil)
	if err != nil {
		return "", err
	}
	resp, err := c.Do(req)
	if err != nil {
		return "", fmt.Errorf("asking for the latest release: %w", err)
	}
	resp.Body.Close()
	loc := resp.Header.Get("Location")
	if resp.StatusCode/100 != 3 || loc == "" {
		return "", fmt.Errorf("asking for the latest release: %s answered %d with no release to point at (has one been published?)", base, resp.StatusCode)
	}
	_, tag, ok := strings.Cut(loc, "/releases/tag/")
	if !ok || tag == "" || strings.ContainsAny(tag, "/?#") {
		return "", fmt.Errorf("asking for the latest release: unexpected redirect to %s", loc)
	}
	return tag, nil
}

var (
	semver = regexp.MustCompile(`^v(\d+)\.(\d+)\.(\d+)(.*)$`)
	// describeTail is what git describe appends past a tag: commits since
	// it, the commit, and whether the tree was dirty.
	describeTail = regexp.MustCompile(`(-\d+-g[0-9a-f]+)?(-dirty)?$`)
)

// Newer reports whether tag is a later release than current. A build
// from a commit past a tag ("v0.3.0-12-gabc") counts as that tag, so it
// is not "updated" to the release it already contains. A prerelease
// ("v0.3.0-rc1") comes before its release, as in semver, so an rc build
// does update to the release it led to. ok is false when current is not
// a release version at all (a dev build).
func Newer(tag, current string) (newer, ok bool) {
	t, c := semver.FindStringSubmatch(tag), semver.FindStringSubmatch(current)
	if t == nil || c == nil {
		return false, false
	}
	for i := 1; i <= 3; i++ {
		a, _ := strconv.Atoi(t[i])
		b, _ := strconv.Atoi(c[i])
		if a != b {
			return a > b, true
		}
	}
	return comparePre(prerelease(t[4]), prerelease(c[4])) > 0, true
}

// prerelease is the semver prerelease in what follows vX.Y.Z, with git
// describe's tail and any +build metadata dropped: "-rc1-3-gabc-dirty"
// is "rc1", "-12-gabc" is "".
func prerelease(rest string) string {
	rest, _, _ = strings.Cut(rest, "+")
	rest = describeTail.ReplaceAllString(rest, "")
	return strings.TrimPrefix(rest, "-")
}

// comparePre orders two prereleases by semver's rules: none is later than
// any; otherwise dot-separated identifiers compare in turn, numeric ones
// as numbers and below alphanumeric ones, and a longer list wins a tie.
func comparePre(a, b string) int {
	switch {
	case a == b:
		return 0
	case a == "":
		return 1
	case b == "":
		return -1
	}
	as, bs := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(as) && i < len(bs); i++ {
		x, xerr := strconv.ParseUint(as[i], 10, 64)
		y, yerr := strconv.ParseUint(bs[i], 10, 64)
		switch {
		case xerr == nil && yerr == nil:
			if x != y {
				if x > y {
					return 1
				}
				return -1
			}
		case xerr == nil:
			return -1
		case yerr == nil:
			return 1
		default:
			if c := strings.Compare(as[i], bs[i]); c != 0 {
				return c
			}
		}
	}
	return len(as) - len(bs)
}

// Result says what Run did.
type Result struct {
	From, To string
	Path     string
	Updated  bool
}

// Run updates the binary at exe from version current to the latest
// release. It changes nothing unless the new binary's checksum matches.
func Run(ctx context.Context, client *http.Client, base, current, exe string) (Result, error) {
	res := Result{From: current, Path: exe}
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Minute}
	}
	tag, err := Latest(ctx, client, base)
	if err != nil {
		return res, err
	}
	res.To = tag
	newer, ok := Newer(tag, current)
	if !ok {
		return res, fmt.Errorf("this janetcode (%s) is not a release build, so there is nothing to compare %s with; install the release with install.sh, or rebuild from source", current, tag)
	}
	if !newer {
		return res, nil
	}
	asset := Asset(tag, runtime.GOOS, runtime.GOARCH)
	sums, err := fetch(ctx, client, base+"/releases/download/"+tag+"/SHA256SUMS", 1<<20)
	if err != nil {
		return res, err
	}
	want, err := sumFor(sums, asset)
	if err != nil {
		return res, fmt.Errorf("release %s: %w", tag, err)
	}
	bin, err := fetch(ctx, client, base+"/releases/download/"+tag+"/"+asset, maxBinary)
	if err != nil {
		return res, err
	}
	got := sha256.Sum256(bin)
	if hex.EncodeToString(got[:]) != want {
		return res, fmt.Errorf("release %s: %s does not match its SHA256SUMS entry; nothing was changed", tag, asset)
	}
	if err := replace(exe, bin); err != nil {
		return res, fmt.Errorf("installing %s over %s: %w", tag, exe, err)
	}
	res.Updated = true
	return res, nil
}

func fetch(ctx context.Context, client *http.Client, url string, limit int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("downloading %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("downloading %s: %s", url, resp.Status)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, fmt.Errorf("downloading %s: %w", url, err)
	}
	if int64(len(b)) > limit {
		return nil, fmt.Errorf("downloading %s: larger than %d MiB", url, limit>>20)
	}
	return b, nil
}

// sumFor finds asset's checksum in a sha256sum listing.
func sumFor(sums []byte, asset string) (string, error) {
	sc := bufio.NewScanner(bytes.NewReader(sums))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) == 2 && strings.TrimPrefix(f[1], "*") == asset && len(f[0]) == 64 {
			return strings.ToLower(f[0]), nil
		}
	}
	return "", fmt.Errorf("no %s in SHA256SUMS (this platform may have no build)", asset)
}

// replace writes bin beside exe and renames it over exe, so an interrupted
// update leaves the old binary whole. A running executable cannot be
// renamed over on Windows; there the old one is moved aside first.
func replace(exe string, bin []byte) error {
	dir := filepath.Dir(exe)
	tmp, err := os.CreateTemp(dir, ".janetcode-update-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(bin); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o755); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if runtime.GOOS != "windows" {
		return os.Rename(tmp.Name(), exe)
	}
	old := exe + ".old"
	_ = os.Remove(old)
	moved := true
	if err := os.Rename(exe, old); err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		moved = false
	}
	if err := os.Rename(tmp.Name(), exe); err != nil {
		// The old binary is aside; put it back, or janetcode is gone
		// from where it was installed.
		if moved {
			_ = os.Rename(old, exe)
		}
		return err
	}
	return nil
}
