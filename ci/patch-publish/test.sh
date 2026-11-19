#!/usr/bin/env bash
set -euo pipefail

runner=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)/run.sh
tmp=$(mktemp -d)
trap 'rm -rf -- "$tmp"' EXIT
repo="$tmp/publisher.git"
pr_ref=refs/pull/42/head
git init -q "$tmp/work"
git init -q --bare "$repo"

git -C "$tmp/work" config user.name Test
git -C "$tmp/work" config user.email test@example.invalid
printf 'before\n' >"$tmp/work/example.txt"
git -C "$tmp/work" add example.txt
git -C "$tmp/work" commit -qm base
head=$(git -C "$tmp/work" rev-parse HEAD)
git -C "$tmp/work" push -q "$repo" "HEAD:$pr_ref"
printf 'new PR head\n' >"$tmp/work/example.txt"
git -C "$tmp/work" commit -qam advance
git -C "$tmp/work" push -q "$repo" HEAD:refs/heads/advanced
advanced=$(git -C "$tmp/work" rev-parse HEAD)

cat >"$tmp/editor" <<'EOF'
#!/bin/sh
set -eu
test ! -e "$2"
test ! -e /var/run/docker.sock
test -z "${GH_TOKEN:-}${GITHUB_TOKEN:-}${SSH_AUTH_SOCK:-}"
test -z "$(git remote)"
if git push /snapshot HEAD:refs/heads/unauthorized >/tmp/push.log 2>&1; then
  echo "Editor could write to the snapshot" >&2
  exit 1
fi
grep -Eq 'Read-only file system|unable to create temporary object directory|insufficient permission' /tmp/push.log
printf 'after\n' >example.txt
printf '\000\377\001' >binary.dat
test "$(cat example.txt)" = after
echo "Editor changed and tested files inside the container" >&2
EOF
chmod +x "$tmp/editor"
JANETCODE_BIN="$tmp/editor" PATCH_PUBLISH_NETWORK=none GH_TOKEN=fixture GITHUB_TOKEN=fixture \
  "$runner" prepare "$repo" 42 "$tmp/review.patch" "$repo" | tee "$tmp/receipt"
digest=$(sha256sum <"$tmp/review.patch" | cut -d ' ' -f1)
grep -Fx "Head $head" "$tmp/receipt"
grep -Fx "SHA-256 $digest" "$tmp/receipt"
branch="refs/heads/janetcode/pr-42/$head-$digest"

cp "$tmp/review.patch" "$tmp/changed.patch"
printf '\nchanged after review\n' >>"$tmp/changed.patch"
if "$runner" publish "$repo" 42 "$tmp/changed.patch" "$head" "$digest" >"$tmp/error" 2>&1; then
  echo "Publisher accepted changed patch bytes" >&2
  exit 1
fi
grep -F 'differs from the reviewed SHA-256' "$tmp/error"

git --git-dir="$repo" update-ref "$pr_ref" "$advanced"
if "$runner" publish "$repo" 42 "$tmp/review.patch" "$head" "$digest" >"$tmp/error" 2>&1; then
  echo "Publisher accepted a stale PR head" >&2
  exit 1
fi
grep -F 'PR head changed' "$tmp/error"
git --git-dir="$repo" update-ref "$pr_ref" "$head"

# Move the PR after the publisher's initial check, just before it asks
# Git to lock the refs. This is the race the transaction must close.
mkdir "$tmp/shim"
ln -s "$(command -v git)" "$tmp/shim/real-git"
cat >"$tmp/shim/git" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
git="${BASH_SOURCE[0]%/*}/real-git"
if [[ ${*: -2} == 'update-ref --stdin' ]]; then
  for arg in "$@"; do
    if [[ $arg == --git-dir=* ]]; then
      "$git" "$arg" update-ref refs/pull/42/head refs/heads/advanced
      break
    fi
  done
fi
exec "$git" "$@"
EOF
chmod +x "$tmp/shim/git"
if PATH="$tmp/shim:$PATH" "$runner" publish "$repo" 42 "$tmp/review.patch" "$head" "$digest" >"$tmp/error" 2>&1; then
  echo "Publisher raced past a moving PR head" >&2
  exit 1
fi
grep -F "cannot lock ref '$pr_ref'" "$tmp/error"
test -z "$(git --git-dir="$repo" for-each-ref refs/heads/janetcode)"
git --git-dir="$repo" update-ref "$pr_ref" "$head"

# Two publishers racing with the same approval may create only one branch.
"$runner" publish "$repo" 42 "$tmp/review.patch" "$head" "$digest" >"$tmp/first" 2>&1 &
first=$!
"$runner" publish "$repo" 42 "$tmp/review.patch" "$head" "$digest" >"$tmp/second" 2>&1 &
second=$!
successes=0
if wait "$first"; then successes=$((successes + 1)); fi
if wait "$second"; then successes=$((successes + 1)); fi
cat "$tmp/first" "$tmp/second"
test "$successes" -eq 1
test "$(git --git-dir="$repo" rev-parse "$branch^")" = "$head"
test "$(git --git-dir="$repo" rev-parse "$pr_ref")" = "$head"
test "$(git --git-dir="$repo" show "$branch:example.txt")" = after
printf '\000\377\001' >"$tmp/expected.dat"
git --git-dir="$repo" show "$branch:binary.dat" | cmp "$tmp/expected.dat" -
git --git-dir="$repo" diff --binary --full-index "$head" "$branch" | cmp "$tmp/review.patch" -
echo 'Patch publishing experiment passed'
