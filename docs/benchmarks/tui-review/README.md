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
