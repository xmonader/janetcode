#!/usr/bin/env bash
# The janetcode GitHub Action's one step that does the work (action.yml at
# the repository root calls it). It reads the event GitHub hands the run,
# builds a prompt for the mode, runs janetcode headless with a JSON result
# and a spending ceiling, and posts the answer back as a comment.
#
# Inputs arrive as INPUT_* variables set by action.yml. Everything it
# needs from GitHub is in GITHUB_EVENT_PATH, GITHUB_EVENT_NAME,
# GITHUB_REPOSITORY and GITHUB_OUTPUT, plus gh (logged in through
# GH_TOKEN) and jq, both preinstalled on GitHub's runners.
set -euo pipefail

jc="${JANETCODE_BIN:-janetcode}"
event="${GITHUB_EVENT_PATH:-/dev/null}"
name="${GITHUB_EVENT_NAME:-workflow_dispatch}"
repo="${GITHUB_REPOSITORY:-}"
out="${GITHUB_OUTPUT:-/dev/null}"
work="${RUNNER_TEMP:-$(mktemp -d)}/janetcode-action"
mkdir -p "$work"

# Empty inputs arrive as empty variables; janetcode would read an empty
# key as a key.
for v in ANTHROPIC_API_KEY OPENAI_API_KEY OPENAI_BASE_URL; do
  [ -n "${!v:-}" ] || unset "$v"
done

output() { # name value — multi-line safe
  local delim="EOF_$RANDOM$RANDOM"
  { echo "$1<<$delim"; printf '%s\n' "$2"; echo "$delim"; } >>"$out"
}

skip() {
  echo "janetcode: $*"
  output skipped true
  exit 0
}

ev() { jq -r "$1 // empty" "$event" 2>/dev/null || true; }

mode="${INPUT_MODE:-auto}"
if [ "$mode" = auto ]; then
  case "$name" in
    pull_request | pull_request_target) mode=review ;;
    issue_comment | pull_request_review_comment | pull_request_review | issues) mode=mention ;;
    *) mode=prompt ;;
  esac
fi

number=""
extra="${INPUT_PROMPT:-}"
case "$mode" in
  review)
    number=$(ev '.pull_request.number // .issue.number')
    [ -n "$number" ] || { echo "janetcode: mode review needs a pull_request event" >&2; exit 1; }
    title=$(ev '.pull_request.title')
    body=$(ev '.pull_request.body')
    gh pr diff "$number" --repo "$repo" >"$work/pr.diff"
    diff=$(head -c 200000 "$work/pr.diff")
    prompt="Review pull request #$number ($title) as a careful senior reviewer. Report correctness bugs, security problems, missing tests and risky changes, each with file:line and a concrete fix, most important first. Do not praise or restate the change. If you find nothing worth changing, say so in one line. You may read the checked-out repository for context.

Pull request description:
$body

${extra:+Extra instructions: $extra

}Diff:
$diff"
    ;;
  mention)
    text=$(ev '.comment.body // .review.body // .issue.body')
    trigger="${INPUT_TRIGGER:-@janetcode}"
    case "$text" in
      *"$trigger"*) ;;
      *) skip "no $trigger in the comment" ;;
    esac
    assoc=$(ev '.comment.author_association // .review.author_association // .issue.author_association')
    allowed=",${INPUT_ALLOWED_ASSOCIATIONS:-OWNER,MEMBER,COLLABORATOR},"
    case "$allowed" in
      *",$assoc,"*) ;;
      *) skip "ignoring a mention by a $assoc (allowed: ${INPUT_ALLOWED_ASSOCIATIONS:-OWNER,MEMBER,COLLABORATOR})" ;;
    esac
    number=$(ev '.issue.number // .pull_request.number')
    what=issue
    if [ -n "$(ev '.issue.pull_request.url // .pull_request.number')" ]; then what="pull request"; fi
    title=$(ev '.issue.title // .pull_request.title')
    body=$(ev '.issue.body // .pull_request.body')
    prompt="You were mentioned on GitHub $what #$number ($title) in $repo. Answer the request in the comment below; the repository is checked out in the working directory. Reply in Markdown suited to a GitHub comment.

$what description:
$body

${extra:+Extra instructions: $extra

}The comment:
$text"
    ;;
  prompt)
    [ -n "$extra" ] || { echo "janetcode: mode prompt needs the prompt input" >&2; exit 1; }
    prompt="$extra"
    number=$(ev '.issue.number // .pull_request.number')
    ;;
  *)
    echo "janetcode: unknown mode $mode (review, mention, prompt or auto)" >&2
    exit 1
    ;;
esac

args=(-p "$prompt" --output-format json)
if [ -n "${INPUT_MAX_COST:-}" ] && [ "${INPUT_MAX_COST}" != 0 ]; then args+=(--max-cost "$INPUT_MAX_COST"); fi
if [ -n "${INPUT_MAX_TURNS:-}" ]; then args+=(--max-turns "$INPUT_MAX_TURNS"); fi
if [ -n "${INPUT_MODEL:-}" ]; then args+=(--model "$INPUT_MODEL"); fi
if [ -n "${INPUT_PROVIDER:-}" ]; then args+=(--provider "$INPUT_PROVIDER"); fi
edits=false
if [ "${INPUT_ALLOW_EDITS:-false}" = true ] && [ "$mode" != review ]; then
  edits=true
  args+=(--yolo)
else
  args+=(--readonly)
fi

set +e
"$jc" "${args[@]}" </dev/null >"$work/result.json"
code=$?
set -e

result=$(jq -r '.result // empty' "$work/result.json" 2>/dev/null || true)
cost=$(jq -r '.cost_usd // empty' "$work/result.json" 2>/dev/null || true)
session=$(jq -r '.session_id // empty' "$work/result.json" 2>/dev/null || true)
model=$(jq -r '.model // empty' "$work/result.json" 2>/dev/null || true)
error=$(jq -r '.error // empty' "$work/result.json" 2>/dev/null | head -c 1000 || true)
output result "$result"
output cost-usd "$cost"
output exit-code "$code"
output session-id "$session"
output skipped false

pr_url=""
if [ "$edits" = true ] && [ -n "$(git status --porcelain 2>/dev/null)" ]; then
  branch="janetcode/${number:-run}-${GITHUB_RUN_ID:-$$}"
  git config user.name >/dev/null || git config user.name "janetcode[bot]"
  git config user.email >/dev/null || git config user.email "janetcode[bot]@users.noreply.github.com"
  git checkout -q -b "$branch"
  git add -A
  git commit -q -m "janetcode: ${title:-changes}${number:+ (#$number)}" -m "Made by janetcode in GitHub Actions run ${GITHUB_RUN_ID:-}."
  git push -q -u origin "$branch"
  base=$(ev '.pull_request.head.ref')
  [ -n "$base" ] || base=$(gh repo view "$repo" --json defaultBranchRef -q .defaultBranchRef.name)
  printf '%s\n\n%s\n' "${number:+Follows #$number.}" "$result" >"$work/pr-body.md"
  pr_url=$(gh pr create --repo "$repo" --base "$base" --head "$branch" --title "janetcode: ${title:-changes}" --body-file "$work/pr-body.md")
  output pr-url "$pr_url"
fi

if [ "${INPUT_COMMENT:-true}" = true ] && [ -n "$number" ] && [ -n "$repo" ]; then
  {
    if [ -n "$result" ]; then printf '%s\n' "$result"; else
      printf 'janetcode ended without an answer (exit %s).\n' "$code"
      [ -z "$error" ] || printf '\n```\n%s\n```\n' "$error"
    fi
    [ -z "$pr_url" ] || printf '\nChanges: %s\n' "$pr_url"
    [ -z "$cost" ] || cost=$(printf '%.4f' "$cost")
    printf '\n<sub>janetcode%s%s · exit %s</sub>\n' "${model:+ · $model}" "${cost:+ · \$$cost}" "$code"
  } >"$work/comment.md"
  gh api --method POST "repos/$repo/issues/$number/comments" -F "body=@$work/comment.md" >/dev/null
fi

if [ "$code" != 0 ] && [ "${INPUT_FAIL_ON_ERROR:-true}" = true ]; then
  echo "janetcode exited $code" >&2
  exit "$code"
fi
