# Five harnesses on glm-5.3-flash, three times each

- Model: `z-ai/glm-5.3-flash` at `https://openrouter.ai/api/v1`
  ($0.075 per million input, $0.015 cache read, $0.25 output at list)
- Tasks: `evals/` 01–20, three complete repeats, identical endpoint and
  identical seeded task directories for every harness
- 300 task runs in total, recorded 2026-09-10 with the same launcher as
  the [qwen3.7-plus series](../qwen-five/REPORT.md). Two other models'
  series ran on the same endpoint at the same time, so wall time here is
  under shared load.

## Totals over three runs

| harness | passed | fresh input | cache read | output | wall | run-to-run spread |
|---|---:|---:|---:|---:|---:|---:|
| **janetcode** | **60/60** | 559,503 | 394,304 | 17,910 | 586s | 104% |
| pi | 57/60 | 1,085,616 | 2,596,096 | 12,674 | **513s** | 21% |
| qwencode | **60/60** | 1,659,512 | 6,865,408 | 29,424 | 1858s | 54% |
| hermes-agent | 52/60 | **505,545** | 226,944 | 11,021 | 989s | 68% |
| openclaw | 58/60 | 2,269,673 | 5,250,752 | 19,448 | 1468s | **11%** |

Fresh input and cache read are kept apart because a cache read costs about
a tenth as much. On this endpoint the credit is uneven: hermes-agent got no
cache figure on 37 of its 60 tasks, and janetcode's largest tasks in every
run were charged entirely fresh. At list price the 60 tasks cost janetcode
$0.052, hermes-agent $0.044, pi $0.124, openclaw $0.254 and qwencode $0.235.

janetcode and qwencode are the only harnesses that passed everything;
hermes-agent lost eight.

## The spread here is cache credit, not the model

| harness | fresh input per run |
|---|---|
| janetcode | 200,174 · 241,298 · 118,031 |
| pi | 401,980 · 350,420 · 333,216 |
| qwencode | 688,856 · 448,080 · 522,576 |
| hermes-agent | 218,425 · 157,001 · 130,119 |
| openclaw | 802,704 · 726,046 · 740,923 |

janetcode's 104% looks like the mercury kind of noise and is not. Its
cache reads per run were 121,344 · 76,480 · 196,480, so fresh plus cached — the prompt
the model actually processed — was 321,518 · 317,778 · 314,511: a 2% spread, in line with
its usual. What moved between runs is how much of that prompt the
provider credited as cached. qwencode's 54% is the same effect: its
prompt total per run was 2,797,848 · 2,885,008 · 2,842,064, within 3%.

For anyone measuring with this model: compare fresh plus cached, or
expect the fresh column alone to swing by a factor of two for reasons that
have nothing to do with the harness.

## What each harness got wrong

Thirteen failures in 300 runs.

**`13-multi-file-package`** — a Go package in `stack/` with a test —
failed five times: pi twice and openclaw once by writing a nested
`stack/go.mod`, so the check's `go test ./stack/` from the module root
finds no package; hermes-agent twice by producing no `stack/` directory
at all.

**hermes-agent**'s other six: `04-python-fizzbuzz` twice and
`20-env-default` once with no file where the check looked, `11-go-return-error`
with a test that did not compile, `12-python-bugfix` with the test still
failing, and `06-read-and-answer` answering `31` where the function
returns 15 (the other two runs answered 15). Its tool calls are not
counted by this launcher, so whether the missing files were never written
or written elsewhere is not on record.

**pi** also lost `08-shell-script` once, with a script that read a file
that was not there. **openclaw** lost `02-go-add-function` once, writing
a test for `Mul` without the function.

**janetcode** and **qwencode** passed 60/60. Every harness answered the
read-and-answer task correctly at least twice; on laguna the same task
failed 8 of 15.

## Reproduce

```
./bin/harnessbench --evals evals \
  --harnesses janetcode,pi,hermes-agent,qwencode,openclaw \
  --model z-ai/glm-5.3-flash --base-url https://openrouter.ai/api/v1 --out run1
```
