# Four harnesses on qwen3.7-plus

- Model: `qwen3.7-plus` at `https://coding-intl.dashscope.aliyuncs.com/v1`
- Tasks: `evals/` 01–20, one run each, same endpoint and same task
  directories for every harness
- Harnesses: janetcode, pi, qwencode, hermes-agent

## Totals

| harness | passed | fresh input | cache read | output | turns | wall |
|---|---:|---:|---:|---:|---:|---:|
| **janetcode** | **20/20** | **96,372** | **240,112** | 7,983 | 75 | 390s |
| pi | 20/20 | 97,732 | 1,261,824 | 7,559 | 75 | 378s |
| qwencode | 20/20 | 866,421 | 1,932,451 | 7,717 | 66 | 459s |
| hermes-agent | 19/20 | 153,833 | 250,496 | 12,027 | 93 | 550s |

Fresh input and cache read are separated deliberately: a cache read costs
roughly a tenth of a fresh input token, and mixing them lets a harness that
merely takes more turns look more expensive than one that genuinely resends
the conversation. On the column that costs money, janetcode and pi are
level and qwencode spends nine times as much.

On the column that does not, the spread is wider: pi reads 1.26M from cache
against janetcode's 240k for the same work and the same turn count, which
is a different prefix strategy rather than a different amount of work.

hermes-agent's single failure — `08-shell-script` — does not reproduce by
hand; run again on its own it writes the script and passes. One in twenty
is within this set's flake rate and is recorded as such rather than as a
finding about the harness.

## Two competitors were misconfigured, and that is worth more than the table

Before any of this was measurable:

- **hermes-agent scored 0/20.** Its terminal tool takes its working
  directory from `TERMINAL_CWD`, not from the process's own, so every task
  ran in the user's home directory. It did the work correctly and the check
  looked in the task directory and found nothing. Fixed, it passes.
- **openclaw was hard-coded to fail** in `runHarness` and never launched at
  all. Three separate obstacles were found: it needs Node ≥22.22.3 and a
  bundled v22.22.2 was shadowing the system's v24.20.0 on PATH; it refuses
  `--local` while a Gateway is running for the same state directory, which
  an isolated `--profile` avoids; and its model configuration is not a
  simple provider block, which is where this stopped. It is absent from the
  table rather than reported as a zero.

A benchmark that misconfigures a competitor produces a number that looks
like a finding. Both of these would have.

## Reproduce

```
./bin/harnessbench --evals evals --harnesses janetcode,pi,hermes-agent,qwencode \
  --model qwen3.7-plus --base-url https://coding-intl.dashscope.aliyuncs.com/v1 --out out
```
