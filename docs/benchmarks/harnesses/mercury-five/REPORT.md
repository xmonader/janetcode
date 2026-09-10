# Five harnesses on mercury-2.5, three times each

- Model: `inception/mercury-2.5` at `https://openrouter.ai/api/v1`
- Tasks: `evals/` 01–20, three complete repeats, identical endpoint and
  identical seeded task directories for every harness
- 300 task runs in total, recorded 2026-09-10 with the same launcher as
  the [qwen3.7-plus series](../qwen-five/REPORT.md)

## Totals over three runs

| harness | passed | fresh input | cache read | output | wall | run-to-run spread |
|---|---:|---:|---:|---:|---:|---:|
| **janetcode** | 57/60 | **466,940** | **566,228** | 30,294 | **189s** | 47% |
| pi | 58/60 | 1,125,961 | 3,180,907 | 32,447 | 322s | 294% |
| qwencode | 58/60 | 5,014,210 | 3,193,476 | 29,560 | 505s | 35% |
| hermes-agent | **59/60** | 806,263 | 209,552 | 40,962 | 453s | **12%** |
| openclaw | 56/60 | 2,836,216 | 4,572,463 | 53,311 | 869s | 129% |

Fresh input and cache read are kept apart because a cache read costs about
a tenth as much. Mercury is a diffusion model served through OpenRouter;
three of hermes-agent's and openclaw's sixty results carry no cache figure
at all, and janetcode and qwencode saw a handful of zero-cache responses.
Those are what the endpoint reported and are recorded as such.

On fresh input janetcode is lowest, hermes-agent 1.7x, pi 2.4x, openclaw
6.1x and qwencode 10.7x. janetcode is also the fastest by a wide margin:
its 60 tasks took 189 seconds of wall time against 322 for pi and 869 for
openclaw.

## This model is noisy, and the spread column says so

| harness | fresh input per run |
|---|---|
| janetcode | 193,396 · 142,007 · 131,537 |
| pi | 674,500 · 280,468 · 170,993 |
| qwencode | 1,994,151 · 1,546,336 · 1,473,723 |
| hermes-agent | 271,817 · 252,657 · 281,789 |
| openclaw | 1,514,900 · 660,615 · 660,701 |

On qwen3.7-plus the same harnesses reproduced to 1%, 6% and 18%; here the
first run is the expensive one for four of the five. The cause is visible
task by task: pi's first run spent 128k on `05-rename-symbol` alone and
openclaw's 192k, where their third runs spent 16k and under 50k. The model
takes a long route through the same task some of the time, and every
harness pays for it in proportion to its per-call prefix. hermes-agent, at
12%, is the only harness whose three runs look alike.

For anyone measuring a change with this model: a 30% difference between
two single runs is not evidence of anything.

## What each harness got wrong

Twelve failures in 300 runs, seven of them one task. `17-csv-sort` fell to
janetcode twice, qwencode twice, pi once and hermes-agent once. The check
compares the file byte for byte with the expected three rows, and the task
directories are not kept, so which byte differed — a line ending, a
trailing newline, quoting — is not recorded. Nine of fifteen attempts
passed, and openclaw passed all three, so it is a coin the model flips
rather than something one harness controls.

**janetcode** also lost `10-append-preserve` once: asked to append one
line to a three-line file, what it left was not those three lines plus
one. The file itself is not kept, so the exact difference is not recorded.

**pi** lost `19-write-test` once with a test that expected `Reverse("héllo")`
to give `"olleh"` — the model wrote a wrong expectation into the test it
was asked to write.

**openclaw** lost `06-read-and-answer` twice, answering `7` where the
function returns 15 — a wrong reading of the code, not a delivery
problem: the reply was read from its result object and was wrong. It lost
`16-readme` twice, replying "Done." while the README did not mention the
command as asked.

**hermes-agent** passed everything except the CSV sort once.

## Reproduce

```
./bin/harnessbench --evals evals \
  --harnesses janetcode,pi,hermes-agent,qwencode,openclaw \
  --model inception/mercury-2.5 --base-url https://openrouter.ai/api/v1 --out run1
```
