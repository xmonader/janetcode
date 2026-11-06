package repomap

import (
	"context"
	"strings"
	"testing"
)

func TestCheckPlanFlagsUnknownNamesAndMissingFiles(t *testing.T) {
	c := &Cache{Root: fixture(t)}
	ctx := context.Background()
	// Everything named exists: defined symbols, a method by bare name, a
	// dotted name, a source path, a non-source path, and a path:line.
	ok := "I will change `NewHandler` in `api/handler.go:7` so `Serve` calls `Put` before `Get`; `store.Open` is used in `cmd/main.go`. See `README.md`."
	if note := c.CheckPlan(ctx, ok); note != "" {
		t.Fatalf("false positive: %s", note)
	}
	// A position with a column is still the file: only ":col" used to be
	// cut, and "api/handler.go:7" was reported missing.
	if note := c.CheckPlan(ctx, "The panic is at `api/handler.go:7:12`, and `cmd/main.go:3-5` calls it."); note != "" {
		t.Fatalf("path:line:col flagged: %s", note)
	}
	// A file named without its directory is the file that has that name.
	if note := c.CheckPlan(ctx, "The bug is in `handler.go`, and `main.go` wires it."); note != "" {
		t.Fatalf("directory-less names flagged: %s", note)
	}
	// Outside the repository: a scratch file, a URL path.
	if note := c.CheckPlan(ctx, "I will check it with `/tmp/check.go` against a request for `/foo%2f`."); note != "" {
		t.Fatalf("paths outside the repository flagged: %s", note)
	}
	bad := "First read `store/cache.go`, then have `Handler.Flush()` call `store.Evict` and the `RetryPolicy`.\nThe `helper` in `web/app.ts` stays. Plain words like `error` and `handler` are not names."
	note := c.CheckPlan(ctx, bad)
	for _, want := range []string{"`store/cache.go` does not exist", "`Handler.Flush` is neither defined nor used", "`store.Evict` is neither", "`RetryPolicy` is neither"} {
		if !strings.Contains(note, want) {
			t.Fatalf("missing %q in %q", want, note)
		}
	}
	if strings.Contains(note, "helper") || strings.Contains(note, "`error`") || strings.Contains(note, "app.ts") {
		t.Fatalf("false positive in %q", note)
	}
	// A sentence that introduces the name is allowed to.
	creating := "Add a `RetryPolicy` type in a new file `store/retry.go`.\nThen I will implement `store.Evict` and call it from `NewHandler`."
	if note := c.CheckPlan(ctx, creating); note != "" {
		t.Fatalf("creation sentences flagged: %s", note)
	}
	if note := c.CheckPlan(ctx, "no backticks here"); note != "" {
		t.Fatalf("plain text flagged: %s", note)
	}
	// Many misses are capped.
	many := "Look at `Alpha1`, `Beta2`, `Gamma3`, `Delta4`, `Epsilon5`, `Zeta6`, `Eta7`"
	if note := c.CheckPlan(ctx, many); !strings.Contains(note, "2 more") {
		t.Fatalf("cap: %s", note)
	}
}
