# Harness benchmark report

- Model: `anthropic/claude-sonnet-4.5`
- Endpoint: `https://openrouter.ai/api/v1`
- Tasks: 01–20 in four batches of five (01–05, 06–10, 11–15, 16–20)
- When: 2026-09-09T13:27:55Z
- Note: pass/fail is the eval JSON shell check; token fields are only those the harness emitted. Limits: 6m0s wall budget for every harness and a fresh 2m for each check; 40 model calls per task for qwencode, hermes-agent, grok and janetcode (pi has no cap flag); hermes-agent runs with its terminal toolset only; the tasks' max_tool_calls are not enforced for any harness. Input tokens exclude cache reads for every harness (qwencode and hermes-agent report prompt totals that include them; the bench subtracts)

Token columns are **input tokens**, **cache read tokens**, **cache write tokens**, and **output tokens**. Wall time is the measured task duration. A field the harness did not report is `n/a`, not an estimate.

## Per-harness totals

| harness | passed | unavailable | wall time | input tokens | cache read tokens | cache write tokens | output tokens | tool calls | turns | cost USD |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| janetcode | 3/3 | 0 | 7m24.63s | 121361 | 1501622 | 0 | 25111 | 152 | 106 | 1.259503 |

## Per-task wall time

| task | janetcode |
|---|---:|
| 41-session-go-service | 3m13.25s |
| 42-session-records | 1m3.42s |
| 43-session-python-ledger | 3m7.95s |

## Per-task input tokens

| task | janetcode |
|---|---:|
| 41-session-go-service | 56546 |
| 42-session-records | 12559 |
| 43-session-python-ledger | 52256 |

## Per-task cache read tokens

| task | janetcode |
|---|---:|
| 41-session-go-service | 697737 |
| 42-session-records | 202934 |
| 43-session-python-ledger | 600951 |

## Per-task cache write tokens

| task | janetcode |
|---|---:|
| 41-session-go-service | 0 |
| 42-session-records | 0 |
| 43-session-python-ledger | 0 |

## Per-task output tokens

| task | janetcode |
|---|---:|
| 41-session-go-service | 12077 |
| 42-session-records | 1981 |
| 43-session-python-ledger | 11053 |

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
| janetcode | 11460 | 67256 | 122030 | 151617 | 225148 | 116099 | 60673 | 754283 |

### 42-session-records

| harness | p1 | p2 | p3 | p4 | p5 | p6 | total |
|---|---:|---:|---:|---:|---:|---:|---:|
| janetcode | 55005 | 25586 | 26773 | 27735 | 49308 | 31086 | 215493 |

### 43-session-python-ledger

| harness | p1 | p2 | p3 | p4 | p5 | p6 | total |
|---|---:|---:|---:|---:|---:|---:|---:|
| janetcode | 13353 | 97134 | 117344 | 107990 | 262793 | 54593 | 653207 |

## Failures and unavailable harnesses

None.

