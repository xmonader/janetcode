package update

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// ReceiptName is the file install.sh leaves beside the binary it put in
// place: which release it was and the checksum of the bytes written. It
// is what lets --uninstall tell a binary janetcode installed from one a
// person built and copied there themselves.
func ReceiptName(exe string) string {
	return filepath.Join(filepath.Dir(exe), "."+strings.TrimSuffix(filepath.Base(exe), ".exe")+".install")
}

// Receipt is the parsed receipt: key=value lines.
type Receipt struct {
	Version string
	SHA256  string
}

// ReadReceipt reads the receipt beside exe. A missing receipt is
// (nil, nil).
func ReadReceipt(exe string) (*Receipt, error) {
	data, err := os.ReadFile(ReceiptName(exe))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	r := &Receipt{}
	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		k, v, ok := strings.Cut(strings.TrimSpace(sc.Text()), "=")
		if !ok {
			continue
		}
		switch k {
		case "version":
			r.Version = v
		case "sha256":
			r.SHA256 = strings.ToLower(v)
		}
	}
	return r, nil
}

// WriteReceipt records that exe holds release tag. --update calls it
// after replacing the binary, so the receipt keeps naming what is there.
func WriteReceipt(exe, tag string) error {
	sum, err := fileSHA256(exe)
	if err != nil {
		return err
	}
	body := "installed-by=janetcode\nversion=" + tag + "\nsha256=" + sum + "\n"
	return os.WriteFile(ReceiptName(exe), []byte(body), 0o644)
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// UninstallPlan is what --uninstall will remove and what it leaves.
type UninstallPlan struct {
	// Binary is the executable to remove, and Receipt its receipt ("" if
	// there is none).
	Binary, Receipt string
	// Dirs are the config and data directories, removed only on purge.
	Dirs []string
	// Kept are the directories left in place because purge was not asked
	// for; they are named so nobody wonders where their sessions went.
	Kept []string
}

// PlanUninstall decides what --uninstall removes. The binary goes when
// janetcode installed it — a receipt whose checksum matches the file —
// or when it is a release build, which came from a release whoever put
// it there. A build from source without a receipt is refused: it was
// put there by `make install` or `go install`, and the tool that put it
// there is the one to take it away. The config and data directories are
// removed only when purge is set.
func PlanUninstall(exe, version string, dirs []string, purge bool) (UninstallPlan, error) {
	plan := UninstallPlan{Binary: exe}
	rc, err := ReadReceipt(exe)
	if err != nil {
		return plan, fmt.Errorf("reading %s: %w", ReceiptName(exe), err)
	}
	if rc != nil {
		plan.Receipt = ReceiptName(exe)
		sum, err := fileSHA256(exe)
		if err != nil {
			return plan, err
		}
		if rc.SHA256 != "" && rc.SHA256 != sum && isSourceBuild(version) {
			return plan, fmt.Errorf("%s is not the binary install.sh put there (its checksum differs from %s, and it is a build from source, %s); remove it with the tool that installed it, or rm it yourself", exe, plan.Receipt, version)
		}
	} else if isSourceBuild(version) {
		return plan, fmt.Errorf("%s is a build from source (%s) that janetcode did not install, so it is left alone; remove it the way it was installed (for `make install` or `go install`: rm %s)", exe, version, exe)
	}
	for _, d := range dirs {
		if d == "" {
			continue
		}
		if _, err := os.Stat(d); err != nil {
			continue
		}
		if purge {
			plan.Dirs = append(plan.Dirs, d)
		} else {
			plan.Kept = append(plan.Kept, d)
		}
	}
	return plan, nil
}

// isSourceBuild reports whether version names a local build rather than a
// release tag.
func isSourceBuild(version string) bool {
	return version == "" || version == "dev" || strings.HasPrefix(version, "dev-")
}

// Uninstall carries out a plan and returns one line per thing removed.
// On Windows a running executable cannot be deleted, only renamed, so it
// is moved aside to <exe>.old and the line says to delete that after
// exit.
func Uninstall(plan UninstallPlan) ([]string, error) {
	var done []string
	for _, d := range plan.Dirs {
		if err := os.RemoveAll(d); err != nil {
			return done, fmt.Errorf("removing %s: %w", d, err)
		}
		done = append(done, "removed "+d)
	}
	if runtime.GOOS == "windows" {
		old := plan.Binary + ".old"
		_ = os.Remove(old)
		if err := os.Rename(plan.Binary, old); err != nil {
			return done, fmt.Errorf("moving %s aside: %w", plan.Binary, err)
		}
		done = append(done, "moved "+plan.Binary+" to "+old+"; Windows cannot delete a running program, so delete that file once janetcode has exited")
	} else {
		if err := os.Remove(plan.Binary); err != nil && !errors.Is(err, os.ErrNotExist) {
			return done, fmt.Errorf("removing %s: %w", plan.Binary, err)
		}
		done = append(done, "removed "+plan.Binary)
	}
	// The receipt goes last: while anything it vouches for remains, a
	// rerun still finds it and can finish the job.
	if plan.Receipt != "" {
		if err := os.Remove(plan.Receipt); err != nil && !errors.Is(err, os.ErrNotExist) {
			return done, err
		}
	}
	return done, nil
}
