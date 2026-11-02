package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestDryRunPlansWithoutNetwork(t *testing.T) {
	t.Setenv("AIBROKER_AI02_API_KEY", "")
	var out bytes.Buffer
	err := run([]string{"run", "--model", "m", "--arms", "janetcode,aider", "--dry-run", "--only", "03-fix", "--evals", "../../evals", "--out", t.TempDir()}, &out)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "adapter janetcode") || !strings.Contains(out.String(), "plan ") && !strings.Contains(out.String(), "skip aider") {
		t.Fatalf("%s", out.String())
	}
}
