# Harness benchmark report

- Model: `qwen3.7-plus`
- Endpoint: `https://coding-intl.dashscope.aliyuncs.com/v1`
- Tasks: 01–20 in four batches of five (01–05, 06–10, 11–15, 16–20)
- When: 2026-09-09T11:24:16Z
- Note: pass/fail is the eval JSON shell check; token fields are only those the harness emitted. Limits: 5m0s wall budget for every harness and a fresh 2m for each check; 40 model calls per task for qwencode, hermes-agent, grok and janetcode (pi has no cap flag); hermes-agent runs with its terminal toolset only; the tasks' max_tool_calls are not enforced for any harness. Input tokens exclude cache reads for every harness (qwencode and hermes-agent report prompt totals that include them; the bench subtracts)

Token columns are **input tokens**, **cache read tokens**, **cache write tokens**, and **output tokens**. Wall time is the measured task duration. A field the harness did not report is `n/a`, not an estimate.

## Per-harness totals

| harness | passed | unavailable | wall time | input tokens | cache read tokens | cache write tokens | output tokens | tool calls | turns | cost USD |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| janetcode | 3/3 | 0 | 10m14.97s | 94962 | 802220 | 0 | 19803 | 99 | 88 | n/a |

## Per-task wall time

| task | janetcode |
|---|---:|
| 41-session-go-service | 4m24.98s |
| 42-session-records | 2m39.63s |
| 43-session-python-ledger | 3m10.35s |

## Per-task input tokens

| task | janetcode |
|---|---:|
| 41-session-go-service | 43838 |
| 42-session-records | 21647 |
| 43-session-python-ledger | 29477 |

## Per-task cache read tokens

| task | janetcode |
|---|---:|
| 41-session-go-service | 417885 |
| 42-session-records | 165088 |
| 43-session-python-ledger | 219247 |

## Per-task cache write tokens

| task | janetcode |
|---|---:|
| 41-session-go-service | 0 |
| 42-session-records | 0 |
| 43-session-python-ledger | 0 |

## Per-task output tokens

| task | janetcode |
|---|---:|
| 41-session-go-service | 8236 |
| 42-session-records | 4963 |
| 43-session-python-ledger | 6604 |

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
| janetcode | 9720 | 138757 | 59814 | 68613 | 78829 | 69764 | 36226 | 461723 |

### 42-session-records

| harness | p1 | p2 | p3 | p4 | p5 | p6 | total |
|---|---:|---:|---:|---:|---:|---:|---:|
| janetcode | 30053 | 21221 | 24388 | 34011 | 45829 | 31233 | 186735 |

### 43-session-python-ledger

| harness | p1 | p2 | p3 | p4 | p5 | p6 | total |
|---|---:|---:|---:|---:|---:|---:|---:|
| janetcode | 7634 | 40318 | 43822 | 52064 | 76344 | 28542 | 248724 |

## Failures and unavailable harnesses

None.

