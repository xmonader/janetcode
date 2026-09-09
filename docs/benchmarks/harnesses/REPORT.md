# Harness benchmark report

- Model: `qwen3.7-plus`
- Endpoint: `https://coding-intl.dashscope.aliyuncs.com/v1`
- Tasks: 01–20 in four batches of five (01–05, 06–10, 11–15, 16–20)
- When: 2026-09-08T21:52:42Z
- Note: pass/fail is the eval JSON shell check; token fields are only those the harness emitted

Token columns are **input tokens**, **cache read tokens**, **cache write tokens**, and **output tokens**. Wall time is the measured task duration. A field the harness did not report is `n/a`, not an estimate.

## Per-harness totals

| harness | passed | unavailable | wall time | input tokens | cache read tokens | cache write tokens | output tokens | tool calls | turns | cost USD |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| pi | 19/20 | 0 | 5m32.97s | 74344 | 1263872 | 0 | 7668 | n/a | 74 | 0.000000 |
| janetcode | 20/20 | 0 | 5m16.51s | 47566 | 109758 | 0 | 7278 | 68 | 76 | n/a |
| qwencode | 20/20 | 0 | 7m29.28s | 2753291 | 1888210 | n/a | 7608 | 56 | 65 | n/a |
| hermes-agent | 2/20 | 0 | 27m23.1s | 1387866 | 1028736 | n/a | 23207 | n/a | 136 | n/a |
| openclaw | 0/0 | 20 | 0s | n/a | n/a | n/a | n/a | n/a | n/a | n/a |
| grok | 18/20 | 0 | 9m32.13s | 419827 | 1608320 | 0 | 9234 | n/a | 85 | n/a |

## Per-task wall time

| task | pi | janetcode | qwencode | hermes-agent | openclaw | grok |
|---|---:|---:|---:|---:|---:|---:|
| 01-create-file | 7.92s | 8.37s | 13.17s | 35.2s | n/a | 10.12s |
| 02-go-add-function | 19.95s | 17.6s | 30.96s | 2m56.29s | n/a | 43.41s |
| 03-fix-failing-test | 26.99s | 15.63s | 27.67s | 3m0.01s | n/a | 1m17.58s |
| 04-python-fizzbuzz | 14.07s | 13.79s | 24.88s | 29.16s | n/a | 19.07s |
| 05-rename-symbol | 31.98s | 18.11s | 32.49s | 1m52.49s | n/a | 43.43s |
| 06-read-and-answer | 11.62s | 11.72s | 15.67s | 2m10s | n/a | 29.64s |
| 07-json-config-edit | 12s | 17.21s | 19.73s | 37.78s | n/a | 37.28s |
| 08-shell-script | 11.66s | 20.26s | 28.19s | 17.38s | n/a | 20.87s |
| 09-makefile-target | 23.19s | 16.05s | 24.97s | 40.61s | n/a | 46.51s |
| 10-append-preserve | 15.55s | 23.55s | 16.37s | 26.09s | n/a | 27.69s |
| 11-go-return-error | 23.75s | 19.45s | 24.48s | 3m0.05s | n/a | 50.17s |
| 12-python-bugfix | 16.53s | 14.39s | 25.03s | 2m8.6s | n/a | 25.2s |
| 13-multi-file-package | 19.31s | 27.04s | 26.86s | 40.8s | n/a | 26.51s |
| 14-gitignore | 11.46s | 11.71s | 20.82s | 20.94s | n/a | 15.61s |
| 15-yes-no-question | 7.6s | 5.29s | 15.82s | 16.87s | n/a | 8.86s |
| 16-readme | 18.36s | 17.01s | 16.89s | 38.12s | n/a | 20.94s |
| 17-csv-sort | 15.46s | 15.42s | 21.29s | 1m27.02s | n/a | 17.29s |
| 18-find-mentions | 8.56s | 5.75s | 11.31s | 3m0.03s | n/a | 7.62s |
| 19-write-test | 23.51s | 26.97s | 31.57s | 2m19.97s | n/a | 29.33s |
| 20-env-default | 13.41s | 11.11s | 21.02s | 25.59s | n/a | 14.89s |

## Per-task input tokens

| task | pi | janetcode | qwencode | hermes-agent | openclaw | grok |
|---|---:|---:|---:|---:|---:|---:|
| 01-create-file | 1515 | 2048 | 84537 | 22109 | n/a | 23645 |
| 02-go-add-function | 5003 | 2517 | 169287 | 152483 | n/a | 28111 |
| 03-fix-failing-test | 6187 | 2499 | 171308 | 407458 | n/a | 28130 |
| 04-python-fizzbuzz | 2657 | 2370 | 126888 | 15389 | n/a | 24530 |
| 05-rename-symbol | 7004 | 2477 | 170930 | 73065 | n/a | 25712 |
| 06-read-and-answer | 2435 | 2060 | 84460 | 31793 | n/a | 17775 |
| 07-json-config-edit | 2437 | 2370 | 126715 | 45401 | n/a | 20503 |
| 08-shell-script | 2419 | 2346 | 126753 | 10749 | n/a | 17943 |
| 09-makefile-target | 4583 | 2254 | 168674 | 95315 | n/a | 20685 |
| 10-append-preserve | 3491 | 2479 | 126472 | 14402 | n/a | 18067 |
| 11-go-return-error | 7798 | 2663 | 169344 | 233269 | n/a | 27416 |
| 12-python-bugfix | 4009 | 2394 | 170299 | 60503 | n/a | 20154 |
| 13-multi-file-package | 3427 | 2700 | 169795 | 23842 | n/a | 20204 |
| 14-gitignore | 2336 | 2221 | 126647 | 14437 | n/a | 17647 |
| 15-yes-no-question | 1533 | 2041 | 84518 | 10791 | n/a | 17443 |
| 16-readme | 3797 | 2323 | 126800 | 40186 | n/a | 17748 |
| 17-csv-sort | 3517 | 2246 | 168777 | 31065 | n/a | 18700 |
| 18-find-mentions | 1501 | 1952 | 84632 | 7068 | n/a | 17458 |
| 19-write-test | 6116 | 3411 | 169648 | 83774 | n/a | 19755 |
| 20-env-default | 2579 | 2195 | 126807 | 14767 | n/a | 18201 |

## Per-task cache read tokens

| task | pi | janetcode | qwencode | hermes-agent | openclaw | grok |
|---|---:|---:|---:|---:|---:|---:|
| 01-create-file | 34048 | 3656 | 41727 | 10240 | n/a | 23296 |
| 02-go-add-function | 85120 | 5930 | 125935 | 124416 | n/a | 139776 |
| 03-fix-failing-test | 85120 | 6180 | 127222 | 356480 | n/a | 116480 |
| 04-python-fizzbuzz | 51072 | 3695 | 83697 | 8192 | n/a | 46592 |
| 05-rename-symbol | 106240 | 6178 | 126928 | 50816 | n/a | 69888 |
| 06-read-and-answer | 51072 | 3659 | 41673 | 6272 | n/a | 52864 |
| 07-json-config-edit | 51072 | 5602 | 83636 | 23168 | n/a | 122752 |
| 08-shell-script | 51072 | 7541 | 83653 | 4096 | n/a | 52864 |
| 09-makefile-target | 85120 | 5566 | 125515 | 65792 | n/a | 146048 |
| 10-append-preserve | 68096 | 7582 | 83511 | 8192 | n/a | 76160 |
| 11-go-return-error | 85120 | 6066 | 125945 | 194816 | n/a | 192640 |
| 12-python-bugfix | 68096 | 6035 | 126564 | 44288 | n/a | 99456 |
| 13-multi-file-package | 51072 | 8007 | 126148 | 12416 | n/a | 76160 |
| 14-gitignore | 51072 | 5548 | 83622 | 6144 | n/a | 52864 |
| 15-yes-no-question | 34048 | 1821 | 41702 | 4096 | n/a | 29568 |
| 16-readme | 68096 | 5838 | 83684 | 25344 | n/a | 52864 |
| 17-csv-sort | 68096 | 5586 | 125589 | 18816 | n/a | 76160 |
| 18-find-mentions | 34048 | 1820 | 41731 | 2048 | n/a | 29568 |
| 19-write-test | 85120 | 9781 | 126049 | 61056 | n/a | 99456 |
| 20-env-default | 51072 | 3667 | 83679 | 2048 | n/a | 52864 |

## Per-task cache write tokens

| task | pi | janetcode | qwencode | hermes-agent | openclaw | grok |
|---|---:|---:|---:|---:|---:|---:|
| 01-create-file | 0 | 0 | n/a | n/a | n/a | 0 |
| 02-go-add-function | 0 | 0 | n/a | n/a | n/a | 0 |
| 03-fix-failing-test | 0 | 0 | n/a | n/a | n/a | 0 |
| 04-python-fizzbuzz | 0 | 0 | n/a | n/a | n/a | 0 |
| 05-rename-symbol | 0 | 0 | n/a | n/a | n/a | 0 |
| 06-read-and-answer | 0 | 0 | n/a | n/a | n/a | 0 |
| 07-json-config-edit | 0 | 0 | n/a | n/a | n/a | 0 |
| 08-shell-script | 0 | 0 | n/a | n/a | n/a | 0 |
| 09-makefile-target | 0 | 0 | n/a | n/a | n/a | 0 |
| 10-append-preserve | 0 | 0 | n/a | n/a | n/a | 0 |
| 11-go-return-error | 0 | 0 | n/a | n/a | n/a | 0 |
| 12-python-bugfix | 0 | 0 | n/a | n/a | n/a | 0 |
| 13-multi-file-package | 0 | 0 | n/a | n/a | n/a | 0 |
| 14-gitignore | 0 | 0 | n/a | n/a | n/a | 0 |
| 15-yes-no-question | 0 | 0 | n/a | n/a | n/a | 0 |
| 16-readme | 0 | 0 | n/a | n/a | n/a | 0 |
| 17-csv-sort | 0 | 0 | n/a | n/a | n/a | 0 |
| 18-find-mentions | 0 | 0 | n/a | n/a | n/a | 0 |
| 19-write-test | 0 | 0 | n/a | n/a | n/a | 0 |
| 20-env-default | 0 | 0 | n/a | n/a | n/a | 0 |

## Per-task output tokens

| task | pi | janetcode | qwencode | hermes-agent | openclaw | grok |
|---|---:|---:|---:|---:|---:|---:|
| 01-create-file | 110 | 150 | 124 | 775 | n/a | 89 |
| 02-go-add-function | 459 | 314 | 364 | 2859 | n/a | 800 |
| 03-fix-failing-test | 634 | 397 | 566 | 2781 | n/a | 824 |
| 04-python-fizzbuzz | 321 | 276 | 385 | 636 | n/a | 343 |
| 05-rename-symbol | 944 | 522 | 694 | 1328 | n/a | 751 |
| 06-read-and-answer | 238 | 157 | 216 | 707 | n/a | 211 |
| 07-json-config-edit | 220 | 307 | 273 | 671 | n/a | 475 |
| 08-shell-script | 175 | 392 | 301 | 249 | n/a | 261 |
| 09-makefile-target | 414 | 231 | 456 | 726 | n/a | 516 |
| 10-append-preserve | 318 | 406 | 270 | 558 | n/a | 262 |
| 11-go-return-error | 597 | 536 | 408 | 2428 | n/a | 1196 |
| 12-python-bugfix | 425 | 353 | 584 | 1933 | n/a | 496 |
| 13-multi-file-package | 645 | 859 | 735 | 1339 | n/a | 867 |
| 14-gitignore | 176 | 262 | 296 | 360 | n/a | 197 |
| 15-yes-no-question | 112 | 96 | 118 | 191 | n/a | 97 |
| 16-readme | 455 | 456 | 307 | 868 | n/a | 387 |
| 17-csv-sort | 325 | 436 | 354 | 1079 | n/a | 322 |
| 18-find-mentions | 124 | 103 | 147 | 153 | n/a | 105 |
| 19-write-test | 667 | 730 | 665 | 3086 | n/a | 679 |
| 20-env-default | 309 | 295 | 345 | 480 | n/a | 356 |

## Per-task pass/fail

| task | pi | janetcode | qwencode | hermes-agent | openclaw | grok |
|---|---:|---:|---:|---:|---:|---:|
| 01-create-file | PASS | PASS | PASS | FAIL | unavailable | PASS |
| 02-go-add-function | PASS | PASS | PASS | FAIL | unavailable | PASS |
| 03-fix-failing-test | PASS | PASS | PASS | FAIL | unavailable | PASS |
| 04-python-fizzbuzz | PASS | PASS | PASS | FAIL | unavailable | PASS |
| 05-rename-symbol | PASS | PASS | PASS | FAIL | unavailable | PASS |
| 06-read-and-answer | PASS | PASS | PASS | FAIL | unavailable | PASS |
| 07-json-config-edit | PASS | PASS | PASS | FAIL | unavailable | PASS |
| 08-shell-script | PASS | PASS | PASS | FAIL | unavailable | PASS |
| 09-makefile-target | PASS | PASS | PASS | FAIL | unavailable | PASS |
| 10-append-preserve | PASS | PASS | PASS | FAIL | unavailable | PASS |
| 11-go-return-error | PASS | PASS | PASS | FAIL | unavailable | PASS |
| 12-python-bugfix | PASS | PASS | PASS | PASS | unavailable | PASS |
| 13-multi-file-package | FAIL | PASS | PASS | FAIL | unavailable | PASS |
| 14-gitignore | PASS | PASS | PASS | FAIL | unavailable | PASS |
| 15-yes-no-question | PASS | PASS | PASS | FAIL | unavailable | PASS |
| 16-readme | PASS | PASS | PASS | FAIL | unavailable | FAIL |
| 17-csv-sort | PASS | PASS | PASS | PASS | unavailable | FAIL |
| 18-find-mentions | PASS | PASS | PASS | FAIL | unavailable | PASS |
| 19-write-test | PASS | PASS | PASS | FAIL | unavailable | PASS |
| 20-env-default | PASS | PASS | PASS | FAIL | unavailable | PASS |

## Failures and unavailable harnesses

- **pi / 13-multi-file-package** (batch 3): check failed: exit status 1: # ./stack
  - command: `pi -p --mode json --provider dashscope --model qwen3.7-plus --no-session Create a Go package 'stack' in stack/ with a Stack type (Push, Pop returning (int, bool)) and a test that pushes 1,2 and pops 2 then 1. Run the tests.`
  - stderr: # ./stack
- **hermes-agent / 01-create-file** (batch 1): check failed: exit status 1: cat: hello.txt: No such file or directory
  - command: `hermes-agent --query Create a file named hello.txt containing exactly the text: hello world --model qwen3.7-plus --base_url https://coding-intl.dashscope.aliyuncs.com/v1 --api_key *** --max_turns 20 --enabled_toolsets terminal --verbose`
  - stderr: agent.conversation_loop - INFO [20260908_232937_e91dd0] - API call #6: model=qwen3.7-plus provider=unknown in=3893 out=107 total=4000 latency=3.7s cache=2048/3893 (53%)
- **hermes-agent / 02-go-add-function** (batch 1): check failed: exit status 1: ./math_test.go:6:5: undefined: Mul
  - command: `hermes-agent --query math.go has Add. Add a Mul(a, b int) int function in the same package so that math_test.go passes. Run the tests. --model qwen3.7-plus --base_url https://coding-intl.dashscope.aliyuncs.com/v1 --api_key *** --max_turns 20 --enabled_toolsets terminal --verbose`
  - stderr: 32:31 - root - DEBUG [20260908_232937_7f51d5] - Captured reasoning (493 chars): The user mentioned "math_test.go passes" but I couldn't find an existing math_test.go file. I created both math.go (with Add and Mul) and math_test.go (with tests for both functions). The tests pass successfully.
- **hermes-agent / 03-fix-failing-test** (batch 1): timed out after 3m0s: signal: killed
  - command: `hermes-agent --query go test fails. Find the bug and fix it in the implementation, not the test. --model qwen3.7-plus --base_url https://coding-intl.dashscope.aliyuncs.com/v1 --api_key *** --max_turns 20 --enabled_toolsets terminal --verbose`
  - stderr: 31604, total_tokens=31832, completion_tokens_details=CompletionTokensDetails(accepted_prediction_tokens=None, audio_tokens=None, reasoning_tokens=151, rejected_prediction_tokens=None, text_tokens=228), prompt_tokens_details=PromptTokensDetails(audio_tokens=None, cached_tokens=29696, text_tokens=31604))
- **hermes-agent / 04-python-fizzbuzz** (batch 1): check failed: exit status 1: python3: can't open file '/tmp/harness-hermes-agent-04-python-fizzbuzz-2350957391/fizzbuzz.py': [Errno 2] No such file or directory
  - command: `hermes-agent --query Write fizzbuzz.py: given a number N as its only argument it prints FizzBuzz for 1..N, one per line (Fizz for multiples of 3, Buzz for 5, FizzBuzz for both, the number otherwise). --model qwen3.7-plus --base_url https://coding-intl.dashscope.aliyuncs.com/v1 --api_key *** --max_turns 20 --enabled_toolsets terminal --verbose`
  - stderr: s=None, text_tokens=79), prompt_tokens_details=PromptTokensDetails(audio_tokens=None, cached_tokens=2048, text_tokens=4100))
- **hermes-agent / 05-rename-symbol** (batch 1): check failed: exit status 1
  - command: `hermes-agent --query Rename the function Fetch to Load everywhere in this package, including its callers. The code must still build. --model qwen3.7-plus --base_url https://coding-intl.dashscope.aliyuncs.com/v1 --api_key *** --max_turns 20 --enabled_toolsets terminal --verbose`
  - stderr: s with `func Fetch`. I need to figure out which one they mean. Let me look at the context more carefully.
- **hermes-agent / 06-read-and-answer** (batch 2): check failed: exit status 1
  - command: `hermes-agent --query What does Compute(3) return? Reply with just the number. --model qwen3.7-plus --base_url https://coding-intl.dashscope.aliyuncs.com/v1 --api_key *** --max_turns 20 --enabled_toolsets terminal --verbose`
  - stderr: Token usage: prompt=23,853, completion=138, total=23,991
- **hermes-agent / 07-json-config-edit** (batch 2): check failed: exit status 1: Traceback (most recent call last):
  - command: `hermes-agent --query In config.json change the port to 8080. Keep every other key exactly as it is. --model qwen3.7-plus --base_url https://coding-intl.dashscope.aliyuncs.com/v1 --api_key *** --max_turns 20 --enabled_toolsets terminal --verbose`
  - stderr: ning (558 chars): The user is asking me to change the port to 8080 in a `config.json` file. However:
- **hermes-agent / 08-shell-script** (batch 2): check failed: exit status 1: bash: count.sh: No such file or directory
  - command: `hermes-agent --query Write count.sh that prints the number of lines in input.txt and nothing else. --model qwen3.7-plus --base_url https://coding-intl.dashscope.aliyuncs.com/v1 --api_key *** --max_turns 20 --enabled_toolsets terminal --verbose`
  - stderr: omptTokensDetails(audio_tokens=None, cached_tokens=2048, text_tokens=3662))
- **hermes-agent / 09-makefile-target** (batch 2): check failed: exit status 1: make: *** No rule to make target 'greet'.  Stop.
  - command: `hermes-agent --query Add a greet target to the Makefile that prints hi. Keep the existing targets. --model qwen3.7-plus --base_url https://coding-intl.dashscope.aliyuncs.com/v1 --api_key *** --max_turns 20 --enabled_toolsets terminal --verbose`
  - stderr: ls(audio_tokens=None, cached_tokens=21248, text_tokens=22445))
- **hermes-agent / 10-append-preserve** (batch 2): check failed: exit status 1
  - command: `hermes-agent --query Append the line 'four' to the end of list.txt. Do not change the existing lines. --model qwen3.7-plus --base_url https://coding-intl.dashscope.aliyuncs.com/v1 --api_key *** --max_turns 20 --enabled_toolsets terminal --verbose`
  - stderr: r' >> list.txt` which should append a line containing "four" to the file. The result shows the file now contains two lines of "four".
- **hermes-agent / 11-go-return-error** (batch 3): timed out after 3m0s: signal: killed
  - command: `hermes-agent --query Parse ignores the error from strconv.Atoi. Change Parse to return (int, error) and propagate it; update the test's expectations only if needed to compile. Run go vet and the tests. --model qwen3.7-plus --base_url https://coding-intl.dashscope.aliyuncs.com/v1 --api_key *** --max_turns 20 --enabled_toolsets terminal --verbose`
  - stderr: detector
- **hermes-agent / 13-multi-file-package** (batch 3): check failed: exit status 1: # ./stack
  - command: `hermes-agent --query Create a Go package 'stack' in stack/ with a Stack type (Push, Pop returning (int, bool)) and a test that pushes 1,2 and pops 2 then 1. Run the tests. --model qwen3.7-plus --base_url https://coding-intl.dashscope.aliyuncs.com/v1 --api_key *** --max_turns 20 --enabled_toolsets terminal --verbose`
  - stderr: int, bool)) and a test that pushes 1,2 and pops 2 then 1. I've completed this task successfully.
- **hermes-agent / 14-gitignore** (batch 3): check failed: exit status 1
  - command: `hermes-agent --query Add node_modules/ and dist/ to .gitignore, keeping what is there. --model qwen3.7-plus --base_url https://coding-intl.dashscope.aliyuncs.com/v1 --api_key *** --max_turns 20 --enabled_toolsets terminal --verbose`
  - stderr: en3.7-plus provider=unknown in=3737 out=89 total=3826 latency=3.9s cache=2048/3737 (55%)
- **hermes-agent / 15-yes-no-question** (batch 3): check failed: exit status 1
  - command: `hermes-agent --query Does any Go file in this directory call fmt.Println? Answer yes or no, then name the file if yes. --model qwen3.7-plus --base_url https://coding-intl.dashscope.aliyuncs.com/v1 --api_key *** --max_turns 20 --enabled_toolsets terminal --verbose`
  - stderr: t_tokens_details=PromptTokensDetails(audio_tokens=None, cached_tokens=0, text_tokens=3680))
- **hermes-agent / 16-readme** (batch 4): check failed: exit status 2: grep: README.md: No such file or directory
  - command: `hermes-agent --query Write a README.md for this tool: a title heading, one paragraph on what it does, and a Usage section showing the command. --model qwen3.7-plus --base_url https://coding-intl.dashscope.aliyuncs.com/v1 --api_key *** --max_turns 20 --enabled_toolsets terminal --verbose`
  - stderr: ements:
- **hermes-agent / 18-find-mentions** (batch 4): timed out after 3m0s: signal: killed
  - command: `hermes-agent --query Which files mention 'flux capacitor'? List only the file names. --model qwen3.7-plus --base_url https://coding-intl.dashscope.aliyuncs.com/v1 --api_key *** --max_turns 20 --enabled_toolsets terminal --verbose`
  - stderr: loud reasoning stream — read timeout raised to 180s to match stale-stream detector
- **hermes-agent / 19-write-test** (batch 4): check failed: exit status 2: ?   	example.com/m	[no test files]
  - command: `hermes-agent --query Add a test file for Reverse covering an empty string, a single rune, and a multi-byte string like 'héllo'. Run it. --model qwen3.7-plus --base_url https://coding-intl.dashscope.aliyuncs.com/v1 --api_key *** --max_turns 20 --enabled_toolsets terminal --verbose`
  - stderr: is implies there's an existing Reverse function somewhere. Let me look more carefully. Maybe it's in the go-book chapters? Or maybe the user expects me to create both the function and test?
- **hermes-agent / 20-env-default** (batch 4): check failed: exit status 1: no Go files in /tmp/harness-hermes-agent-20-env-default-377712896
  - command: `hermes-agent --query Write main.go: a program that prints the PORT environment variable, or 3000 when it is unset. --model qwen3.7-plus --base_url https://coding-intl.dashscope.aliyuncs.com/v1 --api_key *** --max_turns 20 --enabled_toolsets terminal --verbose`
  - stderr: agent.conversation_loop - INFO [20260908_233747_2d2fb8] - API call #4: model=qwen3.7-plus provider=unknown in=3859 out=123 total=3982 latency=5.7s
- **openclaw / 01-create-file** (batch 1): openclaw: Node.js >=22.22.3 <23, >=24.15.0 <25, or >=25.9.0 is required (current: v22.22.2).
  - command: `openclaw --help`
  - stderr: openclaw: Node.js >=22.22.3 <23, >=24.15.0 <25, or >=25.9.0 is required (current: v22.22.2).
- **openclaw / 02-go-add-function** (batch 1): openclaw: Node.js >=22.22.3 <23, >=24.15.0 <25, or >=25.9.0 is required (current: v22.22.2).
  - command: `openclaw --help`
  - stderr: openclaw: Node.js >=22.22.3 <23, >=24.15.0 <25, or >=25.9.0 is required (current: v22.22.2).
- **openclaw / 03-fix-failing-test** (batch 1): openclaw: Node.js >=22.22.3 <23, >=24.15.0 <25, or >=25.9.0 is required (current: v22.22.2).
  - command: `openclaw --help`
  - stderr: openclaw: Node.js >=22.22.3 <23, >=24.15.0 <25, or >=25.9.0 is required (current: v22.22.2).
- **openclaw / 04-python-fizzbuzz** (batch 1): openclaw: Node.js >=22.22.3 <23, >=24.15.0 <25, or >=25.9.0 is required (current: v22.22.2).
  - command: `openclaw --help`
  - stderr: openclaw: Node.js >=22.22.3 <23, >=24.15.0 <25, or >=25.9.0 is required (current: v22.22.2).
- **openclaw / 05-rename-symbol** (batch 1): openclaw: Node.js >=22.22.3 <23, >=24.15.0 <25, or >=25.9.0 is required (current: v22.22.2).
  - command: `openclaw --help`
  - stderr: openclaw: Node.js >=22.22.3 <23, >=24.15.0 <25, or >=25.9.0 is required (current: v22.22.2).
- **openclaw / 06-read-and-answer** (batch 2): openclaw: Node.js >=22.22.3 <23, >=24.15.0 <25, or >=25.9.0 is required (current: v22.22.2).
  - command: `openclaw --help`
  - stderr: openclaw: Node.js >=22.22.3 <23, >=24.15.0 <25, or >=25.9.0 is required (current: v22.22.2).
- **openclaw / 07-json-config-edit** (batch 2): openclaw: Node.js >=22.22.3 <23, >=24.15.0 <25, or >=25.9.0 is required (current: v22.22.2).
  - command: `openclaw --help`
  - stderr: openclaw: Node.js >=22.22.3 <23, >=24.15.0 <25, or >=25.9.0 is required (current: v22.22.2).
- **openclaw / 08-shell-script** (batch 2): openclaw: Node.js >=22.22.3 <23, >=24.15.0 <25, or >=25.9.0 is required (current: v22.22.2).
  - command: `openclaw --help`
  - stderr: openclaw: Node.js >=22.22.3 <23, >=24.15.0 <25, or >=25.9.0 is required (current: v22.22.2).
- **openclaw / 09-makefile-target** (batch 2): openclaw: Node.js >=22.22.3 <23, >=24.15.0 <25, or >=25.9.0 is required (current: v22.22.2).
  - command: `openclaw --help`
  - stderr: openclaw: Node.js >=22.22.3 <23, >=24.15.0 <25, or >=25.9.0 is required (current: v22.22.2).
- **openclaw / 10-append-preserve** (batch 2): openclaw: Node.js >=22.22.3 <23, >=24.15.0 <25, or >=25.9.0 is required (current: v22.22.2).
  - command: `openclaw --help`
  - stderr: openclaw: Node.js >=22.22.3 <23, >=24.15.0 <25, or >=25.9.0 is required (current: v22.22.2).
- **openclaw / 11-go-return-error** (batch 3): openclaw: Node.js >=22.22.3 <23, >=24.15.0 <25, or >=25.9.0 is required (current: v22.22.2).
  - command: `openclaw --help`
  - stderr: openclaw: Node.js >=22.22.3 <23, >=24.15.0 <25, or >=25.9.0 is required (current: v22.22.2).
- **openclaw / 12-python-bugfix** (batch 3): openclaw: Node.js >=22.22.3 <23, >=24.15.0 <25, or >=25.9.0 is required (current: v22.22.2).
  - command: `openclaw --help`
  - stderr: openclaw: Node.js >=22.22.3 <23, >=24.15.0 <25, or >=25.9.0 is required (current: v22.22.2).
- **openclaw / 13-multi-file-package** (batch 3): openclaw: Node.js >=22.22.3 <23, >=24.15.0 <25, or >=25.9.0 is required (current: v22.22.2).
  - command: `openclaw --help`
  - stderr: openclaw: Node.js >=22.22.3 <23, >=24.15.0 <25, or >=25.9.0 is required (current: v22.22.2).
- **openclaw / 14-gitignore** (batch 3): openclaw: Node.js >=22.22.3 <23, >=24.15.0 <25, or >=25.9.0 is required (current: v22.22.2).
  - command: `openclaw --help`
  - stderr: openclaw: Node.js >=22.22.3 <23, >=24.15.0 <25, or >=25.9.0 is required (current: v22.22.2).
- **openclaw / 15-yes-no-question** (batch 3): openclaw: Node.js >=22.22.3 <23, >=24.15.0 <25, or >=25.9.0 is required (current: v22.22.2).
  - command: `openclaw --help`
  - stderr: openclaw: Node.js >=22.22.3 <23, >=24.15.0 <25, or >=25.9.0 is required (current: v22.22.2).
- **openclaw / 16-readme** (batch 4): openclaw: Node.js >=22.22.3 <23, >=24.15.0 <25, or >=25.9.0 is required (current: v22.22.2).
  - command: `openclaw --help`
  - stderr: openclaw: Node.js >=22.22.3 <23, >=24.15.0 <25, or >=25.9.0 is required (current: v22.22.2).
- **openclaw / 17-csv-sort** (batch 4): openclaw: Node.js >=22.22.3 <23, >=24.15.0 <25, or >=25.9.0 is required (current: v22.22.2).
  - command: `openclaw --help`
  - stderr: openclaw: Node.js >=22.22.3 <23, >=24.15.0 <25, or >=25.9.0 is required (current: v22.22.2).
- **openclaw / 18-find-mentions** (batch 4): openclaw: Node.js >=22.22.3 <23, >=24.15.0 <25, or >=25.9.0 is required (current: v22.22.2).
  - command: `openclaw --help`
  - stderr: openclaw: Node.js >=22.22.3 <23, >=24.15.0 <25, or >=25.9.0 is required (current: v22.22.2).
- **openclaw / 19-write-test** (batch 4): openclaw: Node.js >=22.22.3 <23, >=24.15.0 <25, or >=25.9.0 is required (current: v22.22.2).
  - command: `openclaw --help`
  - stderr: openclaw: Node.js >=22.22.3 <23, >=24.15.0 <25, or >=25.9.0 is required (current: v22.22.2).
- **openclaw / 20-env-default** (batch 4): openclaw: Node.js >=22.22.3 <23, >=24.15.0 <25, or >=25.9.0 is required (current: v22.22.2).
  - command: `openclaw --help`
  - stderr: openclaw: Node.js >=22.22.3 <23, >=24.15.0 <25, or >=25.9.0 is required (current: v22.22.2).
- **grok / 16-readme** (batch 4): check failed: exit status 2: grep: README.md: No such file or directory
  - command: `grok -p Write a README.md for this tool: a title heading, one paragraph on what it does, and a Usage section showing the command. --cwd /tmp/harness-grok-16-readme-204485279 --always-approve --output-format json --model qwen3.7-plus --max-turns 20 --disable-web-search`
  - stderr: grep: README.md: No such file or directory
- **grok / 17-csv-sort** (batch 4): check failed: exit status 1
  - command: `grok -p Write sorted.csv: the rows of data.csv sorted by score descending, same header. Use a script or a command, your choice. --cwd /tmp/harness-grok-17-csv-sort-810796564 --always-approve --output-format json --model qwen3.7-plus --max-turns 20 --disable-web-search`

