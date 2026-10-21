package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The orient consult is shown the start of each likely file, cut to a
// byte budget and then redacted. A key the cut went through was no longer
// the shape the redactor knows, so its first part went to the consult
// model in the clear.
func TestOrientQuestionRedactsBeforeCutting(t *testing.T) {
	root := t.TempDir()
	key := "sk-ant-api03-" + strings.Repeat("A1b2", 8)
	pad := strings.Repeat("x", orientFileBytes-len("the key is ")-20) + "\n"
	if err := os.WriteFile(filepath.Join(root, "conf.txt"), []byte(pad+"the key is "+key+"\nmore\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	a := &Agent{LikelyFiles: func(context.Context, string) (string, []string) { return root, []string{"conf.txt"} }}
	q := a.orientQuestion(context.Background(), "fix the config")
	if strings.Contains(q, "sk-ant-api03-A1b2") {
		t.Fatalf("part of a key reached the consult:\n%s", q[len(q)-300:])
	}
}
