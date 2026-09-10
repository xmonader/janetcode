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

## Three identical runs: how reproducible is each harness?

The same 20 tasks, three times, same model and endpoint. Fresh input
tokens per run, and the spread between the largest and smallest:

| harness | passed | fresh input, three runs | spread |
|---|---:|---|---:|
| qwencode | 60/60 | 866,853 · 865,991 · 867,064 | **0%** |
| **janetcode** | 58/60 | 95,849 · 95,447 · 96,835 | **1%** |
| hermes-agent | 60/60 | 138,794 · 141,357 · 146,492 | 6% |
| pi | 60/60 | 102,490 · 100,474 · 82,492 | **24%** |

Two things follow. janetcode is the most reproducible of the four that
vary at all, which matters for anyone using this suite to measure a change:
a 1% run-to-run spread means a 10% difference is visible, while pi's 24%
would swallow it. And the "21% noise floor" quoted elsewhere in these
documents is not a property of the benchmark — it was measured on the
multi-prompt session tasks, which are far noisier than these.

janetcode's two failures were both `17-csv-sort`, and both have the same
cause: asked to sort a CSV, the model sometimes reaches for `execute_code`
and writes the file with Python's `csv` module, which uses CRLF line
endings by default, against a check that compares to LF. Run through the
eval runner directly it passes 3/3 using the shell instead. It is a
stochastic choice rather than a defect, and the harnesses that lack a
Python sandbox cannot make it — an honest cost of having one.

No prompt text was added to steer it. Instruction text rides on every
request forever, and two fields measured at 21% of context earlier the same
day; spending that to influence one task in twenty is not a trade worth
making without evidence it works.
