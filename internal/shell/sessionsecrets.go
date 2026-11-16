package shell

import (
	"sort"
	"sync"
)

// sessionSecrets are the NAME=value pairs the user supplied through the
// request_secret prompt: process-wide and in memory only, for the same
// reason as the honeytokens (every backend builds its environment in
// ScrubEnv).
var (
	sessionMu      sync.Mutex
	sessionSecrets = map[string]string{}
)

// SetSessionSecret exports name=value to every model-run command from now
// on, replacing an earlier value of the same name.
func SetSessionSecret(name, value string) {
	sessionMu.Lock()
	sessionSecrets[name] = value
	sessionMu.Unlock()
}

// ClearSessionSecrets forgets every session secret.
func ClearSessionSecrets() {
	sessionMu.Lock()
	sessionSecrets = map[string]string{}
	sessionMu.Unlock()
}

func sessionSecretNames() []string {
	names := make([]string, 0, len(sessionSecrets))
	for n := range sessionSecrets {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// SessionSecretNames lists the variable names, sorted, without values.
func SessionSecretNames() []string {
	sessionMu.Lock()
	defer sessionMu.Unlock()
	return sessionSecretNames()
}

// SessionSecrets is NAME=value for each, sorted by name.
func SessionSecrets() []string {
	sessionMu.Lock()
	defer sessionMu.Unlock()
	names := sessionSecretNames()
	out := make([]string, len(names))
	for i, n := range names {
		out[i] = n + "=" + sessionSecrets[n]
	}
	return out
}
