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

## With both probe shapes (the ranked search finally measured)

Run 2026-09-09, all three session tasks, compaction forced at 8k, 20 probes.

| probe shape | asked | in the archive | retrieved | recalled |
|---|---:|---:|---:|---:|
| command output | 1 | 1 | 1 | 1 |
| content ("the line mentioning Y") | 10 | 10 | 6 | 5 |
| positional ("line N of X") | 9 | 9 | 3 | 3 |
| **all** | **20** | **20** | **10** | **9** |

Recall is 9/20 (45%), against 0/16 blind and 3/16 when the
archive was labelled only by tool name. All 20 values were still on disk, and
all 9 correct answers appear verbatim in what the model fetched — none was
produced from memory.

The split answers the question the earlier runs could not. **The ranked
search is the stronger half**, not the weaker one: it retrieved 6 of 10
content questions against 3 of 9 positional ones. That inverts the
assumption behind the line lookup — which was added because positional
questions were scoring zero, and which does help, but is now the part
that lags. A query naming a file and a line still has to reach the
lookup, and the model does not always phrase it so.

Retrieval remains the whole of the loss: every miss had its value in the
archive. Nothing here is a storage problem or a compaction problem.
