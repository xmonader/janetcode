#!/usr/bin/env bash
# For every non-merge commit in RANGE that changes both Go code and Go tests:
# check out the code as it was before the commit, add only that commit's test
# files, and run the tests the commit added or changed. A fix is verified when
# those tests fail (or do not compile) on the code without it.
#   docs/benchmarks/tui-review/verify_fixes.sh [RANGE]   (default ebc33f62..master)
set -u
RANGE=${1:-ebc33f62..master}
REPO=$(git rev-parse --show-toplevel)
W=$(mktemp -d)/w
git -C "$REPO" worktree add -q --detach "$W" master
trap 'git -C "$REPO" worktree remove --force "$W"' EXIT
printf '%-9s %-14s %s\n' commit result subject
for c in $(git -C "$REPO" rev-list --no-merges --reverse "$RANGE"); do
  files=$(git -C "$REPO" diff-tree --no-commit-id --name-only -r "$c")
  tests=$(echo "$files" | grep '_test\.go$' || true)
  code=$(echo "$files" | grep '\.go$' | grep -v '_test\.go$' || true)
  subj=$(git -C "$REPO" log -1 --format=%s "$c")
  if [ -z "$code" ]; then printf '%-9s %-14s %s\n' "${c:0:8}" no-code "$subj"; continue; fi
  if [ -z "$tests" ]; then printf '%-9s %-14s %s\n' "${c:0:8}" no-test "$subj"; continue; fi
  names=$(git -C "$REPO" show "$c" -- $tests | sed -n 's/^+func \(Test[A-Za-z0-9_]*\)(.*/\1/p' | sort -u | paste -sd'|')
  if [ -z "$names" ]; then
    # Changed existing tests only: the test functions whose bodies the diff touched.
    names=$(git -C "$REPO" show -U0 "$c" -- $tests | sed -n 's/^@@.*func \(Test[A-Za-z0-9_]*\)(.*/\1/p' | sort -u | paste -sd'|')
  fi
  pkgs=$(echo "$tests" | xargs -n1 dirname | sort -u | sed 's#^#./#' | paste -sd' ')
  ( cd "$W" && git reset -q --hard && git clean -fdq && git checkout -q --detach "$c^" && git checkout -q "$c" -- $tests
    if [ -z "$names" ]; then r=no-test-names
    else out=$(go test $pkgs -run "^(${names})\$" -count=1 2>&1); rc=$?
      # By exit status: a test that crashes the binary (a nil dereference
      # on a goroutine) never prints "--- FAIL", but it failed.
      if [ $rc -eq 0 ]; then r=STILL-PASSES
      elif echo "$out" | grep -qE 'build failed|setup failed|undefined:'; then r=COMPILE-FAIL
      elif echo "$out" | grep -q '^panic:'; then r=FAILS-CRASH
      else r=FAILS; fi
    fi
    printf '%-9s %-14s %s\n' "${c:0:8}" "$r" "$subj" )
done
