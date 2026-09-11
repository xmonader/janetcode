# Regression check after the 2026-09-11 polish rounds

- Model: `qwen/qwen3.7-flash` on OpenRouter, the same endpoint as the
  baseline in `../qwen37flash-five/`
- Tasks: `evals/` 01–20, janetcode only, three runs with the eval driver
  and one with the headless driver (`--janetcode-driver headless`, the
  product path with the full tool surface)
- Tree: master at 6fec002, after the ~90 commits of that day (agent loop
  robustness, tool fixes, the TUI passes, the feature catalogue and the
  harness prompt section)

## Totals

| run | passed | fresh in | cache read | output | tool calls | turns | wall |
|---|---:|---:|---:|---:|---:|---:|---:|
| baseline, 3 runs | 60/60 | 298,758 | 811,228 | 27,707 | 228 | 242 | 560s |
| now, 3 runs | 60/60 | 311,859 (+4.4%) | 804,099 (−0.9%) | 26,196 (−5.5%) | 208 (−8.8%) | 230 (−5.0%) | 589s |
| now, headless, 1 run | 20/20 | 138,786 | 339,894 | 8,163 | 68 | 73 | 341s |

At list price the three eval-driver runs cost $0.0174 before and $0.0176
after (+1%). The headless run cost $0.0073 against a mean eval-driver
run of $0.0059: +33% fresh input for the full tool surface, at the same
pass rate and tool-call count, and a longer wall because it is one
process per task.

## Where the +4.4% fresh input comes from

All of it is the harness section added to the system prompt (312
estimated tokens, budget 350). The single-call tasks pin it: task 15 was
4,456 fresh tokens in every baseline run and is 4,771–4,805 now; task 18
was 4,367–4,398 and is 4,681–4,682. A flat +315 on the first request of
every task, the same on a one-file task as on an eight-call task, so it
is not the repomap, the path rules or the plan cap. Tool schemas are
unchanged at 4.4k for 18 tools; the deferred introspect tool does not
appear in requests.

## What it bought

The tasks that improved most are the ones where the baseline model
re-read files it had already seen, which is what the section tells it
not to do:

| task | cache read | tool calls (mean) |
|---|---|---|
| 13-multi-file-package | 18,541 → 9,589 | 5.7 → 3.0 |
| 20-env-default | 12,953 → 9,173 | 3.3 → 2.0 |
| 01-create-file | 7,136 → 4,549 | 1.7 → 1.0 |
| 11-go-return-error | 16,662 → 14,403 | 5.3 → 4.0 |

Two tasks moved the other way and look like single trajectories rather
than shifts: 17-csv-sort ran 3·3·9 tool calls (baseline 3·3·3, the nine
still passed) and 10-append-preserve ran 5·3·3 (baseline 3·3·2). Three
more runs of task 10 alone would settle the second.

## Also found

The eval driver handed every concurrent task one provider with one
pacer, so a single 429 widened the gap for all of them while the same
tasks as separate processes paid nothing. Each task-run now gets a
sibling provider with its own pacer; the key ring and the learned
dialect stay shared (commit e73fbed).

## Reproduce

```
./bin/harnessbench --evals evals --harnesses janetcode \
  --model qwen/qwen3.7-flash --base-url https://openrouter.ai/api/v1 --out run1
./bin/harnessbench --evals evals --harnesses janetcode --janetcode-driver headless \
  --model qwen/qwen3.7-flash --base-url https://openrouter.ai/api/v1 --out headless
```
