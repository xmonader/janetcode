# Does calling tools from a script actually pay?

- Model: `anthropic/claude-sonnet-4.5` via OpenRouter
- Tasks: `evals/27-survey-many-files`, `28-same-edit-everywhere`, `29-audit-and-fix` (tag `fanout`)
- 3 tasks × 5 runs per arm, `--eval-only fanout --eval-runs 5`
- Arms: `execute_code` available vs `--eval-off script`

These three tasks are the shape the tool exists for: a dozen files to look
at, a per-file decision, and the same edit or answer across all of them.
Nothing else in the eval set fans out like that.

## The first answer was that it made things worse

| | passed | context per solved | cost per solved |
|---|---:|---:|---:|
| with `execute_code` | 15/15 | 36k | $0.071 |
| without | 15/15 | 26k | $0.067 |

Paired bootstrap over the shared tasks: context **x0.74 [0.70, 0.78]** in
favour of *not* having the tool — an interval that excludes 1, so a real
difference, not noise. Cost x0.95 [0.83, 1.13], inside the noise.

Turning the feature off made the harness 26% cheaper in context. That is
the opposite of what a single hand-run demo had suggested, and it is the
result the paired interval was added to catch.

## Why: a quarter of the scripts died on the same line

The per-run breakdown showed the survey task reading 25–39 files with the
tool and 12 without, over 4–6 model calls instead of 2–3. The model was
scripting, failing, and then doing the work again by hand.

Five of the twenty scripts (25%) ended in the same traceback:

```
TypeError: read_file() takes 0 positional arguments but 1 was given
```

The stubs were generated as `def read_file(**kwargs)`, so the natural
`read_file("pkg/f.go")` — and `list_dir(".")` — raised. Every failure cost
a whole extra model call plus the tokens to rewrite the script.

The fix is that a stub's first argument is positional: the first *required*
property of the tool's own JSON schema, falling back to the first declared
property for tools whose arguments are all optional (`list_dir`). The
description now shows the signature, `read_file(path, ...)`, rather than
the bare name.

## The answer after the fix

Same tasks, same arm B, `execute_code` fixed:

| | passed | context per solved | cost per solved | scripts failed |
|---|---:|---:|---:|---:|
| without the tool | 15/15 | 26k | $0.067 | — |
| with it, before the fix | 15/15 | 36k | $0.071 | 5/20 (25%) |
| with it, after the fix | 15/15 | 27k | **$0.059** | **0/14 (0%)** |

Paired bootstrap, with-fixed against without:

- cost **x0.88 [0.75, 0.99]** — a real saving, about 12%
- context x1.03 [1.00, 1.05] — a real but tiny increase
- pass rate unchanged, 15/15 both ways

Cost falls while context rises slightly because the mix changes: input
tokens drop from 149k to 105k while cache reads rise from 250k to 306k, and
a cache read is much cheaper than a fresh input token.

Adoption, over 15 runs: the model reached for `execute_code` in 9 of them
(60%), and 164 of its 252 file reads happened inside a script, never
entering the context.

## What this does and does not establish

It establishes that on fan-out work the tool is now cheaper rather than
more expensive, and that the earlier single-run demo (62k tokens to 4.3k)
was not a lie but was also not the measurement.

It does not establish much beyond that. The bootstrap resamples **three**
tasks, so the interval is coarse and its upper bound sits at 0.99 — a
twelve-percent saving that only just clears its own interval. One model.
Tasks written for this feature. The honest reading is "it pays on this
shape of work, by about a tenth, and it no longer hurts", not "it is a
twelvefold saving", which is what the anecdote implied.

Reproduce, or check the numbers without paying for them:

```
janetcode --eval evals --eval-only fanout --eval-runs 5 --eval-json a.json
janetcode --eval evals --eval-only fanout --eval-runs 5 --eval-off script \
          --eval-json b.json --eval-compare a.json
janetcode --eval-report b.json --eval-compare a.json   # offline, no API calls
```
