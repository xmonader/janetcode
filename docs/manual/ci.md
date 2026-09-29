# Continuous integration

Part of the [janetcode manual](README.md).

janetcode runs anywhere a shell does, so any CI system can call
`janetcode -p "<prompt>" --output-format json --max-cost 1` and read the
[result object](cli.md#output-formats). For GitHub there is a ready-made
action in this repository, `action.yml` at its root, that does the
plumbing: it installs a checked release, builds a prompt from the event
that started the run, runs janetcode headless with a spending ceiling,
and posts the answer back as a comment.

## The GitHub Action

```yaml
- uses: actions/checkout@v4
- uses: xmonader/janetcode@master
  with:
    anthropic-api-key: ${{ secrets.ANTHROPIC_API_KEY }}
    max-cost: "1.00"
```

It has three modes, picked from the event unless `mode` names one:

- **review** (on `pull_request`): fetches the pull request's diff with
  `gh pr diff`, asks for bugs, security problems, missing tests and risky
  changes with `file:line` and a fix, and comments the answer on the pull
  request. It always runs `--readonly`: a review cannot change the
  repository, whatever the diff says.
- **mention** (on `issue_comment`, `pull_request_review_comment`,
  `pull_request_review`, `issues`): acts only when the comment contains
  the trigger (`@janetcode` by default) and its author's association is in
  `allowed-associations` (owners, members and collaborators by default),
  so a stranger's comment cannot spend your key. The issue or pull
  request's title and description and the comment go into the prompt,
  and the answer is posted as a reply.
- **prompt** (anything else, e.g. `workflow_dispatch` or a schedule): runs
  the `prompt` input as it is.

In review and mention mode `prompt` adds instructions to the built-in
brief ("focus on the SQL", "answer in French").

By default nothing is changed: the agent reads the checkout and answers.
`allow-edits: "true"` (mention and prompt modes) runs it with `--yolo` in
the runner instead — the runner is a throwaway machine — and, if it
changed anything, commits the changes to a new `janetcode/<n>-<run>`
branch, pushes it and opens a pull request against the pull request's
branch or the default branch, linked from the reply. That needs
`contents: write` and `pull-requests: write` in the workflow's
`permissions`.

| input | default | what it does |
|---|---|---|
| `mode` | `auto` | `review`, `mention`, `prompt`, or `auto` (from the event) |
| `prompt` | | the prompt for `prompt` mode; extra instructions otherwise |
| `trigger` | `@janetcode` | the word a comment must contain in `mention` mode |
| `allowed-associations` | `OWNER,MEMBER,COLLABORATOR` | whose mentions are answered |
| `allow-edits` | `false` | let the agent edit and open a pull request with its changes |
| `comment` | `true` | post the answer as a comment |
| `max-cost` | `1.00` | `--max-cost` in US dollars; `0` for none |
| `max-turns` | | `--max-turns` |
| `model`, `provider` | | `--model`, `--provider` |
| `anthropic-api-key` | | Anthropic key |
| `openai-api-key`, `openai-base-url` | | an OpenAI-compatible endpoint (OpenRouter, a broker, vLLM) |
| `version` | latest | the release tag to install |
| `install-from` | `release` | `release` (building from the action's own source if the download fails) or `source` |
| `github-token` | `github.token` | what `gh` uses to read the pull request and comment |
| `fail-on-error` | `true` | fail the step when janetcode exits non-zero |

Outputs: `result` (the answer), `cost-usd`, `exit-code` (see the
[exit codes](cli.md#exit-codes): 55 means `max-cost` was reached),
`session-id`, `skipped` (a mention without the trigger or from an
author not allowed) and `pr-url` (when `allow-edits` opened one). The
comment ends with the model, the cost and the exit code.

Two complete workflows to copy into `.github/workflows/`:
[review every pull request](../examples/github/janetcode-review.yml) and
[answer @janetcode mentions](../examples/github/janetcode-mention.yml).

### Keys and forks

Keep the API key in a repository secret. GitHub does not give secrets to
`pull_request` runs from forks, so a fork's pull request is not reviewed
with that trigger; `pull_request_target` would review it, but it runs
with the base repository's secrets and a write token next to code a
stranger wrote. If you use it, keep `allow-edits` off (review mode is
read-only regardless) and check out the base branch, not the pull
request's head. janetcode strips credential variables from the
environment of the commands it runs, but the model still reads the
diff, and a diff can carry instructions.

### Without the action

The action's work is in `ci/action/run.sh`, a plain bash script; another
CI system can run the same command it builds:

```sh
janetcode -p "Review this diff: $(git diff origin/main...HEAD)" \
  --readonly --output-format json --max-cost 1 </dev/null >result.json
jq -r .result result.json
```
