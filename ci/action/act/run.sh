#!/usr/bin/env bash
# Runs the repository's GitHub Action end to end in a runner container
# with nektos/act: the example review workflow (review.yml here) on a
# local pull_request event, a stub gh, and a real model. `make
# action-test` calls it.
#
# Needs act and docker. The key comes from the environment:
#   OPENAI_API_KEY, OPENAI_BASE_URL (an OpenAI-compatible endpoint) and
#   JANETCODE_MODEL (default deepseek/deepseek-v4.1-flash).
# ACT_IMAGE overrides the runner image (catthehacker/ubuntu:act-latest,
# which has jq; gh is stubbed and Go is set up by the action itself).
set -euo pipefail
root=$(cd "$(dirname "$0")/../../.." && pwd)
act=${ACT:-act}
command -v "$act" >/dev/null || { echo "act not found: install it from https://github.com/nektos/act (or set ACT)" >&2; exit 2; }
: "${OPENAI_API_KEY:?set OPENAI_API_KEY (and OPENAI_BASE_URL) for the model the review uses}"
secrets=$(mktemp)
trap 'rm -f "$secrets"' EXIT
chmod 600 "$secrets"
{
  echo "OPENAI_API_KEY=$OPENAI_API_KEY"
  echo "OPENAI_BASE_URL=${OPENAI_BASE_URL:-}"
  echo "JANETCODE_MODEL=${JANETCODE_MODEL:-deepseek/deepseek-v4.1-flash}"
} >"$secrets"
cd "$root"
log=$(mktemp)
set +e
"$act" pull_request \
  -W ci/action/act/review.yml \
  -e ci/action/act/pull_request.json \
  --secret-file "$secrets" \
  -P "ubuntu-latest=${ACT_IMAGE:-catthehacker/ubuntu:act-latest}" \
  --pull=false \
  2>&1 | tee "$log"
rc=${PIPESTATUS[0]}
set -e
if [ "$rc" -ne 0 ] || ! grep -q "ACTION-TEST-OK" "$log"; then
  echo "action-test: FAILED (act exit $rc)" >&2
  exit 1
fi
echo "action-test: ok"
