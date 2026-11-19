#!/usr/bin/env bash
set -euo pipefail

die() { printf '%s\n' "$*" >&2; exit 1; }

usage() {
  cat >&2 <<'EOF'
Usage:
  run.sh prepare BARE_REPO PR PATCH PROMPT [JANETCODE_FLAGS...]
  run.sh publish BARE_REPO PR PATCH REVIEWED_HEAD REVIEWED_SHA256

Use a local bare repository with refs/pull/PR/head set. Review PATCH
before passing its SHA-256 and source commit to publish.
EOF
  exit 2
}

[[ $# -ge 5 ]] || usage
mode=$1 repo=$2 pr=$3 patch=$4
shift 4
[[ $pr =~ ^[1-9][0-9]*$ ]] || die "PR must be a positive integer"
[[ $mode == prepare || $mode == publish ]] || usage
repo=$(cd -- "$repo" && pwd -P)
pr_ref="refs/pull/$pr/head"
patch_limit=$((10 * 1024 * 1024))
tmp=$(mktemp -d /tmp/janetcode-patch.XXXXXXXX)
trap 'rm -rf -- "$tmp"' EXIT

clean_git() {
  env -i PATH="$PATH" GIT_CONFIG_NOSYSTEM=1 GIT_CONFIG_GLOBAL=/dev/null \
    GIT_NO_REPLACE_OBJECTS=1 GIT_INDEX_FILE="$tmp/index" \
    git -c core.hooksPath=/dev/null -c maintenance.auto=false -c gc.auto=0 "$@"
}

repo_git() { clean_git --git-dir="$repo" "$@"; }
digest() { sha256sum <"$1" | cut -d ' ' -f1; }

[[ $(repo_git rev-parse --is-bare-repository) == true ]] || die "Use a bare repository"

if [[ $mode == prepare ]]; then
  prompt=$1
  shift
  head=$(repo_git rev-parse --verify "$pr_ref^{commit}")
  format=$(repo_git rev-parse --show-object-format)
  clean_git init -q --bare --object-format="$format" "$tmp/source"
  clean_git --git-dir="$tmp/source" fetch -q --no-tags --depth=1 -- "$repo" "$head"
  clean_git --git-dir="$tmp/source" update-ref refs/heads/snapshot "$head"
  clean_git --git-dir="$tmp/source" symbolic-ref HEAD refs/heads/snapshot

  script_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
  install -m 0755 -- "${JANETCODE_BIN:-$script_dir/../../bin/janetcode}" "$tmp/janetcode"
  network=${PATCH_PUBLISH_NETWORK:-bridge}
  [[ $network == none || $network == bridge ]] || die "Network must be none or bridge"
  provider=${PATCH_PUBLISH_PROVIDER:-openai}
  case $provider in
    openai) model_env=(--env OPENAI_API_KEY --env OPENAI_BASE_URL) ;;
    anthropic) model_env=(--env ANTHROPIC_API_KEY --env ANTHROPIC_BASE_URL) ;;
    *) die "Provider must be openai or anthropic" ;;
  esac
  # Pass model settings by name. The container gets no GitHub token, Docker
  # socket, caller home, or writable mount into the publisher's repository.
  docker run --rm --pull never --network "$network" --read-only \
    --cap-drop ALL --security-opt no-new-privileges --pids-limit 256 \
    --user "$(id -u):$(id -g)" --tmpfs /tmp:rw,nosuid,nodev,exec,mode=1777 \
    --env HOME=/tmp/home --env GIT_CONFIG_NOSYSTEM=1 \
    "${model_env[@]}" \
    --mount "type=bind,source=$tmp/source,target=/snapshot,readonly" \
    --mount "type=bind,source=$tmp/janetcode,target=/janetcode,readonly" \
    --entrypoint /bin/sh "${PATCH_PUBLISH_IMAGE:-golang:1.26.10}" -eu -c '
      head=$1
      shift
      mkdir -p "$HOME"
      git -c safe.directory=/snapshot clone -q --no-local /snapshot /tmp/work
      cd /tmp/work
      git remote remove origin
      git checkout -q --detach "$head"
      /janetcode "$@" --yolo --max-wall-time 10m >&2
      git -c core.hooksPath=/dev/null add -A
      git --no-pager diff --cached --binary --full-index --no-ext-diff --no-textconv "$head" --
    ' sh "$head" -p "$prompt" "$@" --provider "$provider" |
    head -c "$((patch_limit + 1))" >"$tmp/patch"
  [[ -s $tmp/patch ]] || die "The edit produced no patch"
  [[ $(wc -c <"$tmp/patch") -le $patch_limit ]] || die "Patch exceeds 10 MiB"
  (set -o noclobber; cat "$tmp/patch" >"$patch")
  printf 'PR %s\nHead %s\nSHA-256 %s\nPatch %s\n' "$pr" "$head" "$(digest "$tmp/patch")" "$patch"
  exit 0
fi

[[ $# == 2 ]] || usage
head=$1 reviewed_digest=$2
[[ $head =~ ^([0-9a-f]{40}|[0-9a-f]{64})$ && $reviewed_digest =~ ^[0-9a-f]{64}$ ]] ||
  die "Supply the full reviewed commit ID and SHA-256"
[[ -f $patch ]] || die "Patch must be a regular file"
head -c "$((patch_limit + 1))" <"$patch" >"$tmp/patch"
[[ -s $tmp/patch && $(wc -c <"$tmp/patch") -le $patch_limit ]] || die "Invalid patch size"
[[ $(digest "$tmp/patch") == "$reviewed_digest" ]] || die "Patch differs from the reviewed SHA-256"
[[ $(repo_git rev-parse --verify "$pr_ref^{commit}") == "$head" ]] || die "PR head changed. Review a fresh patch."

# Apply only to a private index. The publisher never checks out or runs
# the proposed files. Both the freshness check and branch creation lock
# their refs in the same transaction.
repo_git read-tree "$head"
repo_git apply --cached --binary --whitespace=nowarn -- "$tmp/patch"
tree=$(repo_git write-tree)
[[ $tree != "$(repo_git rev-parse "$head^{tree}")" ]] || die "Patch made no tree changes"
commit=$(repo_git -c user.name=JanetCode -c user.email=janetcode@example.invalid \
  commit-tree "$tree" -p "$head" -m "JanetCode proposal for PR #$pr")
branch="refs/heads/janetcode/pr-$pr/$head-$reviewed_digest"
printf 'start\nverify %s %s\ncreate %s %s\nprepare\ncommit\n' "$pr_ref" "$head" "$branch" "$commit" |
  repo_git update-ref --stdin >/dev/null
printf 'Published %s to %s\n' "$commit" "$branch"
