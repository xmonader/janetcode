# Harness benchmark report

- Model: `anthropic/claude-haiku-4.5`
- Endpoint: `https://openrouter.ai/api/v1`
- Tasks: 01–20 in four batches of five (01–05, 06–10, 11–15, 16–20)
- When: 2026-09-09T12:36:57Z
- Note: pass/fail is the eval JSON shell check; token fields are only those the harness emitted. Limits: 6m0s wall budget for every harness and a fresh 2m for each check; 40 model calls per task for qwencode, hermes-agent, grok and janetcode (pi has no cap flag); hermes-agent runs with its terminal toolset only; the tasks' max_tool_calls are not enforced for any harness. Input tokens exclude cache reads for every harness (qwencode and hermes-agent report prompt totals that include them; the bench subtracts)

Token columns are **input tokens**, **cache read tokens**, **cache write tokens**, and **output tokens**. Wall time is the measured task duration. A field the harness did not report is `n/a`, not an estimate.

## Per-harness totals

| harness | passed | unavailable | wall time | input tokens | cache read tokens | cache write tokens | output tokens | tool calls | turns | cost USD |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| janetcode | 3/3 | 0 | 5m46.93s | 141476 | 2472188 | 0 | 26974 | 147 | 148 | 0.541726 |

## Per-task wall time

| task | janetcode |
|---|---:|
| 41-session-go-service | 2m29.52s |
| 42-session-records | 39.73s |
| 43-session-python-ledger | 2m37.68s |

## Per-task input tokens

| task | janetcode |
|---|---:|
| 41-session-go-service | 59567 |
| 42-session-records | 21910 |
| 43-session-python-ledger | 59999 |

## Per-task cache read tokens

| task | janetcode |
|---|---:|
| 41-session-go-service | 1209026 |
| 42-session-records | 154409 |
| 43-session-python-ledger | 1108753 |

## Per-task cache write tokens

| task | janetcode |
|---|---:|
| 41-session-go-service | 0 |
| 42-session-records | 0 |
| 43-session-python-ledger | 0 |

## Per-task output tokens

| task | janetcode |
|---|---:|
| 41-session-go-service | 12011 |
| 42-session-records | 2609 |
| 43-session-python-ledger | 12354 |

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
| janetcode | 14628 | 146047 | 102194 | 191941 | 555026 | 158540 | 100217 | 1268593 |

### 42-session-records

| harness | p1 | p2 | p3 | p4 | p5 | p6 | total |
|---|---:|---:|---:|---:|---:|---:|---:|
| janetcode | 59102 | 21386 | 23865 | 16500 | 35748 | 19718 | 176319 |

### 43-session-python-ledger

| harness | p1 | p2 | p3 | p4 | p5 | p6 | total |
|---|---:|---:|---:|---:|---:|---:|---:|
| janetcode | 13005 | 121173 | 101289 | 405408 | 467651 | 60226 | 1168752 |

## Failures and unavailable harnesses

None.

