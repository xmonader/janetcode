package config

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/opref"
	"github.com/xmonader/janetcode/internal/secret"
)

func fakeOpOnPath(t *testing.T, script string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell script stand-in for op")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "op"), []byte("#!/bin/sh\n"+script+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	opref.Forget()
	t.Cleanup(func() { opref.Forget(); secret.Forget() })
}

// A reference in api_key, api_keys or the variable key_env names is read
// when the key is used; the config on disk keeps the reference and never
// gains the value.
func TestOpReferenceKeysResolveAtUseAndNeverReachTheFile(t *testing.T) {
	fakeOpOnPath(t, `case "$2" in op://vault/item/a) printf 'resolved-A\n';; op://vault/item/b) printf 'resolved-B\n';; *) echo "no such item" >&2; exit 1;; esac`)
	p := filepath.Join(t.TempDir(), "config.json")
	os.WriteFile(p, []byte(`{"providers":{"gw":{"type":"openai","base_url":"https://gw.example/v1","api_key":"op://vault/item/a","api_keys":["literal-k","op://vault/item/b"],"models":["m"]}}}`), 0o600)
	cfg, err := load(p, env(nil), Overrides{Provider: "gw", Model: "m"})
	if err != nil {
		t.Fatal(err)
	}
	pr := cfg.Active()
	if got := strings.Join(pr.Keys(), ","); got != "op://vault/item/a,literal-k,op://vault/item/b" {
		t.Fatalf("Keys() must stay references: %s", got)
	}
	got, err := pr.ResolvedKeys()
	if err != nil || strings.Join(got, ",") != "resolved-A,literal-k,resolved-B" {
		t.Fatalf("ResolvedKeys = %v, %v", got, err)
	}
	if err := Update(p, "gw", pr, "m"); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(p)
	if strings.Contains(string(raw), "resolved-") || !strings.Contains(string(raw), "op://vault/item/a") {
		t.Fatalf("config after a save: %s", raw)
	}
}

func TestOpReferenceFromKeyEnvResolves(t *testing.T) {
	fakeOpOnPath(t, `printf 'from-env-ref\n'`)
	p := filepath.Join(t.TempDir(), "config.json")
	os.WriteFile(p, []byte(`{"providers":{"gw":{"type":"openai","base_url":"https://gw.example/v1","key_env":"GW_KEY","models":["m"]}}}`), 0o600)
	cfg, err := load(p, env(map[string]string{"GW_KEY": "op://vault/item/f"}), Overrides{Provider: "gw", Model: "m"})
	if err != nil {
		t.Fatal(err)
	}
	got, err := cfg.Active().ResolvedKeys()
	if err != nil || len(got) != 1 || got[0] != "from-env-ref" {
		t.Fatalf("got %v, %v", got, err)
	}
}

func TestOpReferenceFailureNamesIt(t *testing.T) {
	fakeOpOnPath(t, `echo "not signed in" >&2; exit 1`)
	pr := Provider{Type: "openai"}
	pr.SetKeys([]string{"op://vault/item/zzz"})
	_, err := pr.ResolvedKeys()
	if err == nil || !strings.Contains(err.Error(), "op://vault/item/zzz") || !strings.Contains(err.Error(), "not signed in") {
		t.Fatalf("err = %v", err)
	}
}
