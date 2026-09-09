# Harness benchmark report

- Model: `qwen3.7-plus`
- Endpoint: `https://coding-intl.dashscope.aliyuncs.com/v1`
- Tasks: 01–20 in four batches of five (01–05, 06–10, 11–15, 16–20)
- When: 2026-09-09T13:31:45Z
- Note: pass/fail is the eval JSON shell check; token fields are only those the harness emitted. Limits: 5m0s wall budget for every harness and a fresh 2m for each check; 40 model calls per task for qwencode, hermes-agent, grok and janetcode (pi has no cap flag); hermes-agent runs with its terminal toolset only; the tasks' max_tool_calls are not enforced for any harness. Input tokens exclude cache reads for every harness (qwencode and hermes-agent report prompt totals that include them; the bench subtracts)

Token columns are **input tokens**, **cache read tokens**, **cache write tokens**, and **output tokens**. Wall time is the measured task duration. A field the harness did not report is `n/a`, not an estimate.

## Per-harness totals

| harness | passed | unavailable | wall time | input tokens | cache read tokens | cache write tokens | output tokens | tool calls | turns | cost USD |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| janetcode | 3/3 | 0 | 9m25.73s | 96429 | 732612 | 0 | 17682 | 102 | 83 | n/a |

## Per-task wall time

| task | janetcode |
|---|---:|
| 41-session-go-service | 3m49.27s |
| 42-session-records | 2m3.93s |
| 43-session-python-ledger | 3m32.51s |

## Per-task input tokens

| task | janetcode |
|---|---:|
| 41-session-go-service | 42056 |
| 42-session-records | 17682 |
| 43-session-python-ledger | 36691 |

## Per-task cache read tokens

| task | janetcode |
|---|---:|
| 41-session-go-service | 349581 |
| 42-session-records | 115343 |
| 43-session-python-ledger | 267688 |

## Per-task cache write tokens

| task | janetcode |
|---|---:|
| 41-session-go-service | 0 |
| 42-session-records | 0 |
| 43-session-python-ledger | 0 |

## Per-task output tokens

| task | janetcode |
|---|---:|
| 41-session-go-service | 7365 |
| 42-session-records | 3728 |
| 43-session-python-ledger | 6589 |

## Per-task pass/fail

| task | janetcode |
|---|---:|
| 41-session-go-service | PASS |
| 42-session-records | PASS |
| 43-session-python-ledger | PASS |

## Session context growth

Per prompt of each session task: input + cache tokens the harness read during that prompt (the last column is the task total). A flat row means the history was held down; a rising row means it was resent whole.

### 41-session-go-service

| harness | p1 | p2 | p3 | p4 | p5 | p6 | p7 | total |
|---|---:|---:|---:|---:|---:|---:|---:|---:|
| janetcode | 10028 | 101081 | 31926 | 73526 | 58276 | 82270 | 34530 | 391637 |

### 42-session-records

| harness | p1 | p2 | p3 | p4 | p5 | p6 | total |
|---|---:|---:|---:|---:|---:|---:|---:|
| janetcode | 19426 | 16823 | 19914 | 20653 | 29791 | 26418 | 133025 |

### 43-session-python-ledger

| harness | p1 | p2 | p3 | p4 | p5 | p6 | total |
|---|---:|---:|---:|---:|---:|---:|---:|
| janetcode | 7892 | 56426 | 69073 | 49049 | 88890 | 33049 | 304379 |

## Failures and unavailable harnesses

None.

