# Compaction pressure test

- Model: `qwen3.7-plus` at `https://coding-intl.dashscope.aliyuncs.com/v1`
- When: 2026-09-09T09:19:37Z
- Commit: `adeddd6` (warm-prefix summaries, rich-skip, verbatim asks)

Neither eval set reaches the default compaction threshold (80k estimated tokens), so `--eval-compact-at 8000` forces the whole pipeline — pruning tiers, attention-ordered placeholders, the working-memory summary — onto tasks that pass without it. The question is what compaction costs and what it loses. Context tokens are input + cache read + cache write over the run; "summaries" is the summariser calls' share of that.

## What these totals are and are not

Every row below is one run of one configuration. The session benchmark
measures this task set's run-to-run spread at 21%, so a difference under
that is not a difference. Re-reading these same files through the paired
bootstrap that `--eval-report <file> --eval-compare <base>` now prints:

| pair | context ratio | 95% interval | reading |
|---|---:|---|---|
| hard-baseline → hard-compact8k | x1.17 | [0.92, 1.76] | inside the noise |
| large-baseline → large-compact8k | x0.92 | [0.58, 1.51] | inside the noise |

So forcing compaction at 8k neither demonstrably cost context nor saved it
on these sets, at one run each. What the tables below do show, and what
does not depend on the interval, is the *shape*: how often the pipeline
fired, what the summariser calls cost, and which tasks still passed.

## Totals

| run | passed | context tokens | per solved | compactions | summary calls (in / cached / out) |
|---|---:|---:|---:|---:|---|
| hard-baseline | 6/6 | 115,218 | 19,203 | 0 | 0 / 0 / 0 |
| hard-compact8k | 6/6 | 134,336 | 22,389 | 1 | 1,347 / 2,034 / 776 |
| hard-compact8k-norichskip | 5/6 | 105,540 | 21,108 | 1 | 745 / 2,136 / 1,037 |
| large-baseline | 5/5 | 187,405 | 37,481 | 0 | 0 / 0 / 0 |
| large-compact8k | 5/5 | 172,924 | 34,584 | 6 | 15,608 / 10,172 / 3,983 |

## Per task

| task | baseline context | forced-8k context | compactions | summary in / cached / out | forced pass |
|---|---:|---:|---:|---|---|
| 21-go-data-race | 11,799 | 11,351 | 0 | 0 / 0 / 0 | PASS |
| 22-go-interface-refactor | 13,533 | 38,842 | 0 | 0 / 0 / 0 | PASS |
| 23-python-ini-parser | 18,790 | 16,498 | 1 | 1,347 / 2,034 / 776 | PASS |
| 24-rename-everywhere | 16,992 | 16,498 | 0 | 0 / 0 / 0 | PASS |
| 25-pagination-bugs | 22,388 | 21,093 | 0 | 0 / 0 / 0 | PASS |
| 26-log-summary-script | 31,716 | 30,054 | 0 | 0 / 0 / 0 | PASS |
| 31-large-inventory | 17,997 | 7,263 | 0 | 0 / 0 / 0 | PASS |
| 32-large-rename | 34,263 | 32,508 | 2 | 8,537 / 4,068 / 1,379 | PASS |
| 33-large-sum | 30,333 | 14,591 | 0 | 0 / 0 / 0 | PASS |
| 34-large-headers | 20,606 | 48,049 | 1 | 2,331 / 2,034 / 664 | PASS |
| 35-large-deadcode | 84,206 | 70,513 | 3 | 4,740 / 4,070 / 1,940 | PASS |

## Reading

- Forcing compaction lost no task: 11/11 pass with it, 11/11 without.
- On the large set the forced run read **less** context in total (172k vs 187k, ×0.92 per solved task): the head that compaction drops was being re-read on every call before.
- On the hard set most tasks never crossed 8k even when forced — the pruning tiers (superseded reads, trimmed old results, placeholders) brought them back under the threshold without a summary, so only one summary ran across six tasks.
- The summariser call replays the conversation's own prefix; the cached share of its input was 10.2k of 25.8k on the large set, so the head is served from cache rather than re-billed.
- `--eval-off richskip` (always call the summariser, the pre-change behaviour) changed nothing on the hard set but one timeout unrelated to compaction (no summary ran in that task); with a single summary per run there is not enough signal to rank rich-skip here — the session benchmark is where it matters.

## Re-run

```sh
export OPENAI_API_KEY=...   # never in a file
export OPENAI_BASE_URL=https://coding-intl.dashscope.aliyuncs.com/v1
make build
./bin/janetcode --eval evals --eval-only hard --provider openai --model qwen3.7-plus --eval-json docs/benchmarks/compaction/hard-baseline.json --eval-label hard-baseline --eval-no-tool-limit --eval-max-turns 40
./bin/janetcode --eval evals --eval-only hard --eval-compact-at 8000 --eval-compare docs/benchmarks/compaction/hard-baseline.json ... --eval-json docs/benchmarks/compaction/hard-compact8k.json
./bin/janetcode --eval evals/large --eval-compact-at 8000 --eval-compare docs/benchmarks/compaction/large-baseline.json ...
```
