# Fact survival after compaction

- Model: `qwen3.7-plus` · task `43-session-python-ledger` · compaction forced at 8k
- When: 2026-09-09T10:29:25Z

Pass rate says whether a task got done; it does not say what the agent
still knows once its history has been summarised away. These runs probe
that: facts are taken from the tool output compaction removed, the model
is asked for each against its compacted history with `expand_output` as
its only tool, and the answer is scored by exact substring.

A miss is split three ways, because each needs a different fix: the value
was never kept, the search did not find it, or the model had it in front
of it and did not use it.

| run | recalled | still in the archive | retrieved by the search | grounded | searched |
|---|---:|---:|---:|---:|---:|
| before the line lookup | 1/8 | 8/8 | 0/8 | 0 | 7 |
| after it | 4/8 | 8/8 | 4/8 | 4 | 8 |

## What the split says

Nothing is being lost: every probed value was still on disk in both runs.
The first run retrieved **none** of them, which is the whole of its 1/8.
Every question was of the form "quote line 36 of ledger/parse.py", and a
search that ranks by word overlap cannot serve it — the query shares no
vocabulary with the code on that line. Looking a named file and line up
directly instead took retrieval from 0 to 4 and recall with it.

In the second run `retrieved`, `correct` and `grounded` are all 4: every
value the search returned became a right answer, and every right answer
appears verbatim in what the model fetched. Nothing was recalled from the
model's own memory — which is the point, and matches the blind control
below.

Retrieval remains the only bottleneck: all four remaining misses had the
value in the archive and never got it. One of them is the same question
that succeeded elsewhere in the run, so the model's phrasing of the query,
not the archive or the ranking, decides the outcome.

## Controls

- **Blind** (no `expand_output`, the compacted context alone): **0/16**.
  The summary keeps that a file was read, not what was in it, and with no
  tool the model writes plausible answers it cannot support.
- These runs still probe mostly positional questions. The generator was
  since rebuilt to ask content questions too (the shape ctxresearch
  mines), so the ranked search is measured rather than assumed.

## Re-run

```sh
export OPENAI_API_KEY=...
export OPENAI_BASE_URL=https://coding-intl.dashscope.aliyuncs.com/v1
make build
./bin/janetcode --eval evals/session --eval-only 43-session-python-ledger \
  --eval-compact-at 8000 --eval-facts 8 --provider openai --model qwen3.7-plus \
  --eval-json docs/benchmarks/compaction/facts.json --eval-label facts
```
