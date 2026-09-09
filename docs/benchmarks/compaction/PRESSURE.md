# What compaction does under pressure

Compaction is the largest subsystem in janetcode that nothing measures.
Across every benchmark run in this repository — fanout, large, session,
qwen and mercury alike — the count of compactions is **zero**. No eval task
grows a context big enough to trigger it, so pruning, the archive, the
working-memory summary and retrieval have all been shipped on unit tests
and hope.

Forcing it with `--eval-compact-at` says what happens when it does fire.

## It degrades badly

Nine fanout tasks x 2 runs on `inception/mercury-2.5`:

| threshold | passed | runs stuck repeating a call | compactions |
|---|---:|---:|---:|
| none (normal) | **18/18** | 0 | 0 |
| 20,000 | 18/18 | 0 | **0 — never fires** |
| 6,000 | 7/18 | **11** | 143 |

At six thousand tokens the pass rate falls from 100% to 39%, and eleven of
eighteen runs end with the loop detector stopping the turn because the
model is repeating itself.

## Why: the model forgets what it has already read

The stuck runs are all the same shape. One read `pkg/h01.go` five times,
`pkg/h02.go` three times, and listed `pkg` twice, in thirty-two calls.
Compaction removes the file's content and the summary does not mention that
the file was ever opened, so the model — handed a story in which it has
never seen the file — reads it again. And again.

A summariser drops what it judges unimportant, and *which files have
already been read* looks unimportant right up to the moment the model reads
them again.

## The fix, and how much it actually helped

The paths are now taken from the tool calls themselves rather than left to
the summary, and appended to the handoff note: `Already read: …` and
`Already changed by you: …`. A path extracted mechanically cannot be
forgotten, because nothing had to remember it.

| | passed | stuck | compactions |
|---|---:|---:|---:|
| before | 7/18 | 11 | 143 |
| after | **9/18** | **9** | 116 |

Better, and not fixed. Eighteen runs is a small sample and the difference is
well inside what that can resolve; what can be said is that the direction is
right and the mechanism is no longer guesswork.

## What is still not known

Whether any of this matters at a realistic threshold, because **no eval in
this repository reaches one**. Six thousand tokens is far below the 80,000
default and forces a compaction every turn or two, which is a stress test
rather than a simulation. Twenty-five thousand, on the multi-prompt session
tasks that come closest to real work, fired zero times.

So the honest position is: compaction under heavy pressure is bad and now
measurably less bad; compaction under normal pressure is still untested,
and the eval suite cannot test it. Fixing that needs a task whose context
genuinely grows — a large unfamiliar repository explored over many turns —
which is the same gap the tool-surface report ran into.

## Reproduce

```
janetcode --eval evals/fanout --eval-runs 2 --eval-compact-at 6000
```
