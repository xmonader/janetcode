# Five harnesses on qwen3.7-plus, three times each

- Model: `qwen3.7-plus` at `https://coding-intl.dashscope.aliyuncs.com/v1`
- Tasks: `evals/` 01–20, three complete repeats, identical endpoint and
  identical seeded task directories for every harness
- 300 task runs in total. openclaw's three runs were made later the same
  day, after the launcher learned to read its result object (below); the
  other four are as first recorded

## Totals over three runs

| harness | passed | fresh input | cache read | output | wall | run-to-run spread |
|---|---:|---:|---:|---:|---:|---:|
| **janetcode** | **60/60** | **288,648** | **711,913** | 23,837 | 1047s | **1%** |
| pi | 59/60 | 353,528 | 3,891,712 | 24,449 | 1046s | 114% |
| qwencode | 60/60 | 2,599,690 | 6,009,734 | 25,634 | 1410s | 0% |
| hermes-agent | 59/60 | 489,009 | 769,024 | 40,879 | 1622s | 18% |
| openclaw | 59/60 | 1,167,962 | 6,274,688 | 28,445 | 1755s | 6% |

Fresh input and cache read are kept apart because a cache read costs about
a tenth as much; a harness that merely takes more turns re-reads the same
cached prefix and looks expensive on a combined figure without spending
more money.

On fresh input — the part that costs — janetcode is lowest, pi is 1.2x,
hermes-agent 1.7x, openclaw 4.0x and qwencode 9x. On cache reads the
ordering changes completely: janetcode and hermes-agent sit near 700k
while pi reads 3.9M, qwencode 6.0M and openclaw 6.3M for the same twenty
tasks. openclaw's prompt is the reason for both of its figures: its
`--json` object reports a 31k-character system prompt and 67k characters
of tool schemas on every call, so a task that takes three model calls
re-reads that prefix three times and pays for it fresh once.

## Reproducibility is a result in itself

The spread column is the difference between the largest and smallest of
three identical runs:

| harness | fresh input per run |
|---|---|
| qwencode | 865,845 · 866,781 · 867,064 |
| janetcode | 95,809 · 96,415 · 96,424 |
| hermes-agent | 158,712 · 178,607 · 151,690 |
| openclaw | 380,809 · 405,022 · 382,131 |
| pi | 81,140 · 173,407 · 98,981 |

pi's middle run cost more than twice its cheapest. That matters more than
the totals for anyone using a suite like this to measure a change: at
janetcode's 1% a ten-percent improvement is visible, at pi's 114% nothing
below a doubling is.

It also corrects a number quoted elsewhere in these documents. The "21%
noise floor" is a property of the multi-prompt *session* tasks, not of this
benchmark.

## What each harness got wrong, including ours

**janetcode** passed everything here. In the earlier four-harness repeats it
failed `17-csv-sort` twice: asked to sort a CSV the model sometimes reaches
for `execute_code` and writes the file with Python's `csv` module, which
uses CRLF, against a check comparing to LF. It is a stochastic choice and
one the harnesses without a Python sandbox cannot make — an honest cost of
having one. No prompt text was added to steer it: instruction text is paid
for on every request forever, and two schema fields measured at 21% of
context earlier the same day.

**openclaw** failed `13-multi-file-package` once. The workspace it left
shows why: asked for a package in `stack/`, the model also wrote a
`stack/go.mod`, making it a nested module. Its own `go test` from inside
that directory passed, and it reported success; the check runs `go test
./stack/` from the module root, where a nested module is not a package.
The same task failed the same way once in the first recording of this
series. That is the model's mistake, and one the harness could only have
caught by running the test the way the user would.

An earlier recording of openclaw in this series showed 55/60 with three
failures on `06-read-and-answer` and no token columns. Both were this
benchmark's fault, not openclaw's: it was launched without `--json`, so its
stdout was prose and logs; and once it was, the whole result object — reply
plus a diagnostic block — was handed to the check as "the model's answer".
With the reply read from `finalAssistantVisibleText` the task passes 3/3.

**pi** and **hermes-agent** each failed one task once.

## Three of the four competitors were misconfigured before any of this

Not one of them was benchmarkable as found, and every failure would have
been published as that harness scoring badly:

- **hermes-agent** scored 0/20 because its terminal tool takes its working
  directory from `TERMINAL_CWD`, not the process's. It did every task
  correctly in the user's home directory while the check looked elsewhere.
- **openclaw** was hard-coded to fail without launching. It needed a newer
  Node than a bundled runtime was providing, an isolated profile to avoid
  the operator's running Gateway, its provider catalogue told about the
  endpoint, and — the one that took longest — a workspace and profile per
  task, because it keeps a single workspace setting per profile while this
  benchmark runs tasks concurrently, so tasks silently worked in each
  other's directories.
- **qwencode** needed its prompt-token totals corrected for cache reads
  before its input figure meant the same thing as everyone else's.
- **openclaw**, once it ran, needed `--json` before it reported anything,
  and then a reader of its own: its result object carries `promptTokens`
  (the size of the last prompt, cache included) beside the usage block, and
  the generic extractor added the two together, reporting 56,509 fresh
  input on a task where openclaw itself said 20,200. Usage now comes from
  `meta.agentMeta.usage` alone, whose `input` is already the uncached part
  (`total = input + output + cacheRead`).

## Reproduce

```
./bin/harnessbench --evals evals \
  --harnesses janetcode,pi,hermes-agent,qwencode,openclaw \
  --model qwen3.7-plus --base-url https://coding-intl.dashscope.aliyuncs.com/v1 --out run1
```
