package xdg

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestDir(t *testing.T) {
	cfg, data := "XDG_CONFIG_HOME", "XDG_DATA_HOME"
	dotConfig, dotShare := ".config", filepath.Join(".local", "share")
	cases := []struct {
		name     string
		goos     string
		env      map[string]string
		xdg      string
		fallback string
		want     string
		wantErr  string
	}{
		{"config from XDG", "linux", map[string]string{cfg: "/x/cfg", "HOME": "/home/u"}, cfg, dotConfig, filepath.Join("/x/cfg", "janetcode"), ""},
		{"config from HOME", "linux", map[string]string{"HOME": "/home/u"}, cfg, dotConfig, filepath.Join("/home/u", ".config", "janetcode"), ""},
		{"relative XDG is ignored", "linux", map[string]string{data: "share", "HOME": "/home/u"}, data, dotShare, filepath.Join("/home/u", ".local", "share", "janetcode"), ""},
		{"dot-relative XDG is ignored", "linux", map[string]string{cfg: "./cfg", "HOME": "/home/u"}, cfg, dotConfig, filepath.Join("/home/u", ".config", "janetcode"), ""},
		{"empty XDG falls back", "darwin", map[string]string{cfg: "", "HOME": "/Users/u"}, cfg, dotConfig, filepath.Join("/Users/u", ".config", "janetcode"), ""},
		{"data from XDG", "linux", map[string]string{data: "/x/data"}, data, dotShare, filepath.Join("/x/data", "janetcode"), ""},
		{"data from HOME", "linux", map[string]string{"HOME": "/home/u"}, data, dotShare, filepath.Join("/home/u", ".local", "share", "janetcode"), ""},
		{"XDG wins without a home", "linux", map[string]string{data: "/x/data"}, data, dotShare, filepath.Join("/x/data", "janetcode"), ""},
		{"windows uses USERPROFILE", "windows", map[string]string{"USERPROFILE": `C:\Users\u`, "HOME": "/ignored"}, cfg, dotConfig, filepath.Join(`C:\Users\u`, ".config", "janetcode"), ""},
		{"windows ignores HOME", "windows", map[string]string{"HOME": "/home/u"}, cfg, dotConfig, "", "%userprofile% is not defined"},
		{"plan9 uses home", "plan9", map[string]string{"home": "/usr/u"}, data, dotShare, filepath.Join("/usr/u", ".local", "share", "janetcode"), ""},
		{"no HOME is an error", "linux", map[string]string{}, cfg, dotConfig, "", "$HOME is not defined"},
		{"android defaults", "android", map[string]string{}, cfg, dotConfig, filepath.Join("/sdcard", ".config", "janetcode"), ""},
		{"ios defaults", "ios", map[string]string{}, data, dotShare, filepath.Join("/", ".local", "share", "janetcode"), ""},
	}
	for _, c := range cases {
		got, err := dir(c.goos, env(c.env), c.xdg, c.fallback)
		if c.wantErr != "" {
			if err == nil || err.Error() != c.wantErr {
				t.Errorf("%s: err = %v, want %q", c.name, err, c.wantErr)
			}
			continue
		}
		if err != nil || got != c.want {
			t.Errorf("%s: got %q, %v; want %q", c.name, got, err, c.want)
		}
	}
}

// TestHomeDirMatchesOS pins homeDir to the standard library on this
// platform, so the copy cannot drift from os.UserHomeDir.
func TestHomeDirMatchesOS(t *testing.T) {
	want, wantErr := os.UserHomeDir()
	got, err := homeDir(runtime.GOOS, os.Getenv)
	if got != want || (err == nil) != (wantErr == nil) {
		t.Fatalf("homeDir = %q, %v; os.UserHomeDir = %q, %v", got, err, want, wantErr)
	}
}

func TestExported(t *testing.T) {
	e := env(map[string]string{"XDG_CONFIG_HOME": "/c", "XDG_DATA_HOME": "/d"})
	if got, err := ConfigDir(e); err != nil || got != filepath.Join("/c", "janetcode") {
		t.Errorf("ConfigDir = %q, %v", got, err)
	}
	if got, err := DataDir(e); err != nil || got != filepath.Join("/d", "janetcode") {
		t.Errorf("DataDir = %q, %v", got, err)
	}
}
