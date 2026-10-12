# TUI review: fix verification

`verify_fixes.sh [RANGE]` re-checks every fix of the 2026-10-11/12 TUI review
(default range `ebc33f62..master`). For each non-merge commit that changes Go
code and Go tests it checks out the code before the commit, adds only that
commit's test files, and runs the tests the commit added or changed. A fix is
verified when they fail there (FAILS, FAILS-CRASH, or COMPILE-FAIL when they
use what the fix added). `verify_fixes.out` is the run recorded at 6c0b7726:
59 of 59 code fixes verified, none still passing.

The four `no-test` rows are:
- `32754d49`, `67f26386`: dead code removed, no behaviour to test.
- `c5094e23`, `6d3e6855`: tested by the later `395c1919` and `6c0b7726`, each
  confirmed to fail on the code before its fix.

The seven `no-code` rows are documentation or test-only commits.

## Limits of the check, and the independent audit

The script is a screen, not a proof:
- A COMPILE-FAIL row shows only that the tests need something the fix added,
  not that the old behaviour was wrong, and any compile error counts.
- Any non-zero exit counts as a failure, so a timeout or a flaky test would too.
- Test names come from added `func Test…` lines, or failing that from the hunk
  header, which can name the function above the edit. A missed name shows as
  STILL-PASSES or no-test-names, never as a false pass.
- One failing test is enough for a commit with several.

An independent audit on 2026-10-12, by a separate agent, re-ran the script and
matched this record line for line. It checked these rows by hand on `c^` and at
`c`, with each test failing before its fix and passing at it:
- COMPILE-FAIL: d59d7117, a159de06, 5034c1f5, b9c9a24e. Each compile error
  names only what the fix introduced, and 5034c1f5's TUI test also fails on
  behaviour.
- FAILS: 69104e91, 4ebea3df, 67bdb78c, 7361ac2e.
- FAILS-CRASH: ba124cff.

The audit also confirmed the no-test rows against their diffs, and found that
`go test ./...` and `go test -race ./internal/tui/...` pass on master.
