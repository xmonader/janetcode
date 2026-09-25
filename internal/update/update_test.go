package update

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// fakeReleases serves the GitHub release layout: /releases/latest
// redirects to the tag, and the assets sit under /releases/download.
func fakeReleases(t *testing.T, tag string, bin []byte, sums string) *httptest.Server {
	t.Helper()
	asset := Asset(tag, runtime.GOOS, runtime.GOARCH)
	if sums == "" {
		sum := sha256.Sum256(bin)
		sums = fmt.Sprintf("%s  %s\n%s  janetcode-%s-plan9-mips\n", hex.EncodeToString(sum[:]), asset, hex.EncodeToString(make([]byte, 32)), tag)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/releases/latest", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/releases/tag/"+tag, http.StatusFound)
	})
	mux.HandleFunc("/releases/download/"+tag+"/SHA256SUMS", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(sums)) })
	mux.HandleFunc("/releases/download/"+tag+"/"+asset, func(w http.ResponseWriter, r *http.Request) { w.Write(bin) })
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func exeFile(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "janetcode")
	if err := os.WriteFile(p, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestRunInstallsANewerRelease(t *testing.T) {
	srv := fakeReleases(t, "v0.4.0", []byte("new binary"), "")
	exe := exeFile(t, "old binary")
	res, err := Run(context.Background(), srv.Client(), srv.URL, "v0.3.2", exe)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Updated || res.To != "v0.4.0" {
		t.Fatalf("res = %+v", res)
	}
	b, _ := os.ReadFile(exe)
	fi, _ := os.Stat(exe)
	if string(b) != "new binary" || fi.Mode().Perm()&0o111 == 0 {
		t.Fatalf("exe = %q mode %v", b, fi.Mode())
	}
	if left, _ := filepath.Glob(filepath.Join(filepath.Dir(exe), ".janetcode-update-*")); len(left) > 0 {
		t.Errorf("left behind %v", left)
	}
}

// A binary that does not match SHA256SUMS is never installed.
func TestRunRefusesAMismatchedChecksum(t *testing.T) {
	asset := Asset("v0.4.0", runtime.GOOS, runtime.GOARCH)
	srv := fakeReleases(t, "v0.4.0", []byte("tampered"), hex.EncodeToString(make([]byte, 32))+"  "+asset+"\n")
	exe := exeFile(t, "old binary")
	_, err := Run(context.Background(), srv.Client(), srv.URL, "v0.3.2", exe)
	if err == nil {
		t.Fatal("installed a binary that does not match its checksum")
	}
	if b, _ := os.ReadFile(exe); string(b) != "old binary" {
		t.Fatalf("exe changed to %q", b)
	}
}

func TestRunLeavesTheCurrentReleaseAlone(t *testing.T) {
	srv := fakeReleases(t, "v0.4.0", []byte("new binary"), "")
	for _, current := range []string{"v0.4.0", "v0.4.0-12-gabcdef", "v0.5.0"} {
		exe := exeFile(t, "old binary")
		res, err := Run(context.Background(), srv.Client(), srv.URL, current, exe)
		if err != nil || res.Updated {
			t.Errorf("%s: res %+v err %v", current, res, err)
		}
		if b, _ := os.ReadFile(exe); string(b) != "old binary" {
			t.Errorf("%s: exe replaced", current)
		}
	}
	// A dev build has no version to compare, and says so.
	if _, err := Run(context.Background(), srv.Client(), srv.URL, "dev-abc123", exeFile(t, "x")); err == nil {
		t.Error("a dev build was updated")
	}
}

func TestRunWithoutARelease(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	defer srv.Close()
	if _, err := Run(context.Background(), srv.Client(), srv.URL, "v0.1.0", exeFile(t, "x")); err == nil {
		t.Fatal("no error when nothing is published")
	}
}

func TestNewer(t *testing.T) {
	for _, c := range []struct {
		tag, cur    string
		newer, okay bool
	}{
		{"v0.10.0", "v0.9.9", true, true},
		{"v1.0.0", "v0.99.0-3-gabc", true, true},
		{"v0.3.0", "v0.3.0-dirty", false, true},
		{"v0.3.0", "dev", false, false},
	} {
		n, ok := Newer(c.tag, c.cur)
		if n != c.newer || ok != c.okay {
			t.Errorf("Newer(%s, %s) = %v %v", c.tag, c.cur, n, ok)
		}
	}
}
