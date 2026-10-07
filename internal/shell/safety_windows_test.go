//go:build windows

package shell

import (
	"path/filepath"
	"strings"
	"testing"
)

// On Windows a path that starts with a slash is rooted on the current
// drive. The classifier took it for a relative path, so writes to
// /etc/hosts or C:\Users were ordinary commands there.
func TestWindowsSlashRootedPathsEscape(t *testing.T) {
	root := t.TempDir()
	for _, cmd := range []string{
		"echo x > /etc/hosts",
		`echo x > \Windows\win.ini`,
		"rm -rf /Users/someone/project",
		"cp a.txt /somewhere/else.txt",
	} {
		risk, why := Classify(cmd, root)
		if risk < RiskEscapes {
			t.Errorf("%q: risk %v (%q), want at least escapes", cmd, risk, why)
		}
	}
	for _, cmd := range []string{"go test ./... 2>/dev/null", "dir > NUL", "echo x > sub/file.txt"} {
		if risk, why := Classify(cmd, root); risk >= RiskEscapes {
			t.Errorf("%q: risk %v (%q), want ordinary", cmd, risk, why)
		}
	}
	vol := filepath.VolumeName(root)
	for _, cmd := range []string{"rm -rf " + vol + `\`, `rm -rf \Windows`, "rm -rf " + vol + `\Users`} {
		if risk, why := Classify(cmd, root); risk != RiskCatastrophic {
			t.Errorf("%q: risk %v (%q), want catastrophic", cmd, risk, why)
		}
	}
	if got := TrimWorkspaceCd("cd "+root+" && go test ./...", root); !strings.HasPrefix(got, "go test") {
		t.Errorf("TrimWorkspaceCd kept the cd: %q", got)
	}
}
