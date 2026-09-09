# Harness benchmark report

- Model: `qwen3.7-plus`
- Endpoint: `https://coding-intl.dashscope.aliyuncs.com/v1`
- Tasks: 01–20 in four batches of five (01–05, 06–10, 11–15, 16–20)
- When: 2026-09-09T12:46:07Z
- Note: pass/fail is the eval JSON shell check; token fields are only those the harness emitted. Limits: 6m0s wall budget for every harness and a fresh 2m for each check; 40 model calls per task for qwencode, hermes-agent, grok and janetcode (pi has no cap flag); hermes-agent runs with its terminal toolset only; the tasks' max_tool_calls are not enforced for any harness. Input tokens exclude cache reads for every harness (qwencode and hermes-agent report prompt totals that include them; the bench subtracts)

Token columns are **input tokens**, **cache read tokens**, **cache write tokens**, and **output tokens**. Wall time is the measured task duration. A field the harness did not report is `n/a`, not an estimate.

## Per-harness totals

| harness | passed | unavailable | wall time | input tokens | cache read tokens | cache write tokens | output tokens | tool calls | turns | cost USD |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| claudecode | 3/3 | 0 | 4m14.16s | 2965 | 3226513 | 104824 | 15802 | n/a | 80 | 2.451033 |

## Per-task wall time

| task | claudecode |
|---|---:|
| 41-session-go-service | 1m31.15s |
| 42-session-records | 1m12.77s |
| 43-session-python-ledger | 1m30.24s |

## Per-task input tokens

| task | claudecode |
|---|---:|
| 41-session-go-service | 984 |
| 42-session-records | 998 |
| 43-session-python-ledger | 983 |

## Per-task cache read tokens

| task | claudecode |
|---|---:|
| 41-session-go-service | 1282286 |
| 42-session-records | 648738 |
| 43-session-python-ledger | 1295489 |

## Per-task cache write tokens

| task | claudecode |
|---|---:|
| 41-session-go-service | 38927 |
| 42-session-records | 26315 |
| 43-session-python-ledger | 39582 |

## Per-task output tokens

| task | claudecode |
|---|---:|
| 41-session-go-service | 6709 |
| 42-session-records | 1791 |
| 43-session-python-ledger | 7302 |

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
| claudecode | 123208 | 236644 | 147929 | 310564 | 219535 | 169658 | 114659 | 1322197 |

### 42-session-records

| harness | p1 | p2 | p3 | p4 | p5 | p6 | total |
|---|---:|---:|---:|---:|---:|---:|---:|
| claudecode | 160007 | 82722 | 126356 | 85748 | 131791 | 89427 | 676051 |

### 43-session-python-ledger

| harness | p1 | p2 | p3 | p4 | p5 | p6 | total |
|---|---:|---:|---:|---:|---:|---:|---:|
| claudecode | 162138 | 319913 | 196703 | 208088 | 333667 | 115545 | 1336054 |

## Failures and unavailable harnesses

None.

