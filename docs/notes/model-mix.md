# Model mixing: the hard set, the strong slot, the cascade

Notes on running one session across more than one model: a cheap slot, a
default slot and a strong slot, and the policies that move work between
them. The Jev-specific half of this work lives in `jev.md`; this file
holds the parts that need no decision model at all.

The prior is the mixai research repository (`grokplay/mixai`), which ran
eight mixing strategies over six "gap" tasks and measured one winner:
**cascade** -- let the cheap model try, and when the *real check* fails,
call the expensive one. It was the only mix that was both complete and
cheaper than every frontier baseline. The losers are as instructive: a
difficulty judge over-routed to the frontier on tasks that merely sounded
hard; advisor and fusion **tools** went unused because the model had to
choose to call them; a hardcoded mid-session promotion misfired in both
directions; and a senior-plus-junior pair cost more than the senior alone.

## 2026-09-21 -- the hard set, and why it is not hard

`evals/hard/` (81-86) ports mixai's six gap tasks: small Python programs
whose stated behaviour a cheap model gets subtly wrong -- an occurs check
in unification, vector clocks that are concurrent at equal sums, integer
cent splitting with the remainder to the front, a regex engine forbidden
from importing `re`, a topological order with a lexicographic tie-break,
and a PN-counter whose merge must be componentwise max.

Each ships its `pytest` specification as a `locked` file, so an edit to
the test is restored before grading, and a `.janetcode/verify.json` that
runs the same tests inside the run. The gate is therefore *truthful*: it
fails exactly when the task is unsolved, which is the property a cascade
trigger needs and which the existing gated set (71-73) does not have --
there the gate is build, vet, gofmt and tests over work the model wrote,
and the model arrives at the end of a turn with a clean tree.

**The set is not hard for any model tested.** Three runs a task, gate on,
decisions off, through OpenRouter:

| model | passed | cost | wall | $/solved |
|---|---:|---:|---:|---:|
| `qwen/qwen3.7-plus` (3 runs a task) | **18/18** | $0.092 | 10m8s | $0.0051 |
| `z-ai/glm-5.3-flash` (2 runs a task) | **11/12** | $0.037 | 25m3s | $0.0034 |
| `qwen/qwen3.7-flash` (partial, cut short) | 5/5 | -- | -- | -- |

mixai's junior was `google/gemma-4-31b-it`, which failed the regex task
and passed the other five. Two years of model releases later, a model at
$0.075/M input passes eleven of twelve. **The gap these tasks were built
to expose has closed at the price point they were built to expose it at.**

That is a result about the tasks, not about mixing. It is recorded here
rather than worked around, because the honest consequence is that every
arm that needs headroom -- advisor, routing, and the interesting half of
cascade -- cannot be read on this set with these models. The set is still
worth committing: it is the only set in the repository whose gate is
exactly its grading criterion, and it will discriminate again for a
weaker slot.

## 2026-09-21 -- the strong slot: GLM 5.3 flash is a cheap model

The tentative pick for the strong/advisor slot was "GLM 5.3 flash".
Verified against OpenRouter's live models endpoint rather than assumed:

| model | input $/M | output $/M | context |
|---|---:|---:|---:|
| `z-ai/glm-5.3-flash` | 0.075 | 0.25 | 1.31M |
| `z-ai/glm-5.3-flashx` | 0.37 | 1.25 | 1.05M |
| `z-ai/glm-5.3` | 0.91 | 2.86 | 1.31M |
| `qwen/qwen3.7-plus` | 0.32 | 1.28 | 1.00M |
| `qwen/qwen3.7-max` | 1.48 | 4.43 | 1.00M |
| `mistralai/devstral-2512` | 0.40 | 2.00 | 0.26M |

`z-ai/glm-5.3-flash` is priced at a quarter of `qwen/qwen3.7-plus` on
input and a fifth on output, and it scored *below* it on the hard set
(11/12 against 18/18). **It is a cheap-slot model, not a strong-slot
model.** The non-flash model of the same generation, `z-ai/glm-5.3`, is
the GLM candidate for a strong slot; `z-ai/glm-5.3-flashx` sits between
them. The concern was worth testing and the answer was the unwelcome one.

The slot is therefore held by `qwen/qwen3.7-plus`: it is the only
candidate measured at the top of the hard set, and at $0.0051 a solved
task it is not an expensive strong slot by any historical standard.

Note also what the price table says about `mistralai/devstral-2512`: at
$0.40/M input it is **more expensive than the model it would be the cheap
tier for**. A "cheap tier" is a measurement, not a reputation.

## 2026-09-21 -- repo-map precision: a word that matches eight files

`precision@5` on the 58-file explore fixture was **0.00**: the map's top
five were `report/report1..5.go`, junk pulled above the two real answers
(at ranks 8 and 9) because the request ends "Report which two files were
involved". Two defects with one cause, both deterministic, no decision
model involved:

1. The directory component `report` matched eight files and was worth the
   full `weightPart` (2.0) to each of them. A word that matches eight of
   fifty-eight files is evidence about a directory, not about any file in
   it.
2. The symbol stem rule then matched `Report1`..`Report7` against the same
   word `report`, so each filler file collected `weightSymbol` (1.5) on
   top -- the same request word counted twice for one file.

The fix is one idea applied twice, in `(*Index).Rank`: a request word
pulls a file **once**, by its strongest route, and that pull is divided by
the number of files the word matches. `symbolMentioned` grew a sibling,
`symbolMention`, that returns *which* word matched (deterministically --
the shortest, then lexicographically smallest -- because `words` is a map
and its range order must never reach the arithmetic). A file changed
against HEAD is not a request mention and is not damped.

| | precision@5 | recall@10 | `billing/limits.go` | `report/audit.go` |
|---|---:|---:|---:|---:|
| before | 0.00 | 1.00 | 9 | 8 |
| rarity damping only | 0.20 | 1.00 | 1 | 9 |
| **shipped (damping + one pull per word)** | **0.40** | **1.00** | **1** | **2** |

0.40 is the ceiling: the fixture has exactly two right answers, so two of
five is all a perfect ranker can score at k=5. Both numbers are now
pinned by `TestRepoMapPrecisionOnTheExploreFixture` beside the existing
rank baseline, and the whole `repomap` suite is unchanged otherwise.

One honesty note on the second step. With damping alone the two answers
and the seven fillers end up at *equal* personalisation, and what puts
`report/audit.go` at rank 2 rather than rank 9 is partly the graph and
partly the lexicographic tie-break on equal scores. A fixture whose
filler files sorted before `audit.go` would not get the same rank 2. The
*precision* claim is robust -- the fillers can no longer outrank the
answers -- but the exact rank 2 is not load-bearing and should not be
quoted as one.

**Open, not built:** a Jev rerank of the top ten. The deterministic fix
reaches the measurable ceiling on this fixture, so there is no headroom
here to buy a call with. It would need a fixture with more than two right
answers before it could even be scored.

## 2026-09-21 -- the cascade, and the trigger the gate cannot pull

`strong_model` + `escalate_after` ship behind a flag, both default off
(`internal/agent/escalate.go`). When the verification gate has failed N
rounds in a turn, the rest of that turn's model calls go to the strong
slot. The cheap step router is asked *after* the cascade, so a turn that
is already failing its own checks cannot also dip to a cheaper tier, and
`routeCheapSmallContext` deliberately does not apply: that 20k ceiling
exists to stop a cold prompt cache being paid for a *cheaper* answer.

Three arms, three tasks (`81-hard-unify`, `84-hard-re`, `85-hard-topo` --
the discriminating third), three runs each, gate on, decisions off, one
binary, `--eval-jobs 2`, no 429s:

| arm | passed | cost | $/solved | wall | model calls | escalations |
|---|---:|---:|---:|---:|---:|---:|
| (a) base solo `devstral-2512` | 8/9 [0.67,1.00] | $0.345 | $0.0431 | 15m45s | 121 | -- |
| (c) cascade `devstral` → `qwen3.7-plus` | 9/9 [1.00,1.00] | $0.124 | $0.0138 | 7m28s | 69 | **0** |
| (b) strong solo `qwen3.7-plus` | 9/9 [1.00,1.00] | $0.053 | $0.0059 | 6m14s | 37 | -- |

**The cascade fired zero times, so (c) is (a) with two inert config keys
-- and it still "beat" it by 1 pass, 2.8x cost and 2.1x wall.** That is
the honest headline of the round: on `84-hard-re` the same model on the
same task took 35, 34 and 26 calls in one arm and 13, 8 and 24 in the
other. At n=9, on this model, an A/B measures devstral's variance.

The strong slot is also the cheapest and the fastest arm: 37 model calls
against the base model's 121, $0.053 against $0.345. On this set there is
nothing for a cascade to save, because the "cheap" model is neither
cheaper nor quicker -- `devstral-2512` bills $0.40/M input against
`qwen3.7-plus`'s $0.32, and spends three to nine times the calls getting
there. **Cascade's premise is that most work is junior-easy and the
junior is cheap. Here the junior is not cheap.**

**Why it fired zero times, which is the more useful result.** Nine base
runs produced sixteen gate commands and **zero gate failures**, and the
single failure in the arm was `84-hard-re` run 0 *timing out* at 35 model
calls -- not a wrong answer. The reason is structural: the hard set's gate
runs the same `pytest` the model runs itself, so the model only ends a
turn once the tests pass, and the gate then agrees with it. **A truthful
gate confirms; it does not catch.** The property that makes the gate a
good grading criterion is exactly the property that makes it a poor
cascade trigger for a self-checking model.

So a second trigger shipped, chosen by that evidence rather than guessed:
a turn past 60% of its `max_iterations` and still going escalates. It is
still a fact about this run rather than a prediction about the request,
which is the property the whole design rests on. **It is untested live:
the arms above ran on the build before it.**

For a gate-failure trigger to have anything to pull on, the set needs a
check the model cannot run for itself -- a hidden specification, or a
check that costs more than the model will spend. That is the next
concrete step, and it is a task-set change, not a harness change.

## 2026-09-21 -- advisor and routing: built to the measurement gate, stopped at it

`advisor_mode: "always"` forces a consult with `advisor_model` at turn
start and at the first gate failure, injecting the reply as guidance text
(`internal/agent/advisorforce.go`). It is forced by the harness because
mixai measured that a consult offered as a *tool* is a consult the model
declines. Capped at two a turn, 45s timeout, fail-open on every path.

Only `off` and `always` are implemented, and `always` was not measured
live. The rule this repository already set is that gating can only
recover a fraction of a gain always-on has to show first -- and on a set
where the cheap model passes 8/9 there is no gain to show. Writing the
gated policy, and the Jev call that would drive it, before that number
exists would be inventing a threshold rather than fitting one.

Workstream 6 (a calibrated "will the cheap model pass?" noul, scored
against real pass/fail) is blocked on the same thing and for a sharper
reason. Its training labels are per-task cheap-tier pass rates. This
round's are 8/9, 11/12, 18/18 and 5/5: **a one-class corpus**. A
calibration measured against it would report the majority rate and learn
nothing, which is exactly where `--eval-train` sat for eighteen months.
Nothing was built.

## 2026-09-21 -- a gate the model cannot run, and what it caught

The previous section ended on the one concrete next step: "for a
gate-failure trigger to have anything to pull on, the set needs a check
the model cannot run for itself." That set now exists (`evals/hidden/`,
mechanism in `docs/manual/evals.md`, evidence in `docs/notes/jev.md`),
and the first thing to report about it is that it works: **seven held-out
gate failures in thirty calibration runs, every one on code the model
wrote, with the repair rounds spent on them.** Against the hard set's
sixteen gate commands and zero failures, that is the whole difference.

The second thing to report is that the difficulty is not calibrated.
Four of the five tasks are passed first-attempt by both tiers in every
run and the fifth is failed first-attempt by both in every run, so the
band the pilot was aiming at (30-70%) is empty and the two tiers cannot
be told apart on first-attempt pass at all: 0.83 [0.58, 1.00] for
`z-ai/glm-5.3-flash` against 0.80 [0.60, 1.00] for `qwen/qwen3.7-plus`.
The strong slot therefore stays where the last round put it, for a worse
reason than last time: not because this set confirmed the choice, but
because it cannot separate the candidates.

A third fact matters for anyone reading the pass column: **every final
failure in the pilot was the wall cap, not a wrong answer.** At 420 s,
`glm-5.3-flash` -- which took 56m26s over fifteen runs against qwen's
26m43s -- timed out six times. The arms were run at 900 s.
