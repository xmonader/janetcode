package textfmt

import "testing"

func TestNearestNameIgnoresCaseAndSeparators(t *testing.T) {
	names := []string{"bash", "read_file", "write_file", "grep", "list_dir"}
	for typed, want := range map[string]string{
		"Bash": "bash", "readfile": "read_file", "read-file": "read_file",
		"ReadFile": "read_file", "writefil": "write_file", "listdir": "list_dir",
		"frobnicate": "",
	} {
		if got := NearestName(typed, names); got != want {
			t.Errorf("NearestName(%q) = %q, want %q", typed, got, want)
		}
	}
}
