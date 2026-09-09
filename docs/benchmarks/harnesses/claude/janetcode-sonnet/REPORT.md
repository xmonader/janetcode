# Harness benchmark report

- Model: `anthropic/claude-sonnet-4.5`
- Endpoint: `https://openrouter.ai/api/v1`
- Tasks: 01–20 in four batches of five (01–05, 06–10, 11–15, 16–20)
- When: 2026-09-09T12:42:08Z
- Note: pass/fail is the eval JSON shell check; token fields are only those the harness emitted. Limits: 6m0s wall budget for every harness and a fresh 2m for each check; 40 model calls per task for qwencode, hermes-agent, grok and janetcode (pi has no cap flag); hermes-agent runs with its terminal toolset only; the tasks' max_tool_calls are not enforced for any harness. Input tokens exclude cache reads for every harness (qwencode and hermes-agent report prompt totals that include them; the bench subtracts)

Token columns are **input tokens**, **cache read tokens**, **cache write tokens**, and **output tokens**. Wall time is the measured task duration. A field the harness did not report is `n/a`, not an estimate.

## Per-harness totals

| harness | passed | unavailable | wall time | input tokens | cache read tokens | cache write tokens | output tokens | tool calls | turns | cost USD |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| janetcode | 3/3 | 0 | 10m25.48s | 128130 | 3125063 | 0 | 30484 | 174 | 173 | 1.834361 |

## Per-task wall time

| task | janetcode |
|---|---:|
| 41-session-go-service | 5m10.7s |
| 42-session-records | 1m4.63s |
| 43-session-python-ledger | 4m10.15s |

## Per-task input tokens

| task | janetcode |
|---|---:|
| 41-session-go-service | 64938 |
| 42-session-records | 11165 |
| 43-session-python-ledger | 52027 |

## Per-task cache read tokens

| task | janetcode |
|---|---:|
| 41-session-go-service | 1860546 |
| 42-session-records | 118428 |
| 43-session-python-ledger | 1146089 |

## Per-task cache write tokens

| task | janetcode |
|---|---:|
| 41-session-go-service | 0 |
| 42-session-records | 0 |
| 43-session-python-ledger | 0 |

## Per-task output tokens

| task | janetcode |
|---|---:|
| 41-session-go-service | 15471 |
| 42-session-records | 2043 |
| 43-session-python-ledger | 12970 |

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
| janetcode | 16519 | 154762 | 181259 | 350171 | 865966 | 246270 | 110537 | 1925484 |

### 42-session-records

| harness | p1 | p2 | p3 | p4 | p5 | p6 | total |
|---|---:|---:|---:|---:|---:|---:|---:|
| janetcode | 20928 | 16088 | 23652 | 18859 | 27489 | 22577 | 129593 |

### 43-session-python-ledger

| harness | p1 | p2 | p3 | p4 | p5 | p6 | total |
|---|---:|---:|---:|---:|---:|---:|---:|
| janetcode | 12985 | 117825 | 125282 | 353952 | 529284 | 58788 | 1198116 |

## Failures and unavailable harnesses

None.

