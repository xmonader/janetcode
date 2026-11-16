# GitHub Action, /pr and /pr-comments against github.com (2026-11-16)

Until now these were tested against a stub `gh` and nektos/act. This round
ran them against the real repository (xmonader/janetcode, private) with a
throwaway branch `test/live-pr-20261116` cut from origin/master. The branch,
the PR (#13) and every local copy are gone; master was not touched.

Setup: the TUI driven under a pty (pyte frames), scratch `XDG_CONFIG_HOME`
and `XDG_DATA_HOME`, `GH_TOKEN` from `gh auth token`, model
`deepseek/deepseek-v4-flash` on OpenRouter through `OPENAI_API_KEY` /
`OPENAI_BASE_URL`. No key was written anywhere.

## /pr

- First attempt: typing `/pr` and enter left `/pr-comments` in the prompt
  and ran nothing. The menu listed prefix matches in table order and
  `/pr-comments` precedes `/pr`; enter accepts the highlighted row. So `/pr`
  without an argument could not be run from the TUI. Fixed: a command typed
  in full leads the menu (`TestExactCommandNameLeadsTheMenu`, commit
  `fix(tui): a slash command typed in full leads the menu...`).
- After the fix, `/pr` pushed the branch and opened
  `https://github.com/xmonader/janetcode/pull/13` in about a minute. The
  status line read `opened test/live-pr-20261116 -> master`. `gh pr view 13`
  confirmed the drafted title `docs: add scratch file for live PR test` and
  body ("Created `docs/notes/live-pr-scratch.md` with a timestamped
  placeholder ..."), base master, head the test branch.
- A second `/pr` showed the existing PR instead of opening another: `a pull
  request for test/live-pr-20261116 is already open: .../pull/13`.

## /pr-comments

With a PR conversation comment and an inline review comment added through
`gh` (`gh pr comment`, `gh api .../pulls/13/comments`), `/pr-comments` showed
the PR header and description, the inline comment with `file:line` and
author, and the conversation comment, and the status line said `shared with
the model`.

## The Action

- `gh secret list` is empty: the repo has no `ANTHROPIC_API_KEY`, so the
  documented workflow could not have run with a credential. No secret or
  setting was created.
- A workflow using `uses: ./` (the review example with the local action) on
  the PR triggered on push and pull_request. GitHub did not start the job:
  the check run annotation says "The job was not started because recent
  account payments have failed or your spending limit needs to be
  increased." Runs 43881320747 (live test) and the repository's own `ci`
  runs all failed in 3-4 s with no steps. So the composite action itself
  (release install, source-build fallback, setup-go) was **not** exercised
  on a hosted runner. That needs the account's billing fixed, and a secret.
- Instead `ci/action/run.sh` (the action's one working step) was run on this
  machine against the live PR with the real `gh`, `GH_TOKEN`, and a real
  `pull_request` event for #13:
  - no credential (`env -i`): janetcode printed its `no API key` message,
    the step exited 41, and a comment was posted. The comment said only
    `janetcode ended without an answer (exit 41).` The reason was in the
    result's `error` field and the log only. Fixed: the comment now carries
    the error in a code block (`TestFailedRunCommentNamesTheError`; the test
    fails on the old script).
  - with the OpenRouter key as the provider, `--readonly`, `max-cost 0.20`:
    exit 0, cost `$0.00035631`, `gh pr diff` read, review posted as a PR
    comment ending `janetcode - deepseek/deepseek-v4-flash - $0.0004 - exit 0`.
    The review's content was weak (it invented problems with the workflow
    file); the plumbing is what was under test.
- Side observation: with the user's normal environment (other providers'
  keys exported) a run with no `OPENAI_API_KEY` still picked a provider
  from the environment (`zai-coding-plan`, `glm-5`). That is the documented
  env detection, but in a runner it means any key exported to the job is
  used, not only the action's inputs.

## Still open

- The Action on a hosted runner (billing, then a secret).
- The action's release-download path against a private repository's
  release (the repo is private, so the download cannot succeed; the
  source-build fallback is what a private repo would use).
