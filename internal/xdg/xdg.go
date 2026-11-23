// Package xdg resolves janetcode's per-user directories: configuration
// under XDG_CONFIG_HOME (default ~/.config) and data — sessions, memory,
// archives — under XDG_DATA_HOME (default ~/.local/share), and run state under
// XDG_STATE_HOME (default ~/.local/state), each in a
// janetcode subdirectory. The same rule applies on every platform; only
// where the home directory comes from differs, exactly as os.UserHomeDir
// decides it. It imports only the standard library.
package xdg

import (
	"errors"
	"path/filepath"
	"runtime"
)

// App is the subdirectory janetcode owns under each base directory.
const App = "janetcode"

// ConfigDir is $XDG_CONFIG_HOME/janetcode, or ~/.config/janetcode when the
// variable is unset or empty. getenv is normally os.Getenv.
func ConfigDir(getenv func(string) string) (string, error) {
	return dir(runtime.GOOS, getenv, "XDG_CONFIG_HOME", ".config")
}

// DataDir is $XDG_DATA_HOME/janetcode, or ~/.local/share/janetcode when
// the variable is unset or empty. getenv is normally os.Getenv.
func DataDir(getenv func(string) string) (string, error) {
	return dir(runtime.GOOS, getenv, "XDG_DATA_HOME", filepath.Join(".local", "share"))
}

// StateDir is $XDG_STATE_HOME/janetcode, or ~/.local/state/janetcode when
// the variable is unset or empty: what a process records about itself
// across runs (the spend ledger) as opposed to what the user keeps.
func StateDir(getenv func(string) string) (string, error) {
	return dir(runtime.GOOS, getenv, "XDG_STATE_HOME", filepath.Join(".local", "state"))
}

func dir(goos string, getenv func(string) string, env, fallback string) (string, error) {
	// The XDG spec says a relative value is invalid and to be ignored. It
	// matters here: taken as given, it resolved against whatever directory
	// janetcode was started in, so every project got its own sessions and
	// memory — conversation transcripts — written inside its working tree,
	// one careless commit away from being published. A leading slash or
	// backslash counts as rooted on every platform, so the rule reads the
	// same whichever OS evaluates it.
	if base := getenv(env); base != "" && (filepath.IsAbs(base) || base[0] == '/' || base[0] == '\\') {
		return filepath.Join(base, App), nil
	}
	home, err := homeDir(goos, getenv)
	if err != nil {
		return "", err
	}
	return filepath.Join(home, fallback, App), nil
}

// homeDir is os.UserHomeDir with the environment and the OS supplied, so
// the rule can be tested for every platform from any one of them.
func homeDir(goos string, getenv func(string) string) (string, error) {
	env, enverr := "HOME", "$HOME"
	switch goos {
	case "windows":
		env, enverr = "USERPROFILE", "%userprofile%"
	case "plan9":
		env, enverr = "home", "$home"
	}
	if v := getenv(env); v != "" {
		return v, nil
	}
	switch goos {
	case "android":
		return "/sdcard", nil
	case "ios":
		return "/", nil
	}
	return "", errors.New(enverr + " is not defined")
}
