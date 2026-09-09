# Harness benchmark report

- Model: `qwen3.7-plus`
- Endpoint: `https://coding-intl.dashscope.aliyuncs.com/v1`
- Tasks: 31–35 (five large evals, one batch of five); openclaw excluded
- When: 2026-09-08T22:55:11Z
- Note: pass/fail is the eval JSON shell check; token fields are only those the harness emitted; session input+cache-read may approach 1e6 tokens on a long tool loop

Token columns are **input tokens**, **cache read tokens**, **cache write tokens**, and **output tokens**. Wall time is the measured task duration. A field the harness did not report is `n/a`, not an estimate.

## Per-harness totals

| harness | passed | unavailable | wall time | input tokens | cache read tokens | cache write tokens | output tokens | tool calls | turns | cost USD |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| pi | 4/5 | 0 | 2m39.16s | 103759 | 514048 | 0 | 4463 | n/a | 29 | 0.000000 |
| janetcode | 4/5 | 0 | 2m27.33s | 52067 | 95900 | 0 | 3940 | 35 | 28 | n/a |
| qwencode | 5/5 | 0 | 3m48.7s | 1426698 | 1154710 | n/a | 6551 | 45 | 29 | n/a |
| hermes-agent | 1/5 | 0 | 22m51.61s | 1650549 | 1417728 | n/a | 22800 | n/a | 68 | n/a |
| grok | 5/5 | 0 | 2m55.47s | 175089 | 683264 | 0 | 4481 | n/a | 31 | n/a |

## Per-task wall time

| task | pi | janetcode | qwencode | hermes-agent | grok |
|---|---:|---:|---:|---:|---:|
| 31-large-inventory | 19.08s | 12.61s | 47.61s | 2m23.06s | 27.72s |
| 32-large-rename | 32.87s | 31.31s | 44.43s | 4m33.24s | 35.5s |
| 33-large-sum | 22.86s | 40.08s | 26.42s | 1m16.5s | 26.68s |
| 34-large-headers | 45.44s | 46.56s | 37.24s | 6m59.64s | 38.19s |
| 35-large-deadcode | 38.89s | 16.75s | 1m13s | 7m39.16s | 47.36s |

## Per-task input tokens

| task | pi | janetcode | qwencode | hermes-agent | grok |
|---|---:|---:|---:|---:|---:|
| 31-large-inventory | 24213 | 8740 | 250891 | 354895 | 36947 |
| 32-large-rename | 27006 | 15064 | 262794 | 435977 | 34869 |
| 33-large-sum | 8666 | 8439 | 179078 | 40981 | 26513 |
| 34-large-headers | 25042 | 7786 | 235753 | 315851 | 32996 |
| 35-large-deadcode | 18832 | 12038 | 498182 | 502845 | 43764 |

## Per-task cache read tokens

| task | pi | janetcode | qwencode | hermes-agent | grok |
|---|---:|---:|---:|---:|---:|
| 31-large-inventory | 53120 | 6168 | 195394 | 284544 | 127360 |
| 32-large-rename | 97664 | 19560 | 203673 | 380288 | 125184 |
| 33-large-sum | 89344 | 24160 | 132491 | 20864 | 93184 |
| 34-large-headers | 118912 | 29021 | 180984 | 277760 | 99712 |
| 35-large-deadcode | 155008 | 16991 | 442168 | 454272 | 237824 |

## Per-task cache write tokens

| task | pi | janetcode | qwencode | hermes-agent | grok |
|---|---:|---:|---:|---:|---:|
| 31-large-inventory | 0 | 0 | n/a | n/a | 0 |
| 32-large-rename | 0 | 0 | n/a | n/a | 0 |
| 33-large-sum | 0 | 0 | n/a | n/a | 0 |
| 34-large-headers | 0 | 0 | n/a | n/a | 0 |
| 35-large-deadcode | 0 | 0 | n/a | n/a | 0 |

## Per-task output tokens

| task | pi | janetcode | qwencode | hermes-agent | grok |
|---|---:|---:|---:|---:|---:|
| 31-large-inventory | 452 | 341 | 1604 | 1936 | 479 |
| 32-large-rename | 862 | 680 | 1372 | 8238 | 1059 |
| 33-large-sum | 568 | 815 | 432 | 1290 | 485 |
| 34-large-headers | 1454 | 1688 | 1038 | 3554 | 1250 |
| 35-large-deadcode | 1127 | 416 | 2105 | 7782 | 1208 |

## Per-task pass/fail

| task | pi | janetcode | qwencode | hermes-agent | grok |
|---|---:|---:|---:|---:|---:|
| 31-large-inventory | PASS | PASS | PASS | FAIL | PASS |
| 32-large-rename | FAIL | PASS | PASS | FAIL | PASS |
| 33-large-sum | PASS | PASS | PASS | PASS | PASS |
| 34-large-headers | PASS | PASS | PASS | FAIL | PASS |
| 35-large-deadcode | PASS | FAIL | PASS | FAIL | PASS |

## Failures and unavailable harnesses

- **pi / 32-large-rename** (batch 1): check failed: exit status 2: grep: widget.go: No such file or directory
  - command: `pi -p --mode json --provider dashscope --model qwen3.7-plus --no-session Rename the Go symbol Widget to Gadget everywhere in this module (source, tests, and comments in .go files). Keep the tests passing. The tree is large; inspect it with tools.`
  - stderr: grep: widget.go: No such file or directory
- **janetcode / 35-large-deadcode** (batch 1): check failed: exit status 1
  - command: `janetcode --eval --eval-only 35-large-deadcode --provider openai --model qwen3.7-plus`
  - stderr: running 5 eval tasks × 1 with openai/qwen3.7-plus (5 at a time)
- **hermes-agent / 31-large-inventory** (batch 1): check failed: exit status 2: sort: cannot read: inventory.txt: No such file or directory
  - command: `hermes-agent --query The data/ directory has many text files. Each file has an 'id: TOKEN_NNNN' line. Write inventory.txt containing every TOKEN_NNNN, one per line, sorted. Do not invent tokens. The tree is large; inspect it with tools. --model qwen3.7-plus --base_url https://coding-intl.dashscope.aliyuncs.com/v1 --api_key *** --max_turns 20 --enabled_toolsets terminal --verbose`
  - stderr: ntains "large-inventory" which matches the task description. This is clearly the test harness for this task. The user is asking me to process files in that data directory.
- **hermes-agent / 32-large-rename** (batch 1): check failed: exit status 1
  - command: `hermes-agent --query Rename the Go symbol Widget to Gadget everywhere in this module (source, tests, and comments in .go files). Keep the tests passing. The tree is large; inspect it with tools. --model qwen3.7-plus --base_url https://coding-intl.dashscope.aliyuncs.com/v1 --api_key *** --max_turns 20 --enabled_toolsets terminal --verbose`
  - stderr: with a `Widget()` function
- **hermes-agent / 34-large-headers** (batch 1): check failed: exit status 1
  - command: `hermes-agent --query Add exactly this first line to every .go file: // SPDX-License-Identifier: MIT
Do not duplicate it if you run twice. Keep go test passing. The tree is large; inspect it with tools. --model qwen3.7-plus --base_url https://coding-intl.dashscope.aliyuncs.com/v1 --api_key *** --max_turns 20 --enabled_toolsets terminal --verbose`
  - stderr: l .go files and keep go test passing. I've successfully:
- **hermes-agent / 35-large-deadcode** (batch 1): check failed: exit status 1
  - command: `hermes-agent --query Delete every DropNNN function (dead code). Keep every KeepNNN function and keep go test passing. The tree is large; inspect it with tools. --model qwen3.7-plus --base_url https://coding-intl.dashscope.aliyuncs.com/v1 --api_key *** --max_turns 20 --enabled_toolsets terminal --verbose`
  - stderr: /compress v1.17.8\ngo: downloading github.com/moby/sys/mountinfo v0.6.2\ngo: downloading github.com/lestrrat-go/backoff/v2 v2.0.8\ngo: downloading github.com/lestrrat-go/iter v1.0.1\ngo: downloading github.com/lestrrat-go/option v1.0.0\ngo: downloading github.com/hashicorp/golang-lru v0.5.5-0.20210104140557-80c98217689d\ngo: downloading github.com/op/go-loggin\n[Command timed out after 120s]", "exit_code": 124, "error": null, "hint": "Exit 124: the command hit its timeout. Raise timeout= (foreground max 600s) or run it with background=true and notify_on_complete=true."}

