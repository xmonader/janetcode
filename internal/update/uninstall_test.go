package update

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func writeExe(t *testing.T, dir string) string {
	t.Helper()
	exe := filepath.Join(dir, "janetcode")
	if err := os.WriteFile(exe, []byte("binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	return exe
}

func TestUninstallRefusesASourceBuildItDidNotInstall(t *testing.T) {
	exe := writeExe(t, t.TempDir())
	_, err := PlanUninstall(exe, "dev-5a51f3d096f2", nil, false)
	if err == nil || !strings.Contains(err.Error(), "build from source") {
		t.Fatalf("want a refusal naming the source build, got %v", err)
	}
	if _, err := os.Stat(exe); err != nil {
		t.Fatalf("the binary was touched: %v", err)
	}
}

func TestUninstallRemovesWhatInstallShPutThere(t *testing.T) {
	dir := t.TempDir()
	exe := writeExe(t, dir)
	if err := WriteReceipt(exe, "v0.3.0"); err != nil {
		t.Fatal(err)
	}
	cfg := filepath.Join(dir, "config")
	if err := os.MkdirAll(cfg, 0o700); err != nil {
		t.Fatal(err)
	}
	// Even a dev version string is removed when the receipt vouches for
	// the bytes: that is what the receipt is for.
	plan, err := PlanUninstall(exe, "dev-x", []string{cfg}, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Kept) != 1 || plan.Kept[0] != cfg || len(plan.Dirs) != 0 {
		t.Fatalf("without purge the config dir must be kept and named: %+v", plan)
	}
	done, err := Uninstall(plan)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		if _, err := os.Stat(exe); !os.IsNotExist(err) {
			t.Fatalf("binary still there: %v", err)
		}
	}
	if _, err := os.Stat(ReceiptName(exe)); !os.IsNotExist(err) {
		t.Fatalf("receipt still there: %v", err)
	}
	if _, err := os.Stat(cfg); err != nil {
		t.Fatalf("config dir removed without purge: %v", err)
	}
	if len(done) != 1 {
		t.Fatalf("want one line per removal, got %q", done)
	}
}

func TestUninstallPurgeRemovesTheDirectories(t *testing.T) {
	dir := t.TempDir()
	exe := writeExe(t, dir)
	cfg, data := filepath.Join(dir, "cfg"), filepath.Join(dir, "data")
	for _, d := range []string{cfg, data} {
		if err := os.MkdirAll(filepath.Join(d, "sub"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	plan, err := PlanUninstall(exe, "v0.3.0", []string{cfg, data, filepath.Join(dir, "missing")}, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Dirs) != 2 {
		t.Fatalf("want the two existing dirs, got %+v", plan)
	}
	if _, err := Uninstall(plan); err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{cfg, data} {
		if _, err := os.Stat(d); !os.IsNotExist(err) {
			t.Fatalf("%s survived purge", d)
		}
	}
}

func TestUninstallRefusesASourceBuildOverAnInstalledOne(t *testing.T) {
	dir := t.TempDir()
	exe := writeExe(t, dir)
	if err := WriteReceipt(exe, "v0.3.0"); err != nil {
		t.Fatal(err)
	}
	// Someone ran `go build -o ~/.local/bin/janetcode` over it since.
	if err := os.WriteFile(exe, []byte("different"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := PlanUninstall(exe, "dev-abc", nil, false); err == nil {
		t.Fatal("a source build that replaced the installed binary was removed")
	}
}
