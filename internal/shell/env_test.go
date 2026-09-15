package shell

import (
	"strings"
	"testing"
)

func TestScrubEnvLocale(t *testing.T) {
	get := func(env []string, key string) (string, bool) {
		val, ok := "", false
		for _, kv := range env { // last wins, as exec does
			if n, v, _ := strings.Cut(kv, "="); n == key {
				val, ok = v, true
			}
		}
		return val, ok
	}
	got := ScrubEnv([]string{"LC_ALL=C", "LC_CTYPE=en_US.UTF-8", "PATH=/bin"})
	if _, ok := get(got, "LC_ALL"); ok {
		t.Error("LC_ALL=C should be dropped; it defeats LC_CTYPE")
	}
	if v, _ := get(got, "LC_CTYPE"); v != "C.UTF-8" {
		t.Errorf("LC_CTYPE = %q, want C.UTF-8", v)
	}
	if v, _ := get(got, "PATH"); v != "/bin" {
		t.Errorf("PATH was disturbed: %q", v)
	}
	// A real locale is left exactly as the user set it.
	got = ScrubEnv([]string{"LC_ALL=de_DE.UTF-8", "LC_CTYPE=de_DE.UTF-8"})
	if v, ok := get(got, "LC_ALL"); !ok || v != "de_DE.UTF-8" {
		t.Errorf("a real LC_ALL must survive, got %q %v", v, ok)
	}
}
