# Harness benchmark report

- Model: `qwen3.7-plus`
- Endpoint: `https://coding-intl.dashscope.aliyuncs.com/v1`
- Tasks: 01–20 in four batches of five (01–05, 06–10, 11–15, 16–20)
- When: 2026-09-09T12:44:35Z
- Note: pass/fail is the eval JSON shell check; token fields are only those the harness emitted. Limits: 6m0s wall budget for every harness and a fresh 2m for each check; 40 model calls per task for qwencode, hermes-agent, grok and janetcode (pi has no cap flag); hermes-agent runs with its terminal toolset only; the tasks' max_tool_calls are not enforced for any harness. Input tokens exclude cache reads for every harness (qwencode and hermes-agent report prompt totals that include them; the bench subtracts)

Token columns are **input tokens**, **cache read tokens**, **cache write tokens**, and **output tokens**. Wall time is the measured task duration. A field the harness did not report is `n/a`, not an estimate.

## Per-harness totals

| harness | passed | unavailable | wall time | input tokens | cache read tokens | cache write tokens | output tokens | tool calls | turns | cost USD |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| claudecode | 3/3 | 0 | 5m36.45s | 3785 | 4194639 | 91496 | 28081 | n/a | 126 | 1.493292 |

## Per-task wall time

| task | claudecode |
|---|---:|
| 41-session-go-service | 2m16.71s |
| 42-session-records | 1m2s |
| 43-session-python-ledger | 2m17.73s |

## Per-task input tokens

| task | claudecode |
|---|---:|
| 41-session-go-service | 1402 |
| 42-session-records | 1130 |
| 43-session-python-ledger | 1253 |

## Per-task cache read tokens

| task | claudecode |
|---|---:|
| 41-session-go-service | 2340949 |
| 42-session-records | 508702 |
| 43-session-python-ledger | 1344988 |

## Per-task cache write tokens

| task | claudecode |
|---|---:|
| 41-session-go-service | 41583 |
| 42-session-records | 19239 |
| 43-session-python-ledger | 30674 |

## Per-task output tokens

| task | claudecode |
|---|---:|
| 41-session-go-service | 13182 |
| 42-session-records | 4026 |
| 43-session-python-ledger | 10873 |

## Per-task pass/fail

| task | claudecode |
|---|---:|
| 41-session-go-service | PASS |
| 42-session-records | PASS |
| 43-session-python-ledger | PASS |

## Session context growth

Per prompt of each session task: input + cache tokens the harness read during that prompt (the last column is the task total). A flat row means the history was held down; a rising row means it was resent whole.

### 41-session-go-service

| harness | p1 | p2 | p3 | p4 | p5 | p6 | p7 | total |
|---|---:|---:|---:|---:|---:|---:|---:|---:|
| claudecode | 103954 | 317939 | 143900 | 356847 | 1083237 | 267973 | 110084 | 2383934 |

### 42-session-records

| harness | p1 | p2 | p3 | p4 | p5 | p6 | total |
|---|---:|---:|---:|---:|---:|---:|---:|
| claudecode | 72504 | 50616 | 79174 | 82451 | 148514 | 95812 | 529071 |

### 43-session-python-ledger

| harness | p1 | p2 | p3 | p4 | p5 | p6 | total |
|---|---:|---:|---:|---:|---:|---:|---:|
| claudecode | 98653 | 238126 | 133650 | 404836 | 413905 | 87745 | 1376915 |

## Failures and unavailable harnesses

None.

