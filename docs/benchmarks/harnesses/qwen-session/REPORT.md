# Five harnesses on the session set, qwen3.7-plus, twice

- Model: `qwen3.7-plus` at `https://coding-intl.dashscope.aliyuncs.com/v1`
- Tasks: `evals/session/` 41–43 — three multi-prompt sessions of six or
  seven dependent prompts each (a Go service built up step by step, a
  records search-and-edit series, a Python ledger). Each prompt is one
  non-interactive invocation continuing the harness's own session in the
  same tree; a five-minute budget per prompt
- Two complete repeats, identical seeded directories; 30 session runs,
  186 prompts, recorded 2026-09-10. A third repeat was started and lost:
  the endpoint's usage quota ran out during it and every harness failed
  from its first prompt (janetcode with an explicit HTTP 429, the others
  with nothing written). It is not counted and will be re-run when the
  quota returns.

## Which harnesses continue a session

pi (`--session-id`), qwencode (`--session-id` / `--resume`), janetcode
(`--resume`) and openclaw (`--session-id`, given one id per session by
the launcher for this series — it had been given a fresh id per prompt)
carry the conversation across prompts. hermes-agent has no
non-interactive continuation: every prompt starts fresh, and its result
here is what that costs.

## Totals over two runs

| harness | passed | fresh input | cache read | output | total read | model calls | wall | cost at list | spread |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| **janetcode** | **6/6** | **226,789** | **1,734,550** | 31,910 | **1,993,249** | 145 | 1166s | **$0.22** | 15% |
| pi | **6/6** | 329,858 | 3,532,544 | **30,864** | 3,893,266 | 156 | **961s** | $0.37 | 11% |
| openclaw | **6/6** | 439,078 | 5,579,648 | 36,728 | 6,055,454 | **145** | 1389s | $0.55 | 21% |
| qwencode | **6/6** | 4,943,865 | 29,110,660 | 149,526 | 34,204,051 | 173 | 1359s | $3.64 | **1%** |
| hermes-agent | 2/6 | 473,602 | 1,700,224 | 57,503 | 2,231,329 | 259 | 1882s | $0.33 | 13% |

Cost at list price: $0.32 per million fresh, $0.064 cache read, $1.28
output.

This is the shape the harness is built for, and the gap is wider than on
single prompts. Over six or seven dependent prompts, qwencode re-reads
34M tokens of context to janetcode's 2M — seventeen times — and its bill
is sixteen times. openclaw is three times janetcode's context; pi twice.
On fresh input alone pi is 1.5x, openclaw 1.9x, qwencode 22x.

Every harness that continues a session passed all six sessions in both
runs, so as on the single-prompt sets the pass column does not separate
them; the cost column does, and by more.

## Reproducibility

| harness | fresh input per run |
|---|---|
| janetcode | 121,343 · 105,446 |
| pi | 173,396 · 156,462 |
| hermes-agent | 222,192 · 251,410 |
| openclaw | 240,212 · 198,866 |
| qwencode | 2,484,566 · 2,459,299 |

Two runs give a range, not a spread; the earlier janetcode-only series
measured 21% on this set over three. Treat differences under about 25%
between two harnesses here as unresolved.

## hermes-agent without continuation

Its four failures are what starting every prompt fresh costs: on the
records session it did the file work but its final reply did not state
the count the sixth prompt asked for, because it had no memory of the
first five; on the service and ledger sessions the later prompts build
on state only a continued conversation carries. It passed the service
session once and the records session once, by re-deriving the state from
the tree. That it passed at all on 259 model calls — the most of any
harness — says the tree carried enough; that it failed four of six says
the tree is not the conversation.

## Reproduce

```
./bin/harnessbench --evals evals/session --filter session \
  --harnesses janetcode,pi,hermes-agent,qwencode,openclaw \
  --model qwen3.7-plus --base-url https://coding-intl.dashscope.aliyuncs.com/v1 \
  --timeout 5m --out run1
```
