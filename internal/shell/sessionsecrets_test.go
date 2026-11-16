package shell

import (
	"strings"
	"testing"
)

func TestSessionSecretsReachScrubbedEnvEvenWhenNamedLikeACredential(t *testing.T) {
	t.Cleanup(ClearSessionSecrets)
	SetSessionSecret("JANETCODE_SECRET_API_KEY", "v4lue-xyz")
	env := ScrubEnv([]string{"PATH=/bin"})
	found := false
	for _, kv := range env {
		if kv == "JANETCODE_SECRET_API_KEY=v4lue-xyz" {
			found = true
		}
	}
	if !found {
		t.Fatalf("session secret missing from %v", env)
	}
	if got := SessionSecretNames(); len(got) != 1 || strings.Contains(got[0], "v4lue") {
		t.Fatalf("names = %v", got)
	}
	ClearSessionSecrets()
	for _, kv := range ScrubEnv([]string{"PATH=/bin"}) {
		if strings.HasPrefix(kv, "JANETCODE_SECRET_") {
			t.Fatalf("cleared secret still exported: %s", kv)
		}
	}
}
