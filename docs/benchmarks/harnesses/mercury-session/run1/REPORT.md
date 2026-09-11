# Harness benchmark report

- Model: `inception/mercury-2.5`
- Endpoint: `https://openrouter.ai/api/v1`
- Tasks: 01–20 in four batches of five (01–05, 06–10, 11–15, 16–20)
- When: 2026-09-11T16:51:55Z
- Note: pass/fail is the eval JSON shell check; token fields are only those the harness emitted. Limits: 5m0s wall budget for every harness and a fresh 2m for each check; 40 model calls per task for qwencode, hermes-agent, grok and janetcode (pi has no cap flag); hermes-agent runs with its terminal toolset only; the tasks' max_tool_calls are not enforced for any harness. Input tokens exclude cache reads for every harness (qwencode and hermes-agent report prompt totals that include them; the bench subtracts)

Token columns are **input tokens**, **cache read tokens**, **cache write tokens**, and **output tokens**. Wall time is the measured task duration. A field the harness did not report is `n/a`, not an estimate.

## Per-harness totals

| harness | passed | unavailable | wall time | input tokens | cache read tokens | cache write tokens | output tokens | tool calls | turns | cost USD |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| pi | 3/3 | 0 | 2m17.56s | 1194073 | 554085 | 0 | 18094 | n/a | 68 | 0.052693 |
| janetcode | 2/3 | 0 | 2m19.2s | 1052534 | 292869 | 0 | 19776 | 122 | 88 | 0.046239 |
| qwencode | 2/3 | 0 | 4m25.73s | 11730575 | 4806874 | n/a | 80042 | 491 | 90 | n/a |
| hermes-agent | 2/3 | 0 | 4m1.64s | 770038 | 166800 | n/a | 30115 | n/a | 141 | n/a |
| openclaw | 2/3 | 0 | 5m18.85s | 2033517 | 1545180 | n/a | 23157 | 97 | 88 | n/a |

## Per-task wall time

| task | pi | janetcode | qwencode | hermes-agent | openclaw |
|---|---:|---:|---:|---:|---:|
| 41-session-go-service | 57.51s | 1m19.08s | 1m23.71s | 1m28.61s | 2m4.21s |
| 42-session-records | 39.02s | 20.44s | 1m12.41s | 1m13.39s | 1m29.47s |
| 43-session-python-ledger | 41.02s | 39.66s | 1m49.59s | 1m19.63s | 1m45.16s |

## Per-task input tokens

| task | pi | janetcode | qwencode | hermes-agent | openclaw |
|---|---:|---:|---:|---:|---:|
| 41-session-go-service | 465293 | 581546 | 4089029 | 276919 | 771342 |
| 42-session-records | 374651 | 136858 | 3243130 | 302504 | 526688 |
| 43-session-python-ledger | 354129 | 334130 | 4398416 | 190615 | 735487 |

## Per-task cache read tokens

| task | pi | janetcode | qwencode | hermes-agent | openclaw |
|---|---:|---:|---:|---:|---:|
| 41-session-go-service | 278205 | 210808 | 1502770 | 76701 | 755568 |
| 42-session-records | 145064 | 31177 | 1185770 | 49856 | 291034 |
| 43-session-python-ledger | 130816 | 50884 | 2118334 | 40243 | 498578 |

## Per-task cache write tokens

| task | pi | janetcode | qwencode | hermes-agent | openclaw |
|---|---:|---:|---:|---:|---:|
| 41-session-go-service | 0 | 0 | n/a | n/a | n/a |
| 42-session-records | 0 | 0 | n/a | n/a | n/a |
| 43-session-python-ledger | 0 | 0 | n/a | n/a | n/a |

## Per-task output tokens

| task | pi | janetcode | qwencode | hermes-agent | openclaw |
|---|---:|---:|---:|---:|---:|
| 41-session-go-service | 8101 | 8506 | 33624 | 10549 | 8962 |
| 42-session-records | 3054 | 2864 | 11422 | 7942 | 6399 |
| 43-session-python-ledger | 6939 | 8406 | 34996 | 11624 | 7796 |

## Per-task pass/fail

| task | pi | janetcode | qwencode | hermes-agent | openclaw |
|---|---:|---:|---:|---:|---:|
| 41-session-go-service | PASS | PASS | PASS | PASS | PASS |
| 42-session-records | PASS | PASS | PASS | PASS | PASS |
| 43-session-python-ledger | PASS | FAIL | FAIL | FAIL | FAIL |

## Session context growth

Per prompt of each session task: input + cache tokens the harness read during that prompt (the last column is the task total). A flat row means the history was held down; a rising row means it was resent whole.

### 41-session-go-service

| harness | p1 | p2 | p3 | p4 | p5 | p6 | p7 | total |
|---|---:|---:|---:|---:|---:|---:|---:|---:|
| pi | 57644 | 118632 | 51649 | 110733 | 180152 | 159380 | 65308 | 743498 |
| janetcode | 32219 | 202824 | 86556 | 202973 | 126809 | 83641 | 57332 | 792354 |
| qwencode | 85518 | 314867 | 506508 | 816449 | 1104460 | 1290060 | 1473937 | 5591799 |
| hermes-agent | 15390 | 44306 | 31891 | 53551 | 48385 | 25924 | 134173 | 353620 |
| openclaw | 106442 | 438120 | 168089 | 223634 | 337585 | 151783 | 101257 | 1526910 |

### 42-session-records

| harness | p1 | p2 | p3 | p4 | p5 | p6 | total |
|---|---:|---:|---:|---:|---:|---:|---:|
| pi | 134720 | 50818 | 76809 | 81355 | 115611 | 60402 | 519715 |
| janetcode | 34810 | 16420 | 26500 | 18636 | 50123 | 21546 | 168035 |
| qwencode | 275449 | 475498 | 680367 | 785205 | 1051525 | 1160856 | 4428900 |
| hermes-agent | 28775 | 35035 | 37809 | 53011 | 108116 | 89614 | 352360 |
| openclaw | 103997 | 103962 | 139180 | 70560 | 326385 | 73638 | 817722 |

### 43-session-python-ledger

| harness | p1 | p2 | p3 | p4 | p5 | p6 | total |
|---|---:|---:|---:|---:|---:|---:|---:|
| pi | 73030 | 84869 | 68032 | 123706 | 80314 | 54994 | 484945 |
| janetcode | 12117 | 35923 | 49741 | 137420 | 105240 | 44573 | 385014 |
| qwencode | 127161 | 449170 | 699902 | 1132782 | 1989441 | 2118294 | 6516750 |
| hermes-agent | 24418 | 61478 | 30987 | 31103 | 53088 | 29784 | 230858 |
| openclaw | 212779 | 231652 | 202396 | 218718 | 274254 | 94266 | 1234065 |

## Failures and unavailable harnesses

- **janetcode / 43-session-python-ledger** (batch 1): check failed: exit status 1
  - command: `janetcode --eval --eval-only 43-session-python-ledger --provider openai --model inception/mercury-2.5`
  - stderr: running 3 eval tasks × 1 with openai/inception/mercury-2.5 (3 at a time)
- **qwencode / 43-session-python-ledger** (batch 1): check failed: exit status 1
  - command: `qwen --yolo --output-format json --auth-type openai --openai-base-url https://openrouter.ai/api/v1 --model inception/mercury-2.5 --max-session-turns 40 --max-wall-time 300s --chat-recording --resume 3355e2ab-a03e-4b3c-995e-5cec86bb047e Run the tests and fix anything that fails. When they pass, reply with only the name of the CLI option you added and nothing else.`
- **hermes-agent / 43-session-python-ledger** (batch 1): check failed: exit status 1
  - command: `hermes-agent --query Run the tests and fix anything that fails. When they pass, reply with only the name of the CLI option you added and nothing else. --model inception/mercury-2.5 --base_url https://openrouter.ai/api/v1 --api_key *** --max_turns 40 --enabled_toolsets terminal --verbose`
  - stderr: tTokensDetails(audio_tokens=0, cached_tokens=1829, cache_write_tokens=0, video_tokens=0), cost=0.000327106, is_byok=False, cost_details={'upstream_inference_cost': 0.000327106, 'upstream_inference_prompt_cost': 0.000255556, 'upstream_inference_completions_cost': 7.155e-05})
- **openclaw / 43-session-python-ledger** (batch 1): check failed: exit status 1
  - command: `openclaw --profile janetbench-harness-openclaw-43-session-python-ledger-2767473966 agent --local --json --model bench/inception/mercury-2.5 --session-id bench-e7b7445a-17c9-473a-ae51-c1a8860fec29 --message Run the tests and fix anything that fails. When they pass, reply with only the name of the CLI option you added and nothing else.`
  - stderr: [33m[provider-transport-fetch][39m [36m[model-fetch] start provider=bench api=openai-completions model=inception/mercury-2.5 method=POST url=https://openrouter.ai/api/v1/chat/completions timeoutMs=undefined proxy=none policy=custom[39m

