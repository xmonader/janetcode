package agent

import "testing"

// Running the package an edited file is in is running the edited code:
// `go run .` after editing main.go read "nothing was run against it".
func TestRanEditedSeesPackageRuns(t *testing.T) {
	cases := []struct {
		cmd, path string
		want      bool
	}{
		{"go run .", "main.go", true},
		{"go run .", "/w/main.go", true},
		{"go run ./cmd/app", "cmd/app/main.go", true},
		{"go run -race ./cmd/app arg", "cmd/app/main.go", true},
		{"go run ./...", "internal/x/y.go", true},
		{"cd /w && go run .", "main.go", true},
		{"go run ./cmd/app", "internal/x/y.go", false},
		{"go run .", "cmd/app/main.go", false},
		{"cargo run --release", "src/lib.rs", true},
		{"go build ./...", "main.go", false},
		{"go run .", "README.md", false},
	}
	for _, c := range cases {
		if got := ranEdited(c.cmd, map[string]bool{c.path: true}, "/w"); got != c.want {
			t.Errorf("ranEdited(%q, %q) = %v, want %v", c.cmd, c.path, got, c.want)
		}
	}
}
