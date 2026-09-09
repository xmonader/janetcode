# Harness benchmark report

- Model: `qwen3.7-plus`
- Endpoint: `https://coding-intl.dashscope.aliyuncs.com/v1`
- Tasks: 01–20 in four batches of five (01–05, 06–10, 11–15, 16–20)
- When: 2026-09-09T11:18:36Z
- Note: pass/fail is the eval JSON shell check; token fields are only those the harness emitted. Limits: 5m0s wall budget for every harness and a fresh 2m for each check; 40 model calls per task for qwencode, hermes-agent, grok and janetcode (pi has no cap flag); hermes-agent runs with its terminal toolset only; the tasks' max_tool_calls are not enforced for any harness. Input tokens exclude cache reads for every harness (qwencode and hermes-agent report prompt totals that include them; the bench subtracts)

Token columns are **input tokens**, **cache read tokens**, **cache write tokens**, and **output tokens**. Wall time is the measured task duration. A field the harness did not report is `n/a`, not an estimate.

## Per-harness totals

| harness | passed | unavailable | wall time | input tokens | cache read tokens | cache write tokens | output tokens | tool calls | turns | cost USD |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| pi | 3/3 | 0 | 8m14.02s | 191661 | 1759872 | 0 | 15766 | n/a | 79 | 0.000000 |
| janetcode | 3/3 | 0 | 8m53.79s | 92299 | 772550 | 0 | 17332 | 96 | 80 | n/a |
| qwencode | 3/3 | 0 | 10m50.42s | 2290545 | 14073178 | n/a | 74623 | 439 | 84 | n/a |
| grok | 3/3 | 0 | 12m9.94s | 248069 | 3060608 | 0 | 20667 | n/a | 105 | n/a |

## Per-task wall time

| task | pi | janetcode | qwencode | grok |
|---|---:|---:|---:|---:|
| 41-session-go-service | 3m50.1s | 4m24.7s | 4m18.86s | 5m36.21s |
| 42-session-records | 1m42.59s | 1m43.14s | 2m44.25s | 2m51.37s |
| 43-session-python-ledger | 2m41.33s | 2m45.94s | 3m47.3s | 3m42.36s |

## Per-task input tokens

| task | pi | janetcode | qwencode | grok |
|---|---:|---:|---:|---:|
| 41-session-go-service | 94545 | 41261 | 967069 | 112755 |
| 42-session-records | 31467 | 18700 | 601443 | 66863 |
| 43-session-python-ledger | 65649 | 32338 | 722033 | 68451 |

## Per-task cache read tokens

| task | pi | janetcode | qwencode | grok |
|---|---:|---:|---:|---:|
| 41-session-go-service | 904320 | 430539 | 6546200 | 1716352 |
| 42-session-records | 430976 | 148794 | 3452531 | 543744 |
| 43-session-python-ledger | 424576 | 193217 | 4074447 | 800512 |

## Per-task cache write tokens

| task | pi | janetcode | qwencode | grok |
|---|---:|---:|---:|---:|
| 41-session-go-service | 0 | 0 | n/a | 0 |
| 42-session-records | 0 | 0 | n/a | 0 |
| 43-session-python-ledger | 0 | 0 | n/a | 0 |

## Per-task output tokens

| task | pi | janetcode | qwencode | grok |
|---|---:|---:|---:|---:|
| 41-session-go-service | 7182 | 9150 | 35036 | 7930 |
| 42-session-records | 2635 | 2397 | 12329 | 5822 |
| 43-session-python-ledger | 5949 | 5785 | 27258 | 6915 |

## Per-task pass/fail

| task | pi | janetcode | qwencode | grok |
|---|---:|---:|---:|---:|
| 41-session-go-service | PASS | PASS | PASS | PASS |
| 42-session-records | PASS | PASS | PASS | PASS |
| 43-session-python-ledger | PASS | PASS | PASS | PASS |

## Session context growth

Per prompt of each session task: input + cache tokens the harness read during that prompt (the last column is the task total). A flat row means the history was held down; a rising row means it was resent whole.

### 41-session-go-service

| harness | p1 | p2 | p3 | p4 | p5 | p6 | p7 | total |
|---|---:|---:|---:|---:|---:|---:|---:|---:|
| pi | 58539 | 168498 | 75875 | 160192 | 318818 | 154165 | 62778 | 998865 |
| janetcode | 9246 | 68760 | 56516 | 90320 | 121299 | 88051 | 37608 | 471800 |
| qwencode | 88105 | 622942 | 830681 | 1161112 | 1396700 | 1644097 | 1769632 | 7513269 |
| grok | 176381 | 421560 | 129049 | 201653 | 664371 | 156666 | 79427 | 1829107 |

### 42-session-records

| harness | p1 | p2 | p3 | p4 | p5 | p6 | total |
|---|---:|---:|---:|---:|---:|---:|---:|
| pi | 97326 | 61857 | 84518 | 64355 | 109965 | 44422 | 462443 |
| janetcode | 27860 | 31857 | 25377 | 26120 | 37000 | 19280 | 167494 |
| qwencode | 218730 | 352592 | 581465 | 721460 | 1014082 | 1165645 | 4053974 |
| grok | 127012 | 82488 | 86436 | 88151 | 124889 | 101631 | 610607 |

### 43-session-python-ledger

| harness | p1 | p2 | p3 | p4 | p5 | p6 | total |
|---|---:|---:|---:|---:|---:|---:|---:|
| pi | 56917 | 88748 | 71579 | 102094 | 111841 | 59046 | 490225 |
| janetcode | 10244 | 26170 | 38199 | 58038 | 59222 | 33682 | 225555 |
| qwencode | 86686 | 467060 | 726623 | 949172 | 1189142 | 1377797 | 4796480 |
| grok | 71483 | 213366 | 146058 | 124159 | 240124 | 73773 | 868963 |

## Failures and unavailable harnesses

None.

