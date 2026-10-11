package shell

import (
	"reflect"
	"testing"
)

func TestPlantedHoneytokens(t *testing.T) {
	t.Cleanup(func() { Plant(nil) })
	Plant([]string{"DEPLOY_TOKEN_BACKUP=dtk_1", "PROD_DB_PASSWORD_RO=pg_2"})
	got := ScrubEnv([]string{"PATH=/bin", "PROD_DB_PASSWORD_RO=real"})
	has := map[string]bool{}
	for _, kv := range got {
		has[kv] = true
	}
	if !has["DEPLOY_TOKEN_BACKUP=dtk_1"] {
		t.Errorf("the honeytoken is not planted: %v", got)
	}
	// A variable the user really has is never shadowed by a planted one.
	if has["PROD_DB_PASSWORD_RO=pg_2"] || !has["PROD_DB_PASSWORD_RO=real"] {
		t.Errorf("a real variable was shadowed: %v", got)
	}
	Plant(nil)
	for _, kv := range ScrubEnv([]string{"PATH=/bin"}) {
		if kv == "DEPLOY_TOKEN_BACKUP=dtk_1" {
			t.Fatal("Plant(nil) left it planted")
		}
	}
}

func TestExposedSecrets(t *testing.T) {
	t.Cleanup(func() { Plant(nil) })
	Plant([]string{"DEPLOY_TOKEN_BACKUP=dtk_1"})
	got := ExposedSecrets([]string{
		"GITHUB_TOKEN=ghp_x",       // kept for gh: exposed
		"OPENAI_API_KEY=sk-x",      // scrubbed: not exposed
		"DB_PASSWORD=hunter2",      // exposed
		"EMPTY_SECRET=",            // empty: nothing to read
		"PATH=/bin", "KEYBOARD=us", // not credentials
	})
	if !reflect.DeepEqual(got, []string{"GITHUB_TOKEN", "DB_PASSWORD"}) {
		t.Fatalf("ExposedSecrets = %v", got)
	}
}
