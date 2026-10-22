package update

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
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
		// A prerelease comes before its release, and a describe tail on
		// either does not change that.
		{"v0.3.0", "v0.3.0-rc1", true, true},
		{"v0.3.0", "v0.3.0-rc1-4-gabc123", true, true},
		{"v0.3.0", "v0.3.0-rc.2-dirty", true, true},
		{"v0.3.0-rc1", "v0.3.0", false, true},
		{"v0.3.0-rc2", "v0.3.0-rc1", true, true},
		{"v0.3.0-rc.10", "v0.3.0-rc.9", true, true},
		{"v0.3.0-rc.1", "v0.3.0-rc.1-2-gabc", false, true},
		{"v0.3.0", "v0.3.0-7-gabc123-dirty", false, true},
		{"v0.2.9", "v0.3.0-rc1", false, true},
	} {
		n, ok := Newer(c.tag, c.cur)
		if n != c.newer || ok != c.okay {
			t.Errorf("Newer(%s, %s) = %v %v", c.tag, c.cur, n, ok)
		}
	}
}

// install.sh follows the same release layout and refuses the same bad
// checksum: run it against the fake releases.
func TestInstallScript(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("install.sh is for Linux and macOS")
	}
	if _, err := exec.LookPath("curl"); err != nil {
		t.Skip("no curl")
	}
	script, _ := filepath.Abs("../../install.sh")
	run := func(srvURL, dir string) (string, error) {
		cmd := exec.Command("sh", script)
		cmd.Env = append(os.Environ(), "JANETCODE_RELEASES="+srvURL, "JANETCODE_INSTALL_DIR="+dir, "JANETCODE_VERSION=")
		out, err := cmd.CombinedOutput()
		return string(out), err
	}

	srv := fakeReleases(t, "v0.4.0", []byte("#!/bin/sh\necho hi\n"), "")
	dir := filepath.Join(t.TempDir(), "bin")
	out, err := run(srv.URL, dir)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	fi, err := os.Stat(filepath.Join(dir, "janetcode"))
	if err != nil || fi.Mode().Perm()&0o111 == 0 || !strings.Contains(out, "installed") || !strings.Contains(out, "v0.4.0") {
		t.Fatalf("not installed: %v\n%s", err, out)
	}
	// The receipt it leaves is the one --uninstall reads: even a binary
	// whose version reads as a source build is recognised by its bytes.
	installed := filepath.Join(dir, "janetcode")
	rc, err := ReadReceipt(installed)
	if err != nil || rc == nil || rc.Version != "v0.4.0" {
		t.Fatalf("install.sh left no usable receipt: %+v %v", rc, err)
	}
	if _, err := PlanUninstall(installed, "dev-x", nil, false); err != nil {
		t.Fatalf("--uninstall does not recognise what install.sh installed: %v", err)
	}
	if !strings.Contains(out, "janetcode --uninstall") {
		t.Fatalf("install.sh does not say how to uninstall:\n%s", out)
	}

	asset := Asset("v0.4.0", runtime.GOOS, runtime.GOARCH)
	bad := fakeReleases(t, "v0.4.0", []byte("tampered"), hex.EncodeToString(make([]byte, 32))+"  "+asset+"\n")
	dir2 := filepath.Join(t.TempDir(), "bin")
	out, err = run(bad.URL, dir2)
	if err == nil || !strings.Contains(out, "does not match") {
		t.Fatalf("a bad checksum installed: %v\n%s", err, out)
	}
	if _, err := os.Stat(filepath.Join(dir2, "janetcode")); err == nil {
		t.Fatal("the mismatched binary was written")
	}
}

// A receipt that cannot be put in place must not leave a partial or temp
// receipt behind. The rename of the receipt is made to fail by a stub mv
// ahead of the real one on PATH.
func TestInstallScriptLeavesNoReceiptLeftoversOnFailure(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("install.sh is for Linux and macOS")
	}
	if _, err := exec.LookPath("curl"); err != nil {
		t.Skip("no curl")
	}
	realMv, err := exec.LookPath("mv")
	if err != nil {
		t.Skip("no mv")
	}
	stubs := t.TempDir()
	stub := "#!/bin/sh\nfor last; do :; done\ncase \"$last\" in *.janetcode.install) exit 1 ;; esac\nexec " + realMv + " \"$@\"\n"
	if err := os.WriteFile(filepath.Join(stubs, "mv"), []byte(stub), 0o755); err != nil {
		t.Fatal(err)
	}
	script, _ := filepath.Abs("../../install.sh")
	srv := fakeReleases(t, "v0.4.0", []byte("#!/bin/sh\necho hi\n"), "")
	dir := filepath.Join(t.TempDir(), "bin")
	cmd := exec.Command("sh", script)
	cmd.Env = append(os.Environ(), "PATH="+stubs+string(os.PathListSeparator)+os.Getenv("PATH"),
		"JANETCODE_RELEASES="+srv.URL, "JANETCODE_INSTALL_DIR="+dir, "JANETCODE_VERSION=")
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("install.sh succeeded although the receipt could not be renamed:\n%s", out)
	}
	left, _ := filepath.Glob(filepath.Join(dir, ".janetcode.*"))
	if len(left) != 0 {
		t.Fatalf("install.sh left receipt leftovers behind: %v", left)
	}
}
