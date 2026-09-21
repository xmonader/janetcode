# Jev (TypeSafe System One) for janetcode: where it fits (2026-09-20)

Jev is TypeSafe AI's structured-decision model, released 2026-09-15: a
state plus typed questions (Noul yes/no, Choice, Score) in, probability
distributions and confidences out, all questions over one state answered
in parallel. No string generation, so schema-matching is guaranteed by
construction. Published price $0.042/M input tokens, output unmetered;
quoted latency 70–500ms end-to-end from the US West Coast. TypeSafe's
own versioned limitations page lists arithmetic, counting, date
comparison, indirect questions, distracting context and adversarial
input as weaknesses, and says the model is not a security boundary.

This note records what the marketing claims survive as, the full
use-case enumeration for janetcode, and the filter that cut 83
candidates to 13.

## The announcement, revised

- "Can't hallucinate" — true only as schema conformance. The model can
  pick the wrong option with high confidence on any single call. No
  type errors is real and verifiable; no errors is not on offer.
- "40x–200x faster", "444.6x cheaper" — measured against full chat
  completions from frontier models, the slowest baseline available.
  Against an LLM constrained to structured output the multiple is far
  smaller. Headline numbers are a ceiling; they say so in footnotes.
- "Calibrated: higher confidence means higher accuracy" — the strongest
  claim and the one with no published evidence. Their workflow evals
  measure agreement with the average of two frontier models they
  picked, not ground truth; a model can agree with both and be
  miscalibrated wherever both are systematically wrong. Verify
  calibration on our own labeled sessions before setting any threshold.
- "Owns the Pareto frontier" — on their four workflows, their wrapper
  constraining the LLM side, their reference models. A demo of intended
  use, not a measurement.

What survives revision: guaranteed typed output; parallel questions
over one state (13 questions in one call legitimately beats 13 calls);
first-class probability distributions enabling threshold routing; the
price, if sustained; and the thesis — most "LLM calls" in a running
system are classification in disguise.

## Why the filter is harsh here

Three properties of janetcode remove most candidate uses before
accuracy is even discussed:

1. **Deterministic code already does it exactly.** Shell risk
   (`internal/shell/safety.go`), tool Guards, workspace confinement,
   loop detection, context pressure (a token count), secret patterns
   (`internal/memory/secret.go`), failure classification
   (`llm.ClassifyFailure`), mechanical-step routing
   (`internal/agent/steproute.go`). A probabilistic answer to a
   question Go answers exactly is strictly worse.
2. **Jev's own weaknesses are our content.** Coding-agent states are
   tool traces, diffs and test output — counting, arithmetic, long
   noisy context, distractors: the limitation list verbatim.
3. **Per-call latency is real money in a turn loop.** 100–500ms before
   every tool call is unaffordable; per-turn or per-event only.

## The 13 that survive

Ranked by expected value:

1. **Verification-failure triage** — failed gate in, typed answers out:
   patch-caused / environmental / flaky / unrelated, retry vs stop,
   repair likelihood. Compact state, direct control of an expensive
   branch. The exit code stays authoritative; Jev only classifies what
   to do next. **Pilot this first.**
2. **Trajectory stuck/progress scoring** — the local critic
   (`internal/critic`) is a fitted model that cannot train until enough
   eval runs fail; Jev needs no training data. Use as a second signal
   beside the predictor, not a replacement.
3. **Request intake classification** — explain / implement / debug /
   review, plus complexity and mode, one call before the first LLM
   request. Feeds model-tier and mode routing.
4. **Plan-drift detection** — periodic "is the current work still what
   the user asked" over goal + changed files.
5. **Offline session annotation** — label the 154+ recorded sessions
   (necessary call? progress? misuse?) to train the local critic and
   discover rules to graduate into Go. Zero live risk; the exit
   strategy for everything else on this list.
6. Subagent delegation decision (worth it? which profile?).
7. Memory extraction triage at session end (keep? type? obsolete?).
8. Hint/warning suppression in the TUI (worth showing?).
9. Session outcome classification for sleep consolidation.
10. Premature-stop detection (claimed done without evidence) —
    borderline, evidence-checking brushes the counting weakness.

Advisory only, never authoritative:

11. Suspicious command/edit second opinion — a warning layered on the
    deterministic guards, never a gate.
12. Goal-mismatch alert on tool calls.
13. (with 12) the only two security-adjacent uses retained.

## The 83-item enumeration and where it died

The full list was brainstormed unconstrained, then each item tagged.
Tally: 13 kept, 4 advisory, 66 cut. Cut taxonomy:

- **24 — deterministic code does it exactly**: repo_map self-ranking,
  parallel read-only tools, todo trigger rule, verify-gate presence,
  loop counting, context pressure, secret regexes, provider failure
  classes, hook trust, `/stats` counters, mutation-target derivation
  from the diff.
- **17 — security or authority**: approval levels, injection detection,
  permission gating, side-effect blocking. Jev's own docs say it is not
  a security boundary; the harness's fail-closed rules stay sovereign.
- **11 — arithmetic/counting/staleness**: anything over token counts,
  turn counts, line counts, timestamps, coverage numbers.
- **8 — per-call latency**: tool-call gating, per-result relevance,
  TUI event prioritisation.
- **6 — no ground truth to calibrate on**: "enough context", "plan
  quality", user-frustration detection.

## Architecture if adopted

Jev is not an `llm.Provider` — no messages, no text out, different
semantics. It would be a separate optional interface:

```go
type DecisionProvider interface {
    Evaluate(ctx context.Context, state any, questions []Question) (DecisionSet, error)
}
```

with `internal/jev` plus two consumers (`jevverify`, `jevcritic`).
Every decision timeout-bounded, fail-open to current behaviour, logged
with model version + latency + confidence, excluded from eval runs
unless explicitly enabled, never in the permission path.

## Pilot

`--jev-verify-triage` on the multistep set (51–62), which is built to
produce real failures. Measure against the current baseline: extra
latency per failed gate, unnecessary repair calls avoided, true
environmental detections, false stops, cost per solved, pass rate
(with `--eval-compare`, ≥5 runs per side per the standing guidance).
Also first: replay Jev over recorded failed-gate events offline and
check its confidence calibration by hand — if the probabilities do not
track outcomes on our own data, nothing live ships.

## Open

- Calibration on janetcode's own labeled sessions (the gate the whole
  idea hangs on).
- Whether triage state (command + tail of output + diff summary) fits
  Jev's context without hitting the long-noisy-input weakness.
- A Go client; only official JS/Python SDKs were found. The Requesty
  and Vercel gateway routes are OpenAI-shaped enough to hand-roll if
  the API stays `state` + `questions`.
- Pricing sustainability at eval-harness volume.

## First A/B: triage versus baseline (2026-09-20, live)

Janetcode headless, deepseek/deepseek-v4-flash on OpenRouter, identical
config, ablation by JANETCODE_OFF=decisions; trust seeded per task dir;
"calls" is the envelope's model-call count.

- env-failure task ("make a.txt say ok" under a gate that greps a file
  the change cannot touch, so no repair can pass; n=3 per side):
  baseline 6–8 model calls, triage 3–4 — **half the repair burn**,
  decision itself ~0.6–1.0s and ~400–700 input tokens per failed gate;
  wall time flat (45–60s both sides: the saved calls are the cheap part,
  the turn-not-spent effect shows in calls, not seconds); cost equal or
  slightly lower with triage (never higher). All 3 triage runs ended on
  one confident environmental verdict; the fold recorded `check failed`
  honestly in all 3 — the exit stays authoritative.
- patch-caused task (one edit away; n=3 per side): **identical to the
  digit** — 4 model calls, 7.8k tokens, 0 decision calls, on every run:
  when the work is right the gate never fails and Jev costs nothing.
- multistep, comfortably in range (fib/CLI under a verify gate, n=2):
  no gate failures on deepseek, no decision calls, verdicts identical.
  Jev's overhead on solved work measured zero.
- multistep at capacity (INI parser round-trip under a gate, n=2 per
  side): both sides hit the 12-turn / max-cost cap (exit 53), decision
  fired once on one run; costs 0.0018–0.0038 both sides — under the
  budget cap there is nothing to report except that the ceiling
  dominated before triage could matter.

Caveats: n=3 and n=2, one model, one machine, hand-trapped tasks — this
is a capability demonstration and a smoke of the instrumentation, not
the calibration replay the spec gates consumer-on-default on. Verdict
wording on the simple tasks is "not demonstrated"/"self-checked" on
both sides regardless of Jev — the pre-existing verdict semantics treat
grep gates as checks, not tests; nothing here changed them.

## Phase 0: calibration replay, first live run (2026-09-20)

The recorded-session gate corpus is structurally empty (no project ran
gates before 2026-09-20), so the calibration ran over a hand-labeled
state corpus — `docs/benchmarks/decisions/triage.jsonl`, 35 states
whose truth comes from the task's construction (a check the change
cannot touch is environmental, a check naming the edited file's symbol
is patch_caused, and so on), drawn from the shapes the trap A/B and
eval fixtures actually produced. `janetcode --decisions-replay`
shipped with this round asks the same questions and applies the same
action mapping a live turn takes.

| truth | n | skip% | mean p(caused) | mean conf | correct | costly |
|---|---|---|---|---|---|---|
| environmental | 8 | 75% | 0.18 | 0.81 | 6 | 0 |
| flaky | 5 | 40% | 0.30 | 0.75 | 2 | 0 |
| inconclusive | 6 | 0% | 0.58 | 0.54 | 6 | 0 |
| patch_caused | 10 | **0%** | **0.73** | 0.79 | **10** | 0 |
| unrelated | 6 | 83% | 0.14 | 0.89 | 5 | 0 |
| overall | 35 | | monotone | | 29 (82%) | **0** |

Against the spec's gates: ≥70% correct ✓ (82%); patch_caused ordering
the highest ✓ and skipped **never** ✓ (zero costly wrong skips);
monotone ✓. The weak cell is flaky (2/5 correct): its mean confidence
(0.75) sits right at the mapping's 0.7 skip threshold, so the borderline
rows flip to the conservative send-back. Honest and safe: a missed
flaky skip costs a repair round, never correctness.

Provenance caveat, kept deliberately loud: the labels are by
construction, not by recorded-session post-hoc truth — the recorded
corpus has no gate events to score until gate-using projects accrue
sessions. This satisfies the spirit of the gate (calibrate before
thresholds) on labeled data of our own shape; it is not the same data
queue as recorded sessions, and the note says so.

## Evidence-Preserving Reducer (SoL-Pi's EPR): verified, integrated, reverted (2026-09-20)

The offline prototype verified the mechanism end to end: a 72KB test
failure log reduced to a 912-byte receipt (12s, ~21k cheap tokens), 5/5
quoted lines byte-exact; a perturbed source rejected the same receipt
(all-or-nothing gate fires at line granularity); a passing run
compressed to 58 bytes with zero quotes (correct). So the mechanism is
real and the gate is mechanically sound.

The integration (decisions.receipts, one cheap side call in the
long-output path) then ran the ms3 A/B — a task whose gate emits a
~23.5KB log per failure — n=3 per side on deepseek-v4-flash:

| metric | baseline | with receipts |
|---|---|---|
| model calls (median) | 5 | 5 |
| wall (mean) | 46.6s | **66.5s** |
| input tokens (mean) | 12,363 | **24,027** |
| cost (mean) | $0.00076 | **$0.00123** |
| receipts generated per run | 0 | 2 (2, 2, 0 — one rejected) |

Receipts worked — 2–4 per run, the evidence gate rejecting one — but
the cost side dominates in janetcode's regime: the side call's input
(the whole oversized output) is paid every time it fires, while the
receipt's saving (a 12KB preview down to ~1KB) only pays back on later
turns, which short sessions never reach. ms2 runs never fired at all
(python assertion output stays under the 12KB budget). The paper's 45%
number is measured against Pi replaying full logs; janetcode already
captures most of that with head+tail stubbing and archive+expand_output
— measured years (of prompt) earlier in its own leanness rounds.

Kept: nothing in the shipped tree (reverted per the keep-only-if-it-
improves rule), the prototype's numbers, and the method: receipts are a
good idea whose correct regime is *long* sessions whose receipts
actually recur — exactly the EdgeBench long-horizon setting the paper
measures, not the short-turn regime the A/Bs run in. The offline
prototype (reduce.py approach + quote gate) is the reference we would
start from if that regime arrives.

## Noul criteria on caused_by_change: replay A/B (2026-09-21, live)

Same 35-row corpus, same pinned model, `--decisions-replay`. Baseline is
the two-question live payload with no criteria; treatment adds
true/false `criteria` to `caused_by_change` only (the text restates the
`failure_class` option descriptions, it was not fitted to corpus rows).

| arm | runs | overall | flaky | environmental | unrelated | patch_caused | mean p(caused) patch / flaky | costly skips |
|---|---|---|---|---|---|---|---|---|
| no criteria | 1 (+ the 2026-09-20 run) | 28/35 (80%), 29/35 (82%) | 2/5 | 6/8 | 4/6 | 10/10 | 0.73 / 0.31 | 0 |
| criteria | 2 | 31/35 (88%), 32/35 (91%) | 3/5, 3/5 | 7/8, 7/8 | 5/6, 6/6 | 10/10, 10/10 | 0.77 / 0.24 | 0 |

The criteria widen the separation in both directions (patch up, every
skip class down), which moves borderline rows off the 0.3 threshold.
No rows ended in error in any run. Caveat: n=35 and run-to-run noise is
about one row, so the honest claim is "+3 rows, never worse in any
cell", not a precise percentage. flaky is still the weak cell: its class
confidence (0.71–0.73) sits on the 0.7 mapping threshold.

## Sharper failure_class option text: replay A/B (2026-09-21, live)

Same 35-row corpus, same pinned model, `--decisions-replay`, three runs
a side. Baseline is the shipped question set after the noul-criteria
change above. The treatment rewrites the five `failure_class` option
descriptions to say what each class *is* rather than name it:
`environmental` covers a tool or runtime that is "missing, unreachable
or the wrong version" (it said "missing" only); `flaky` covers "timing,
a race, a port or another shared resource in the test setup" as well as
"passed on a retry"; `patch_caused` and `inconclusive` say what
evidence would pick them. The text generalises the class definitions —
it was not written against particular rows.

| arm | runs | overall | environmental | flaky | unrelated | patch_caused | inconclusive | mean class conf | costly skips |
|---|---|---|---|---|---|---|---|---|---|
| baseline | 3 | 32, 31, 31 /35 | 7, 7, 7 /8 | 3, 3, 3 /5 | 6, 5, 5 /6 | 10/10 ×3 | 6/6 ×3 | 0.71–0.80 | 0 |
| sharper text | 3 | 33, 33, 33 /35 | 8/8 ×3 | 4, 4, 4 /5 | 5, 5, 5 /6 | 10/10 ×3 | 6/6 ×3 | 0.88–0.93 | 0 |

Shipped. Two rows move and stay moved in every run — `env:py39-vs-310-syntax`
(a runtime version mismatch, which the old text did not describe) and
`flaky:parallel-workers-same-port` (a race over a port, likewise) — and
mean class confidence rises 0.1–0.2 in every cell, which is the
mechanism: the answers are no longer sitting on the 0.7 mapping
threshold. patch_caused stays 10/10 with zero costly skips.

The one `unrelated` row that fails in the treatment,
`unrelated:gate-runs-every-package`, also fails in two of the three
baseline runs: it is a `go test ./...` failure in `internal/netx` after
a docs-only edit, genuinely split between `unrelated` and
`environmental`, and it fails for want of confidence in *either*, not
for want of the right label. It is a borderline row in both arms, not a
regression.

Two rows are still wrong in every treatment run. `flaky:timing-off-by-ms-under-load`
(p(caused) 0.69–0.71) is a row where the change really did touch the
backoff the failing test measures; the label is arguably the weaker
party. The `unrelated` row above is the other.

## Skip mass instead of class confidence: tried and reverted (2026-09-21)

The mapping skips when p(caused) ≤ 0.3 **and** the winning class is a
skip class **and** its confidence ≥ 0.7. But `environmental`, `flaky`
and `unrelated` all mean the same thing to the mapping — another repair
round will not fix this — so an answer that splits 0.45/0.40 between two
of them is certain about the *decision* and uncertain only about the
*label*, and reading the winner's confidence throws that away. The
treatment summed the distribution's mass over the three skip classes and
thresholded that instead (identical for a distribution with one winner,
strictly more permissive otherwise), falling back to the old reading
when a provider reports no distribution.

| arm | runs | overall | unrelated | patch_caused | costly skips |
|---|---|---|---|---|---|
| class confidence (shipped) | 3 | 33/35 ×3 | 5/6 ×3 | 10/10 ×3 | **0** |
| skip mass | 2 | 33/35, 33/35 | **6/6, 6/6** | **9/10, 9/10** | **1, 1** |

Reverted. It buys the `unrelated` cell and pays with the one thing the
gate forbids: `patch:fixture-reads-tightened-schema` is skipped in both
runs — a repair round not spent on a failure the patch really caused.

The reason is worth keeping. On that row the model answers p(caused)
0.21–0.23 and puts 0.88–0.90 of its class mass on skip classes: it is
confidently wrong, and the only thing standing between it and a costly
skip today is that the mass is split between two labels, so no single
one clears 0.7. Today's safety on that row is an accident of the
scoring rule, not a signal. Any later widening of the skip condition has
to answer for that row first.

## Structured state instead of rendered text: tried and reverted (2026-09-21)

The wire contract takes `state` as a string, an object or an array; the
client only ever sent a string. The treatment widened the client with an
`EvaluateState(ctx, any, qs)` and had the replay split each corpus state
back into `{request, failing_check, output, changed_this_turn}`, sending
the same evidence as an object. Two runs a side, same corpus, same
questions.

| arm | runs | overall | environmental | flaky | unrelated | patch_caused | mean p(caused) patch / unrelated |
|---|---|---|---|---|---|---|---|
| rendered string | 3 | 33/35 ×3 | 8/8 | 4/5 | 5/6 | 10/10 | 0.77 / 0.09 |
| structured object | 2 | 29/35, 30/35 | 7/8, 7/8 | 3/5, 3/5 | **3/6, 4/6** | 10/10 | 0.80 / 0.13 |

Reverted, along with the API widening it needed. It is worse in three
cells and better in none, and the direction is consistent: every class's
mean p(caused) rises, so the model reads a field-keyed object as more
incriminating than the same words as prose. The `unrelated` cell halves.
Per-field scrubbing, which was the other reason to want an object, was
solved differently — see the redaction note below.

## Hardening round (2026-09-21)

Four changes with no replay to run, each with a test that fails without
it.

**The replay is no longer killed by its own circuit breaker.** A burst
of 503s used to open the circuit three rows in, after which every
remaining row returned "circuit is open" and counted as incorrect — a
corpus lost to a transient outage, read as a calibration result.
`jev.Client.NoBreaker` turns the breaker off for `--decisions-replay`
and `doctor`; the live path is unchanged and must never set it.

**The circuit half-opens.** It used to open for the rest of the session,
so a five-second blip cost a four-hour session all of its triage. After
a five-minute cooldown exactly one probe call is allowed: success closes
the circuit and resets both breakers (including the latency window,
which is stale by then and would otherwise re-open on the first
success), failure buys another cooldown. The claim is made under the
same lock that reads the state, so eight racing turns produce one probe;
the session-level notice is still emitted once however often the circuit
re-opens.

**One scrubbing hook, at the wire, redacting the span.** There were two:
`jev.Client.Scrub`, which production never set, and `agent.ScrubState`,
which replaced a whole state field with `[redacted]` when
`memory.ScanSecret` fired anywhere in it — and which covered the request
and the output but not the failing command line, the likeliest place for
a key to be (`curl -H "Authorization: Bearer …"`). The per-field hook is
gone and the wire hook is wired, using a new `memory.RedactSecrets` that
replaces each matched span and, for an assignment, only the value. A
blanked field is a decision made blind; a blanked span is a decision
made on the evidence minus the key.

**Spend is visible.** The response's `usage.cost` and `output_tokens`
are parsed and accumulated, and `/stats`, the session `decision` event
and the headless `decisions` envelope report cumulative USD beside the
existing latency. The figure is always the service's own — a decisions
model is not in the harness's rate table, so an unreported cost stays
zero rather than becoming an estimate.

## Phase 3a intake classification, log-only (2026-09-21)

The session's first prompt is classified — `kind` (choice of explain /
implement / debug / review / docs / other), `complexity` (a five-rung
score), `needs_plan` (a noul) — behind `decisions.intake`, default off.
Nothing reads the answer. It is written to the session log as a
`decision` event carrying `"acted_on": false`, counted in `/stats`, and
that is all: routing, plan mode and the todo hint keep their
deterministic rules, and acting on any of this needs its own measured
gate. The call runs on its own goroutine started before the turn's
Prepare hook and is collected once the turn's work is done, so it adds
nothing to the turn; an answer still in flight by then is dropped.

Calibration on a new 18-row corpus (`docs/benchmarks/decisions/intake.jsonl`,
three rows per kind, hand-written, truth by construction), two runs:

| truth | n | correct (run 1, 2) | mean conf | mean complexity | mean needs_plan |
|---|---|---|---|---|---|
| explain | 3 | 3, 3 | 1.00 | 0.31, 0.34 | 0.30 |
| implement | 3 | 3, 3 | 1.00 | 2.41, 2.40 | 0.71 |
| debug | 3 | 3, 3 | 1.00 | 1.43, 1.44 | 0.61 |
| review | 3 | 3, 3 | 0.99 | 0.66, 0.64 | 0.55 |
| docs | 3 | 3, 3 | 1.00 | 0.91, 0.90 | 0.26 |
| other | 3 | 2, 2 | 0.80 | 0.50, 0.51 | 0.27 |
| **overall** | 18 | **17/18 (94%)** | | | |

`kind` is near-saturated and the one miss is arguably the label's fault:
"what timezone are these session timestamps in?" is called `explain`
(confidence 0.47), which is defensible. The two unscored answers do
separate — complexity runs 0.3 (explain) to 2.4 (implement) on the
rubric's 0–4 index, needs_plan 0.26 (docs) to 0.71 (implement) — which
is the thing worth knowing before anything acts on them. Same provenance
caveat as the triage corpus: hand-constructed, not recorded sessions.

## Phase 2 (trajectory): skipped, not attempted (2026-09-21)

The spec fixes Phase 2's hook point (every five tool calls), its state
and its questions (`on_goal`, `stuck`, `recommended`,
`success_likelihood`), but not the two things an implementation has to
decide: the five rungs of the `success_likelihood` rubric, and what
"disagrees with the local signal" means numerically against a critic
score that is a probability with a 35% warning threshold. Both would be
invented here and then calibrated against themselves. There is also no
corpus to calibrate against — the truth for `stuck` is whether the
session went on to fail, which is exactly the recorded-session data the
Phase 0 note says does not exist yet. Left undone deliberately.

## The open Phase 1 gate: cannot be run as specified (2026-09-21)

The spec's second gate is evals 51–62 under `--eval-compare`, ≥5 runs a
side, triage on against triage off, passing if the pass rate is not
worse and repair calls are down. It cannot be run today, and not for
want of budget: **the eval harness has no verify gate and no decision
client.** `internal/eval`'s agents come from `internal/minimal`, which
never calls `SetVerify` and never wires `Decisions`; `verifyGate`
returns immediately on a nil runner, and `triageFailure` is only ever
reached from it. Both sides of the comparison would be byte-identical
runs, and the gate would "pass" having measured nothing.

What it would take, in order: wire a verify gate into the eval agent
(per task, from the project type or a task-declared command), wire
`stepDecisions`' equivalent into `internal/minimal`, and then
**re-baseline every eval number in the repository** — adding a gate
changes tool counts, pass rates and cost on every task, so the existing
`docs/benchmarks/multistep` baselines would no longer be comparable.
That is its own round with its own risks and must not be slipped in
beside a triage change.

Cost once it is wired, from this repository's own measured figures for
the same set (`docs/benchmarks/multistep/REPORT.md`, PROJECT-STATE:
five runs of one variant of evals 51–62 on qwen3.7-flash ≈ 6 h and
≈ $0.55): two sides at five runs is ≈ 12 h wall and ≈ $1.10 — an order
of magnitude past an hour, so it was not started. The command lines,
for whoever picks it up:

```sh
make build
# baseline: triage off
./bin/janetcode --eval evals --eval-only multistep --eval-runs 5 \
    --eval-jobs 2 --eval-off decisions --eval-label triage-off \
    --eval-json docs/benchmarks/multistep/triage-off.json
# treatment: triage on (needs decisions.triage: true in the config and
# the key in the environment)
./bin/janetcode --eval evals --eval-only multistep --eval-runs 5 \
    --eval-jobs 2 --eval-label triage-on \
    --eval-json docs/benchmarks/multistep/triage-on.json \
    --eval-compare docs/benchmarks/multistep/triage-off.json
```

Read the paired bootstrap intervals, not the point estimates: this set
swings about 21% between identical runs, which is why the spec asks for
five.

## The Phase 1 gate, wired and run: triage never fired (2026-09-21)

The blocker above is gone. `--eval-gate` (off by default) wires two
things onto the eval agent that only a real session had: the
verification gate — the work directory's `.janetcode/verify.json` if the
fixture ships one, otherwise the checks for its project type (`go build
./...` + `go test ./...`, or `python3 -m compileall -q .`) — and the
configured decision provider, through the same `decisionsClient` a
session's `stepDecisions` now calls, scrubber included. Without the flag
the eval agent is exactly what it was, so every baseline already in this
repository stays valid; `--eval-off decisions` keeps the gate and drops
the provider, which is the control arm.

Both arms therefore ran **with the gate on**; the only difference was
decisions on versus off. Same model (`dashscope-coding/qwen3.7-plus`),
same tasks, the two arms launched two seconds apart so endpoint drift
hits both.

```sh
make build
# decisions on (config has a "decisions" block, triage+intake true,
# OPENROUTER_API_KEY in the environment only)
./bin/janetcode --eval evals --eval-only 51-price-chain-bug,56-two-failures \
    --eval-gate --eval-runs 3 --eval-jobs 3 --eval-timeout 360s \
    --eval-label jev-on --eval-json jev-on.json
# decisions off, gate still on
./bin/janetcode --eval evals --eval-only 51-price-chain-bug,56-two-failures \
    --eval-gate --eval-off decisions --eval-runs 3 --eval-jobs 3 \
    --eval-timeout 360s --eval-label jev-off --eval-json jev-off.json
# second pair, harder tasks: --eval-only 52-options-refactor,57-expr-layers
#   --eval-runs 2 --eval-jobs 4 --eval-timeout 330s
```

n per cell: 3 runs × 2 tasks (51, 56) plus 2 runs × 2 tasks (52, 57) per
arm — 10 runs an arm, 20 runs in all, ~23 minutes of wall clock with the
four processes overlapping. No 429s.

| | decisions on | decisions off |
|---|---|---|
| passed | 10/10 | 9/10 |
| gate commands run | 20 | 18 |
| **gate failures** | **0** | **0** |
| repair rounds | 0 | 0 |
| triage calls | 0 | 0 |
| skips taken | 0 | 0 |
| intake calls (log-only) | 10 | — |
| main-model calls | 76 (mean 7.6) | 66 (mean 6.6) |
| main-model tokens in / out | 83,262 / 36,267 | 78,970 / 38,848 |
| main-model cost | not priced (`cost_known: false` for this model) | same |
| wall per run, mean | 135 s | 178 s |
| Jev latency p50 | 640 ms (n=10, intake only) | — |
| Jev cost | $0.000333 total | — |
| circuit-open events | 0 | 0 |

**The finding is the zero.** Triage fired not once in 20 gated runs,
because the gate never failed in 20 runs: this model runs `go test`
itself as a tool before it ends a turn, so by the time the gate runs the
tests already pass. Triage is reachable in evals now — a unit test
drives a failing gate into `triageFailure` with a fake provider — but on
this eval set it has nothing to triage. Everything else in the table is
noise at n=10: the pass difference is one off-arm run that hit the
330 s `--eval-timeout` mid-turn (a harness cap, not a wrong answer), and
the model-call and wall-time gaps are inside the ±21% this set swings
between identical runs. No cost comparison is possible at all —
qwen3.7-plus has no price table here, so `cost_usd` is 0 on every run.

What did get measured is intake's price, which is real and small: one
call per run at 640 ms p50 and $0.000033 a call, off the turn's critical
path, and no circuit opened.

To actually measure triage, the gate has to be one that fails for
reasons the model cannot pre-empt: flaky or environmental commands
(a network fetch, a port bind, a clock- or ordering-sensitive test) in
the fixture's own `.janetcode/verify.json`. That is a corpus change, not
a harness change, and it is the next step — the current eval set cannot
answer the Phase 1 question however many runs it is given.

## Long tasks, a gate that can fail, and the mid-turn ceiling (2026-09-21)

The previous section ended on a zero: 20 gated runs, no gate failure, no
triage call. Two things were wrong with that set — the tasks were short
(6–20 model calls) and the gate was the project-type fallback, `go build
./...` plus `go test ./...`, which is exactly what this model runs
itself before it ends a turn. Both were fixed here, and the zero came
back.

### The gated set

`evals/gated/` (commit b7f1529) holds three long tasks that ship their
own `.janetcode/verify.json`. 71 and 72 are copies of the session
fixtures 41 and 43 — seven and six prompts, 22–36 model calls and 31–44
tool calls a run — and 73 is a copy of 57-expr-layers. They are copies,
not edits, so every baseline already recorded for 41, 43 and 57 stays
comparable. The Go gate is four commands:

```json
{"commands": ["go build ./...", "go vet ./...",
  "out=$(gofmt -l .); if [ -n \"$out\" ]; then echo 'gofmt: not formatted:'; echo \"$out\"; exit 1; fi",
  "go test ./..."], "timeout": 120}
```

`go vet` and the `gofmt -l` guard are the point: they are ordinary
things a repository gates on and unusual things a model runs on itself.

Before trusting a zero again, the plumbing was proved with a fixture
whose gate cannot pass (`{"commands": ["go build ./...", "echo
'integration smoke test could not reach the fixture service'; exit
1"]}`, kept out of the repository). The gate failed, triage fired once,
called it `environmental` at p(caused)=0.07 and confidence 0.96, and
skipped the repair round — 314 ms, $0.000028. So the gate, the triage
call and the skip all work end to end; on the real tasks they simply
have nothing to do.

### Half 1: Jev on versus Jev off, gate on in both arms

```sh
make build
XDG_CONFIG_HOME=<a config dir whose config.json has the decisions block:
  {"model":"~typesafe/jev-latest","timeout_ms":2000,"triage":true,"intake":true}>
COMMON="--eval evals/gated --eval-gate --eval-runs 3 --eval-jobs 3 \
        --eval-timeout 420s --eval-max-turns 80"
./bin/janetcode $COMMON --eval-label jev-on  --eval-json jev-on.json &
sleep 2
./bin/janetcode $COMMON --eval-off decisions --eval-label jev-off \
        --eval-json jev-off.json &
```

n per cell: 3 runs × 3 tasks per arm, 9 runs an arm, 18 in all, ~73
minutes with the six processes overlapping, no 429s. Same endpoint and
model as the last round (`dashscope-coding/qwen3.7-plus`); the two arms
launched two seconds apart so endpoint drift hits both. The raw
`--eval-json` files carry every session event and are not committed.

| | decisions on | decisions off |
|---|---|---|
| passed | 9/9 | 8/9 |
| main-model calls, total (mean) | 175 (19.4) | 177 (19.7) |
| main-model calls, per-run range | 5–28 | 5–36 |
| tool calls, total | 241 | 239 |
| **failed/wrong tool calls** | **4** | **4** |
| loop calls | 0 | 0 |
| **gate commands run** | **120** | **116** |
| **gate failures** | **0** | **0** |
| repair rounds | 0 | 0 |
| triage calls / skips | 0 / 0 | 0 / 0 |
| costly wrong skips | none (no skips) | — |
| intake calls (log-only) | 9 | — |
| tokens in / out | 302,062 / 51,070 | 290,385 / 47,823 |
| wall per run, mean (median) | 359 s (372 s) | 381 s (304 s) |
| Jev latency p50 / cost | 664 ms / $0.000298 total | — |
| circuit-open events | 0 | 0 |

"Failed/wrong tool call" is defined from what the log actually records:
a `tool/result` event carrying a tool name and `is_error: true`, or
`denied: true` (the harness refused, nothing ran). Reads of paths that
do not exist and edits whose `old_string` does not match both arrive
that way, so they are counted; "repeated identical call" is counted
separately as a loop (≥3 consecutive calls with the same tool and the
same arguments, or ≥3 calls on the same tool-and-path inside a window of
five) and did not occur once in 480 tool calls.

Per task, sorted within the cell so the spread is visible:

| task | arm | pass | model calls | tool calls | wall s |
|---|---|---|---|---|---|
| 71-gated-go-service | on | 3/3 | 26 / 27 / 28 | 31 / 35 / 44 | 372 / 444 / 802 |
| 71-gated-go-service | off | 3/3 | 26 / 31 / 36 | 36 / 41 / 43 | 304 / 668 / 670 |
| 72-gated-python-ledger | on | 3/3 | 22 / 23 / 28 | 34 / 36 / 37 | 258 / 473 / 578 |
| 72-gated-python-ledger | off | 2/3 | 12 / 23 / 27 | 19 / 36 / 39 | 257 / 514 / 577 |
| 73-gated-expr-layers | on | 3/3 | 5 / 6 / 10 | 7 / 8 / 9 | 68 / 117 / 122 |
| 73-gated-expr-layers | off | 3/3 | 5 / 5 / 12 | 7 / 7 / 11 | 141 / 146 / 151 |

**Verdict: not enough signal, and the reason is structural rather than
statistical.** Every difference in the table sits inside the run-to-run
spread of one cell — 71 alone swings 26 to 36 model calls and 304 to 802
seconds between identical runs, which is wider than any arm gap. The one
off-arm failure was a stalled stream, not a wrong answer. The number
that matters is the one that is not noise: **236 gate commands ran
across the two arms and none of them failed**, so triage, the only thing
Jev changes today, had nothing to act on. That is a harder zero than the
last one: this gate included `go vet` and a `gofmt -l` guard on every
gating turn, and the model still arrived at the end of every turn with a
clean tree. Adding runs to this design cannot produce a signal.

### Half 2: would a mid-turn consumer pay?

The 18 recorded runs are the trajectory data Phase 2 never had. Every
tool call in them was labelled from the log alone, no judgement:

- **(a) errored** — the result came back `is_error` or `denied`.
- **(b) loop** — ≥3 consecutive calls with identical tool and
  arguments, or ≥3 calls on the same tool-and-path within a window of
  five.
- **(c) dead tail** — the run did not pass, and no later call ever
  produced a successful mutating result or a passing gate.

A main-model call counts as wasted if it issued a labelled call or sits
in the dead tail. The result:

| | on | off | both |
|---|---|---|---|
| wasted main-model calls | 2 / 175 | 4 / 177 | **6 / 352 = 1.7%** |
| wasted tool calls | — | — | 8 / 480 = 1.7% |
| of which loops | 0 | 0 | **0** |

**The ceiling is 1.7% of main-model calls, and that is the answer to the
expectation that Jev shows up as fewer main-model calls.** A perfect
mid-turn oracle, consulted free and never wrong, could remove at most
one main-model call in sixty on this model and this set. qwen3.7-plus on
these tasks does not flail: it never repeated a call, never reverted an
edit, and errored on eight tool calls out of 480, mostly a first
`edit_file` whose anchor did not match, which it fixed on the next step.

The corpus was still built and scored, as a smoke test of the
instrument rather than a calibration: `docs/benchmarks/decisions/
stuck.jsonl`, 28 rows (8 waste, 20 healthy) built by
`build-stuck-corpus.py` from the same two `--eval-json` files, scored
through the new `consumer: "stuck"` branch of `--decisions-replay`
(commits fa7e01f, af119be). Two runs:

```sh
./bin/janetcode --decisions-replay docs/benchmarks/decisions/stuck.jsonl
```

| question | thr | precision | recall | run 2 |
|---|---|---|---|---|
| `stuck` | any of 0.5–0.9 | — (never fires) | 0.00 | same |
| `on_goal` (inverted) | 0.5–0.9 | 0.00 | 0.00 | same |
| `next_call_will_fail` | 0.5 | 0.73 | 1.00 | 0.73 / 1.00 |
| `next_call_will_fail` | 0.6 | 0.71 | 0.62 | 0.60 / 0.38 |
| `next_call_will_fail` | 0.7 | 1.00 | 0.25 | 1.00 / 0.25 |

Mean probabilities, waste versus healthy: `stuck` 0.26 vs 0.12,
`on_goal` 0.85 vs 0.78, `next_call_will_fail` 0.63 vs 0.25. Replay
latency p50 358 and 367 ms over the two runs.

Three caveats, and the third is the one that decides it. Eight positives
cannot support a precision estimate — 0.73 is eight rows. `stuck` never
fires because there were no loops to see, which is the ceiling result
restated rather than a failure of the question. And
`next_call_will_fail` is reading an error that has already happened: the
state window ends at the call being labelled and shows its own `ERROR:`
line, which is also what (a) labels the row on. It is not scoring a
prediction, it is scoring whether Jev can read a line the harness
already has, deterministically, for free, at zero latency.

**Recommendation: reject a mid-turn consumer for this model.** Not
"later" — on these numbers. The ceiling is 1.7% of main-model calls; the
only question that separated the classes separates them by re-reading a
flag the harness already holds; and the latency story is the opposite of
free: 640 ms p50 live (358 ms on the replay's warmer path) consulted
every step on a 30-step run adds ~19 s of wall clock to save at most
half a step, and a false positive interrupts a healthy run, which is the
costly error. The only trigger policy that could have paid — consult
only after a tool error or a repeated call — is a policy whose trigger
is already the deterministic signal, so the consult adds latency and
subtracts nothing.

What would change the answer, stated so it is not re-litigated by
adding runs: a model that actually loops (the ceiling here is the
model's competence, not Jev's), or a gate that fails for reasons the
model cannot pre-empt, which on this evidence means something outside
the repository — a network fetch, a port bind, a service — not a
stricter local lint. `evals/gated/` is the place to put that when there
is one; the instrument (`--eval-gate`, the `stuck` consumer, the corpus
builder) is finished and waiting.

## A weaker main model: devstral-2512, gate failures and real loops (2026-09-21)

The previous two sections both ended on the same zero, and both said the
same thing about why: `dashscope-coding/qwen3.7-plus` never fails the
gate, never loops, and wastes 1.7% of its calls, so triage — the only
thing Jev changes today — has nothing to act on. This round changes the
main model and nothing else. The Jev side is untouched: same pinned
`~typesafe/jev-latest` (answers report `typesafe/jev-1.13-20260917`),
same `--eval-gate`, same `evals/gated/` 71–73, same metric and
waste-label definitions.

### Picking the model

OpenRouter's `/api/v1/models` was queried and filtered to models whose
`supported_parameters` contain `tools`, in the small open-weights class
and cheap. Four were piloted with one gated run each on
`73-gated-expr-layers`, under a scratch `XDG_CONFIG_HOME` whose
`config.json` holds an `openrouter` provider plus the `decisions` block
(`~/.config/janetcode` was not touched; the key lives in the scratch
file and is never committed):

| pilot model | pass | model calls | tool calls | tool errors | wall | cost | what it showed |
|---|---|---|---|---|---|---|---|
| `qwen/qwen3-coder-30b-a3b-instruct` | FAIL | 2 | 1 | 0 | 5 s | $0.0008 | one `list_dir`, then an empty assistant message and `end_turn` — no progress at all, measures nothing |
| `qwen/qwen3-32b` | FAIL | 5 | 16 | 2 | 7 m (timeout) | $0.005 | never finished a turn inside the cap |
| `mistralai/mistral-small-3.2-24b-instruct` | FAIL | 24 | 25 | 6 | 3 m 27 s | $0.029 | ended on `ErrStuck` ("stuck repeating the same tool call") — loops, but never passes |
| **`mistralai/devstral-2512`** | **PASS** | **42** | **42** | **0** | **1 m 57 s** | **$0.041** | passed *and* failed its gate (`gofmt -l`), triage fired, `patch_caused` p=0.88 conf=0.93, repair round spent, run then passed |

`devstral-2512` is the one in the useful band: it finishes real work, and
it arrives at the end of a turn with a tree the gate rejects — which is
exactly what qwen3.7-plus never did. The tier below it (mistral-small
24B, qwen3-32b, qwen3-coder-30b) is too weak to be informative: a model
that cannot drive the tools produces 0% and measures the harness, not
Jev. Pinned model id for this round: **`mistralai/devstral-2512`**.

### The A/B

```sh
make build
# XDG_CONFIG_HOME points at a scratch config dir whose config.json is
#   {"provider":"openrouter","model":"mistralai/devstral-2512",
#    "providers":{"openrouter":{"type":"openai",
#       "base_url":"https://openrouter.ai/api/v1","api_key":"<from env>",
#       "model":"mistralai/devstral-2512"}},
#    "decisions":{"model":"~typesafe/jev-latest",
#       "api_key_env":"OPENROUTER_API_KEY","timeout_ms":2000,
#       "triage":true,"intake":true}}
COMMON="--eval evals/gated --eval-gate --eval-runs 3 --eval-jobs 1 \
        --eval-timeout 420s --eval-max-turns 80"
./bin/janetcode $COMMON --eval-label jev-on  --eval-json ab-on.json &
sleep 2
./bin/janetcode $COMMON --eval-off decisions --eval-label jev-off \
        --eval-json ab-off.json &
```

n per cell: 3 runs × 3 tasks per arm, 9 runs an arm, 18 in all, ~24
minutes with the two processes overlapping. `--eval-jobs 1`, not 3: the
first attempt at `--eval-jobs 3` (six concurrent devstral requests) was
thrown away — every run in it came back `Provider returned error (HTTP
429)`, which is an invalid run by the rule set for this experiment, and
the whole batch was rerun at one job an arm. The A/B below has **no
429s**. Raw `--eval-json` files are not committed.

| | Jev on | Jev off |
|---|---|---|
| passed | 9/9 | 7/9 |
| main-model calls, total (mean) | 265 (29.4) | 329 (36.6) |
| main-model calls, per-run range | 22–42 | 20–64 |
| tool calls, total | 380 | 436 |
| **failed/wrong tool calls** | **10** | **6** |
| **loop calls** | **45** | **44** |
| **gate commands run** | **123** | **129** |
| **gate failures** | **1** | **3** |
| repair rounds spent | 1 | 3 |
| triage calls / skips | 1 / 0 | 0 / 0 |
| costly wrong skips | **none (no skip was taken)** | — |
| intake calls (log-only) | 9 | — |
| tokens in / out | 1,221,601 / 50,673 | 1,265,191 / 60,545 |
| main-model cost | $0.683 | $0.764 |
| wall per run, mean (median) | 117 s (124 s) | 159 s (157 s) |
| Jev latency p50 / cost | 532 ms / $0.000331 total | — |
| circuit-open events | 0 | 0 |

Per task, sorted within the cell so the spread is visible:

| task | arm | pass | model calls | tool calls | wall s | gate failures |
|---|---|---|---|---|---|---|
| 71-gated-go-service | on | 3/3 | 23 / 28 / 32 | 40 / 49 / 54 | 124 / 131 / 133 | 0 / 0 / 1 |
| 71-gated-go-service | off | 3/3 | 25 / 29 / 30 | 48 / 50 / 50 | 110 / 170 / 200 | 0 / 1 / 1 |
| 72-gated-python-ledger | on | 3/3 | 22 / 26 / 28 | 40 / 43 / 45 | 90 / 124 / 162 | 0 / 0 / 0 |
| 72-gated-python-ledger | off | 3/3 | 20 / 29 / 32 | 33 / 44 / 45 | 103 / 126 / 147 | 0 / 0 / 0 |
| 73-gated-expr-layers | on | 3/3 | 27 / 37 / 42 | 29 / 39 / 41 | 82 / 89 / 117 | 0 / 0 / 0 |
| 73-gated-expr-layers | off | 1/3 | 37 / 63 / 64 | 38 / 63 / 65 | 157 / 206 / 211 | 1 / 0 / 0 |

The single triage call, in full: task 71 run 2, the gate's `gofmt -l`
guard failed, Jev answered `caused_by_change` 0.89, class
`patch_caused` at confidence 0.95, in 380 ms. The truth was
`patch_caused` — the model had written an unformatted `main.go` — so
the correct action was to spend the repair round, which is what
happened, and the run then passed. **Jev took no skip in this round, so
it skipped no repair round the weak model needed, and there is no costly
wrong skip to list.** That is the safety answer: on this evidence Jev
does not hurt.

**Verdict: not enough signal, again, but for a new reason.** The gate now
fails — four times across 18 runs, where 236 gate commands produced zero
failures last round — and loops now happen, 89 of them. But triage was
consulted exactly once in 9 gated runs, and its one answer was the
answer that changes nothing. A single call cannot explain the on-arm's
9/9 versus 7/9, its 64 fewer model calls or its 42 s shorter mean run;
those sit inside the spread one cell shows between identical runs (73
alone runs 27 to 64 model calls off-arm and 27 to 42 on-arm), and both
off-arm failures are the harness's 60-tool-call cap firing on a model
that padded, not a wrong answer. Attributing any of that to Jev would be
reading noise. The honest reading is: a weaker model makes the gate fail
often enough for triage to exist, but not often enough — at 4 failures
per 18 runs — for 9 runs an arm to measure what triage is worth.

### Half 2: the waste ceiling, without the leakage

Every tool call in the 18 runs was labelled by construction, same three
labels as last round (errored / loop / dead tail):

| | on | off | both |
|---|---|---|---|
| wasted main-model calls | 18 / 265 | 19 / 329 | **37 / 594 = 6.2%** |
| wasted tool calls | 53 / 380 | 50 / 436 | 103 / 816 = 12.6% |
| of which errored | — | — | 16 |
| **of which loops** | **45** | **44** | **89** |
| of which dead tail | — | — | 0 |

**The ceiling moved from 1.7% to 6.2% of main-model calls, and the shape
of the waste changed completely.** With qwen3.7-plus the waste was eight
errored calls and no loops; with devstral it is 89 loop calls and 16
errors. A perfect mid-turn oracle could now remove about one main-model
call in sixteen. That is the first time this number has been large
enough to be worth an oracle at all.

The corpus was rebuilt from these two files and rescored:

```sh
python3 docs/benchmarks/decisions/build-stuck-corpus.py \
    --out docs/benchmarks/decisions/stuck.jsonl --per-class 40 \
    ab-on.json ab-off.json          # 80 rows (40 waste, 40 healthy)
./bin/janetcode --decisions-replay docs/benchmarks/decisions/stuck.jsonl
```

The leakage the first round flagged is fixed (commit 29fa85f). The state
window now ends at call i-1 and the labelled call appears as
`About to run: <tool> <args> -> (not run yet)`, so all three questions
are scored before the result exists. `--leaky` reproduces the old
framing.

| question | thr | run 1 precision / recall | run 2 |
|---|---|---|---|
| `stuck` | 0.5 | 0.60 / 0.07 | 0.60 / 0.08 |
| `stuck` | 0.6 | 1.00 / 0.05 | 1.00 / 0.05 |
| `stuck` | 0.7 | 1.00 / 0.03 | 1.00 / 0.03 |
| `on_goal` (inverted) | 0.5 | 0.50 / 0.05 | 0.50 / 0.05 |
| `on_goal` (inverted) | ≥0.6 | 0.00 / 0.00 | 0.00 / 0.00 |
| `next_call_will_fail` | 0.5 | 0.75 / 0.15 | 0.71 / 0.13 |
| `next_call_will_fail` | 0.6 | 0.75 / 0.07 | 0.67 / 0.05 |
| `next_call_will_fail` | 0.7 | 1.00 / 0.03 | 1.00 / 0.03 |

Replay latency p50 329 ms and 352 ms over the two runs.

**`stuck` finally has positives to find, and finds almost none of them.**
40 of the 80 rows are waste and 89 of the 103 waste calls in the pool
are loop calls, so the question "is this agent repeating itself without
making progress?" is being asked on a corpus built mostly of exactly
that. It fires on three of forty at 0.5 and on two at 0.6. The
precision figures that read 1.00 are one or two rows and mean nothing.

A leaky rebuild (`--leaky`, scored once, 78 rows, 2 rows lost to a
provider deadline) gives `next_call_will_fail` 0.60/0.15 at 0.5 — this
round the leaky and honest framings barely differ, because the waste is
no longer mostly errored calls whose `ERROR:` line the leaky state
handed over. That is a second-order confirmation of the first round's
caveat rather than a contradiction of it.

**Recommendation: reject a mid-turn consumer for this model class too,
and this time the rejection is about Jev, not about the model.** The
headroom is real now — 6.2% of main-model calls, 12.6% of tool calls,
89 loop calls — and Jev cannot see it: recall 0.05–0.15 at any threshold
where precision is not one row. The concrete trigger policy that was
worth pricing, and the arithmetic that kills it: consult only when the
last two calls share a tool and a path (the cheapest deterministic
prefix of a loop). On these runs that trigger fires on roughly 90 of 816
calls, ~5 times a run; at the measured 532 ms live p50 that is ~2.7 s a
run of added latency, against a 117–159 s run, which is affordable. What
is not affordable is the return: at 0.07 recall the consult catches
fewer than one of the ~5 loop episodes a run, and at 0.60 precision at
that threshold two of five firings interrupt a healthy step. The trigger
itself — "same tool, same path, twice" — is already the signal, is free,
and is deterministic; a consult on top of it adds latency and subtracts
nothing. Nothing was wired mid-turn.

What would change the answer: a question that is asked about the loop
rather than about the agent ("has the state this call reads changed
since the last identical call?"), which is a harness fact and needs no
model at all. That is the finding to carry forward — the waste at this
model class is repetition, and repetition is detectable without Jev.

### Caveats

- 4 gate failures and 1 triage call in 18 runs. Every triage statistic
  in the table is n=1.
- The on/off gaps in pass rate, model calls and wall time are not
  attributable to Jev: Jev intervened once, and that intervention
  changed nothing (`patch_caused` → spend the round, the default).
  Intake is log-only (`acted_on: false`).
- The first `--eval-jobs 3` batch was discarded for 429s; only the
  `--eval-jobs 1` batch is reported. Running one job an arm is also why
  the arms are not perfectly time-matched inside a task.
- Both off-arm failures are the 60-tool-call harness cap, not wrong
  answers.
- `mistralai/devstral-2512` is priced by OpenRouter, so unlike
  qwen3.7-plus the cost column is real.
- Total main-model spend for the round, pilots and the discarded 429
  batch included: ~$1.84. Jev spend: ~$0.007.

## Intake routing: shipped, acting, explain only (2026-09-21)

Phase 3a classified the first prompt and nobody read it. The question
this round asks is the one that was deferred: can anything act on it?

### What janetcode can actually route on today

Three per-turn knobs exist, and only one of them is a model choice.
`route_cheap` (`internal/agent/steproute.go`) sends mechanical steps of
a turn to a cheaper model and refuses the first call of every turn by
construction — `a.stepN <= 1` — because before a step has finished there
is nothing to judge the turn by. Reasoning `effort` is per-request but
honoured by the Anthropic provider alone, so it cannot be measured on
either of this round's models. Plan mode is settable per turn and was
**not** used: janetcode's plan mode is read-only until a human accepts,
so turning it on for an implementation turn would stop the work rather
than sequence it, and in headless or eval runs there is nobody to
accept. Subagent delegation has no harness-side decision point at all —
the model calls the `task` tool or it does not.

So `decisions.intake_route` routes exactly one thing: it lets the
existing cheap route start at the first call of the turn that was
classified. Nothing else is relaxed, and no tier system was invented.

### The corpus

`docs/benchmarks/decisions/intake.jsonl` grew from 18 rows to 41 and
gained a second label, `truth_route`, by the same construction rule
throughout: **cheap exactly when the request asks for no change to the
repository and can be answered from at most two files.** The 23 new rows
are aimed at the routing gate rather than the kind one — eight
explain/review requests that really are one look, eight that sound
trivial and are not ("is this thread-safe?", "anything racy in
internal/tui?"), seven that sound like questions while asking for a
change. Same provenance caveat as every corpus here: hand written.

### The rubric was the bug

First scoring run, thresholds as designed (explain+review, kind
confidence ≥ 0.8, complexity ≤ 1.0 at confidence ≥ 0.6, needs_plan <
0.5):

| | routed cheap | kept on strong |
|---|---|---|
| truth cheap (12) | 7 | 5 |
| **truth strong (29)** | **7** | 22 |

Seven costly wrong routes. The cause is visible in the answers file, not
in the mapping: `review:security` scored complexity **0.02**, and
`review:one-line-leak` scored **0.03**. The rubric's five rungs were all
phrased as edits — "one look", "a small edit", "a contained change", "a
feature", "a project" — so a request that changes nothing has no rung to
land on and falls to zero whatever it asks for.

The fix is the same shape as the `failure_class` squeeze that shipped
earlier: reword the question, change nothing else. The rungs now count
reading as work ("a whole subsystem to read through"), and the
instructions say so outright: *a question can cost as much as a change,
because judging whether something is correct, safe, fast or thread-safe
means reading all of it first.*

| | kind correct | costly wrong routes | cheap recall |
|---|---|---|---|
| edit-only rubric, 1 run | 35/41 | 7 / 29 | 7/12 |
| reading-aware rubric, 3 runs | 35/41, 35/41, 35/41 | 1, 1, 1 | 5/12 |
| reading-aware, explain only, 2 runs | 35/41, 36/41 | **0, 0** | 3/12 |

Kind accuracy is untouched by the rewording, which is the thing that had
to be checked before the rubric could move: the shipped log-only
consumer is calibrated on it.

### Why review was cut

The one costly route that survives the rubric fix is the same row in
every run: *"look over internal/tool/fetch.go and tell me whether the
private-address guard can be bypassed"* — one file named, complexity
0.23, and in truth the guard plus everything that calls it. review is
the kind the classifier cannot size, so it is excluded on the
measurement rather than on principle. Putting it back buys two more
cheap routes out of twelve and costs that one; the dial is one entry in
`routeKinds` and the numbers above are what to re-read before turning
it.

**Shipped acting**, behind `decisions.intake_route`, default off:
explain only, 0/29 costly over five scoring runs, 3/12 recall. The
costly error is stated plainly: a request that needs the strong model
sent to the weak one — bounded, because the step router's taint rules
end the route at the first edit, failure or warning after the first
call.

Cost of the wait: routing must be decided before the first request, so
this is the one intake call a turn waits on — 700 ms at most, after
Prepare has had its chance to cover it, and a late answer is collected,
priced and logged at the end of the turn without routing anything.

## Offline session annotation, and the critic's first negative class (2026-09-21)

Item 5 on the surviving list, and the one with no live risk: the
sessions are already over. `--decisions-annotate <sessions>` takes a
session file or a directory of them, folds each recorded turn out of the
log (`agent.AnnotateTurns` — prompt, the turn's tool calls with their
results, the harness's own verdict line) and asks four questions of it:
how it ended (success / partial / failed / abandoned), whether it made
progress, whether work was wasted, whether a tool was misused.

Two files come out. `annotations.jsonl` is the rows. `critic-train.json`
is shaped like an eval result file, deliberately, so `--eval-train`
reads it without knowing where it came from.

### Why that second file is the point

`internal/critic` fits a logistic predictor on `{Features, Passed}`:
`Features` is the sixteen-number `agent.FoldTrajectory` vector, `Passed`
is whether the eval run passed. It has never trained, for one reason
recorded in PROJECT-STATE since 2026-09-08 — **there are no negatives.**
`critic.Train` refuses one-class data outright, and the eval runs that
produce the training data nearly all pass.

A recorded session that went badly is a negative. So the annotation's
outcome answer becomes the label: a session counts as a pass when its
last annotated turn came back `success`, and `partial`, `failed` and
`abandoned` are all "no".

Run over the 28 sessions in `~/.local/share/janetcode/sessions`:

| | |
|---|---|
| sessions with turns to annotate | 13 (9 had no turn at all) |
| turns annotated | 29, 0 errored |
| outcomes | success 11, partial 6, failed 6, abandoned 6 |
| `wasted` ≥ 0.5 | 9 turns |
| `tool_misuse` ≥ 0.5 | 10 turns |
| training samples | 13 — **3 passing, 10 failing** |

`--eval-train` on that file:

```
trajectory critic trained on 13 runs (3 passed, 10 failed) from 1 file(s)
cross-validated accuracy 54% (always guessing the majority: 77%)
not saved: the critic is no better than guessing; more failing runs are needed
```

**The blocker moved.** For eighteen months of this repository's history
the critic could not be fitted at all; it now fits, and loses to the
majority class at n=13. That is a different problem, and a countable
one: more annotated sessions. Nothing was saved, so the live critic is
unchanged.

### Agreement against truth by construction

Model annotations are not ground truth, so they were scored against
labels that are: the 80-row `stuck` corpus, where every tool call was
labelled `waste` or `healthy` from the log alone by
`build-stuck-corpus.py`. The same four questions were run over those
states (`consumer: "annotate"`, a one-line retag of `stuck.jsonl`, not
committed twice), and `wasted ≥ 0.5` scored against the label. Two runs,
identical:

| | run 1 | run 2 |
|---|---|---|
| tp / fp / fn / tn | 16 / 5 / 24 / 35 | 16 / 5 / 24 / 35 |
| agreement | 51/80 (63%) | 51/80 (63%) |
| precision on waste | 0.76 | 0.76 |
| recall on waste | 0.40 | 0.40 |

For comparison, the mid-turn `stuck` question on the same corpus last
round was precision 0.60 at recall 0.07. The annotation question is the
better of the two by a distance — **it finds 40% of the waste and is
right three times in four when it fires** — which is a usable offline
labeller and still nowhere near a live gate.

Caveat worth stating: those 80 states are single tool calls rendered
mid-turn, and the annotation question is written about a whole turn. The
agreement rate is therefore a lower bound on the question as it is
actually asked, and an approximation either way.

## Session outcome classification, log-only (2026-09-21)

Item 9, behind `decisions.session_outcome`, default off. One call after
the last turn — success / partial / failed / abandoned, with the
choice's confidence — over one line a turn (what was asked, how many
calls, how many failed, the harness's verdict). Logged as a `decision`
event with `"acted_on": false` and counted in `/stats`.

It is offered to sleep consolidation, which is the one place it can be
worth something today: `sleepfold.Fold` builds `memory.SleepInput` with
a `GoalOutcome` that is empty whenever the model never opened a goal,
which is most sessions. The outcome now fills that blank — and only
that blank: a goal the model opened and closed still wins, because that
is the harness's own record rather than an opinion about it.

Two findings while wiring it:

- **There is no session-end hook outside headless.** `builtAgent.close`
  runs under a five-second shutdown grace, and the TUI cancels the
  context every background call uses (`m.endLife()`) *before* `tea.Quit`
  — so a classification started there would be cancelled by the exit it
  was triggered by. Headless is honest about it ("a headless run is one
  session per process, so its end is the session's end") and that is the
  only place this consumer is called from. A TUI session gets no
  classification, and saying so is better than starting a call that
  cannot finish.
- `sleepfold` rendered *every* decision event in triage's vocabulary, so
  an intake record folded to `intake →  (caused=0.00, sent the model
  back)`. A consumer with no class and no skip is now skipped, and
  session outcome renders as itself.

**Memory extraction triage (item 7) was not attempted.** The hook point
exists and was found — `[]Extracted` sits unwritten between `Extract`
and `Apply` in both `internal/tui/memory.go` and
`internal/headless/headless.go`, exactly the shape `StampEvidence`
already filters — but acting on it means suppressing candidates, the
costly error is dropping a memory worth keeping, and there is no corpus
of labelled candidates to measure that against. Building one is the
next piece of work, not a thing to skip the gate for.

## Premature stop: deterministic, and Jev adds nothing (2026-09-21)

Item 10, flagged borderline in the original list because evidence
checking brushes Jev's counting weakness. It turns out most of it was
already deterministic and already shipped, and the part that was missing
needed no model either.

What already exists: `TurnOutcome` (verdict.go) folds every turn from
the event log — files edited, checks run, how many passed, whether tests
were actually exercised, whether a red check later went green, whether
the model claimed something ("the tests pass") — and `Overclaimed()`
sends the turn back once when the claim and the record disagree. That is
premature-stop detection, and it has been in the harness for weeks.

What was missing is one thing, and it is an ordering: **`Ran` and
`Passed` are per-turn totals and cannot say whether anything ran after
the last edit.** A turn that edits, runs the tests, edits again and
stops is green on every number the fold keeps, and nothing has been run
against the tree as it now stands.

That is a free signal — two sequence numbers — so it was taken for free.
`TurnOutcome.EditedAfterCheck` is the last successful edit coming after
the last check; `StaleCheck()` is that plus a claim over it; the nudge
is behind `stale_check_nudge`, default off, and shares the overclaim
gate's one-correction-a-turn budget so the costly error — nagging a
finished turn into another round of model calls — is bounded to one
nudge. A turn that ran nothing at all is already `unverified` and
already sent back, and is deliberately left to that gate.

**Jev was not consulted, and there is nothing left for it to answer.**
The question was "does the transcript tail contain evidence for the
claim", and the harness holds the transcript, the exit codes and the
ordering. Asking a model to re-read what the harness already knows is
the same mistake the `next_call_will_fail` result exposed last round,
where 0.73 precision came from re-reading an ERROR line the harness was
already holding. Recorded as a negative: **item 10 ships deterministic,
Jev adds nothing to it.**

## The rest of the list: hook points surveyed, nothing built (2026-09-21)

Items 4, 6, 8, 11 and 12 were to be log-only if the round had room. It
did not, and what was learned about them is worth more than a rushed
consumer, so this is the survey and nothing else.

- **#4 plan-drift, periodic.** Hook point exists: `a.mutations`
  (turnstate.go) is the per-turn "the world changed" counter the verify
  gate already keys off, and the checkpointer holds the changed paths,
  so "every N mutating steps, goal + changed file names" is a few lines
  in `exec.go`. Not built because it has no corpus, and the one
  neighbouring measurement is discouraging: `on_goal`, asked per step,
  did not separate on either the 28-row or the 80-row corpus in two
  earlier rounds. A periodic question over changed *file names* is a
  genuinely different question, but "different enough to work" is a
  hypothesis and needs its own labelled data.
- **#6 subagent delegation. The hook point does not exist.** Delegation
  in janetcode happens when the model calls the `task` tool. There is no
  place in `RunBlocks` or `openTurn` where the harness decides to
  delegate, so there is nothing for a decision to attach to. Building
  one would be inventing a delegation policy and then asking Jev to run
  it. Skipped for the reason, not for the time.
- **#8 TUI hint suppression.** Hook point exists and is unusually clean:
  `internal/hints` is already a pure function of a `State`, evaluated at
  turn end, one hint per project. Filtering its output is a `[]Hint ->
  []Hint` filter. Not built because the costly error is suppressing the
  one hint that would have helped, the benefit is at most a little less
  noise, and the ratio does not justify a live consumer this round. It
  is the cheapest of the five to come back to.
- **#11/#12 advisory second opinion and goal-mismatch.** Hook points
  exist at the permission gate, which is exactly why they were left
  alone. The standing invariant is that Jev is never in the permission
  or security path; an advisory notice printed *beside* a decision the
  deterministic guards have already made does not break it, but it is
  one refactor away from doing so, and the value is a second opinion
  nobody asked for. Not built.

## 2026-09-21 — the on/off A/B, two models, four arms

The final verification for everything shipped this round. "On" is every
`decisions.*` flag this repo now has enabled (`triage`, `intake`,
`intake_route`, `session_outcome`) plus `stale_check_nudge`; "off" is all
of them off. The verify gate is on in both arms, and `route_cheap` with
`qwen/qwen3-30b-a3b-instruct-2507` is on in both arms — that is
deliberate, because `intake_route` does not turn routing on, it only lets
the existing cheap route start at the *first* call of a turn Jev called
cheap. So the off arm still routes; the on arm routes earlier.

Three runs a task, `--eval-jobs 1`, arms interleaved, scratch
`XDG_CONFIG_HOME`. Sets: `evals/gated` 71-73 and `evals/routing` 74-76.

    janetcode --eval <set> --eval-gate --eval-runs 3 --eval-jobs 1 \
      --eval-timeout 420s --eval-max-turns 80 \
      --eval-route qwen/qwen3-30b-a3b-instruct-2507 --eval-json <arm>.json

### mistralai/devstral-2512 (openrouter)

| gated 71-73 (n=9)   |     on |    off |
|---------------------|-------:|-------:|
| passed              |    7/9 |    8/9 |
| main-model calls    |    368 |    459 |
| tokens in / out     | 2.07M / 63.8k | 2.44M / 64.6k |
| main-model cost     | $0.769 | $0.905 |
| cheap-tier calls    |    107 |    145 |
| wall (s)            |   2018 |   2346 |
| failed tool calls   |      7 |      6 |
| loops (max_repeat)  |     34 |     43 |
| gate cmds / failures|  125/3 |  131/4 |
| repair rounds       |      3 |      4 |

| routing 74-76 (n=9) |     on |    off |
|---------------------|-------:|-------:|
| passed              |    6/9 |    6/9 |
| main-model calls    |     18 |     18 |
| cost                | $0.0161| $0.0149|
| cheap-tier calls    |     11 |      6 |
| wall (s)            |     30 |     57 |

### dashscope-coding/qwen3.7-plus

| gated 71-73 (n=9)   |     on |    off |
|---------------------|-------:|-------:|
| passed              |    7/9 |    9/9 |
| main-model calls    |    212 |    249 |
| tokens in / out     | 579k / 55.4k | 671k / 60.1k |
| main-model cost     | $0.0296| $0.0310|
| cheap-tier calls    |     36 |     42 |
| wall (s)            |   4135 |   4808 |
| failed tool calls   |      9 |      7 |
| loops (max_repeat)  |     28 |     30 |
| gate cmds / failures|  123/1 |  123/1 |
| repair rounds       |      1 |      1 |

| routing 74-76 (n=9) |     on |    off |
|---------------------|-------:|-------:|
| passed              |    6/9 |    6/9 |
| main-model calls    |     24 |     25 |
| cost                |$0.00396|$0.00307|
| cheap-tier calls    |     17 |     13 |
| wall (s)            |    130 |    395 |

### Per-consumer fire and acted-on counts (on arms only)

| consumer        | devstral fired / acted | qwen fired / acted |
|-----------------|-----------------------:|-------------------:|
| intake          |                18 / 5  |            18 / 6  |
| verify triage   |                 3 / 0  |             1 / 0  |
| session_outcome |         0 (headless only, not reached in eval) |  0 |
| stale-check nudge (deterministic, no Jev call) | 0 | 0 |

Jev itself: devstral 21 calls, p50 513 ms gated / 327 ms routing, $0.00071
total; qwen 19 calls, p50 627 ms / 374 ms, $0.00064 total. No breaker
trips, no timeouts recorded, no Jev error path taken.

### Costly errors, listed

The costly error for intake routing is *a hard task started on the weak
tier*. Live count: **zero, both models.** All 18 gated-task turns (three
implementation tasks x three runs x two models) routed `default`, and so
did all six runs of 76-routed-trivial-edit — the only `cheap` routes were
on 74 and 75, the two one-look explain questions, which is exactly what
the corpus said would happen (0/29 costly over five replay runs).

The costly error for verify triage is *skipping a gate that would have
caught a real failure*. It fired 4 times across both models and skipped
nothing, so the count is zero by construction this round.

74-routed-explain-value fails 0/3 in **both** arms and on **both**
models — both models answer "10" where the truth is 20. It is a
task/model discriminator, not a routing failure; the on and off arms give
the same wrong answer, one of them cheaper.

### Verdict

- **devstral:** decisions-on cost 20% fewer main-model calls, 15% less
  money and 14% less wall for one extra failure out of nine — a
  difference this sample cannot resolve from noise, pointing very
  slightly the wrong way on correctness.
- **qwen3.7-plus:** decisions-on cost 15% fewer calls and 14% less wall
  but lost two of nine passes (both on 73-gated-expr-layers, which is
  also the task with the widest run-to-run spread in every arm), so the
  on arm is not defensible as a win on this evidence.

Run-to-run spread is the honest headline: on the gated set a single task
varies by 2x in calls and 3x in wall between its own three runs, so a
9-run arm cannot detect a one-pass difference. What the A/B *does*
establish is the safety claim, which is what it was for: the routing
dial never once sent an implementation task to the weak tier live, triage
never skipped a gate, and no consumer took a Jev error path. The savings
are real and repeat across two models; the correctness delta is inside
the noise and needs a much larger n, or a set with less variance, before
anyone should read it either way. Nothing here justifies flipping these
flags on by default, and they stay off.

Total live spend this round: ~$1.75 main model, $0.0014 Jev.

## 2026-09-21 — Step 0: where the calls and the input tokens actually go

Before building any context-curation consumer, the harness was asked the
only question that sizes them: what is a run *spending*? `--session-profile`
(new, offline, no decisions call at all) reads recorded runs — an
`--eval-json` result file, a session `.jsonl`, or a directory of either —
and classifies every tool call as explore / edit / verify / other from the
log alone. `bash` is judged by its command: `shell.IsCheck` makes it verify,
a reading head (`ls`, `cat`, `find`, `git status`…) explore, a writing head
(`mkdir`, `rm`, `gofmt -w`…) edit, anything else other. A model call takes
the strongest phase of the calls it issued (edit > verify > explore >
other). `--session-profile-stale N` sets the age, in model calls, past which
an unreferenced tool result counts as dead weight; the default 6 is the tail
compaction keeps verbatim.

Input source: the **off arms** of the 2026-09-21 four-arm A/B, `evals/gated`
71-73, nine runs per model — the cleanest recorded behaviour this repository
has, with no decisions consumer touching it.

| | qwen3.7-plus | devstral-2512 |
|---|---:|---:|
| runs | 9 | 9 |
| model calls / tool calls | 249 / 323 | 459 / 488 |
| **compactions** | **0** | **0** |
| tool calls explore | 121 (37%) | 169 (35%) |
| tool calls edit | 137 (42%) | 164 (34%) |
| tool calls verify | 42 (13%) | 57 (12%) |
| tool calls other | 23 (7%) | 98 (20%) |
| **model calls explore** | **60 (24%)** | **110 (24%)** |
| model calls edit | 97 (39%) | 153 (33%) |
| model calls verify | 40 (16%) | 57 (12%) |
| model calls other | 52 (21%) | 139 (30%) |
| **mean model calls before the first edit** | **3.9 of 27.7 (14%)** | **5.2 of 51.0 (10%)** |
| input at the first call / at the last | 4,711 / 21,426 | 4,552 / 26,525 |
| input growth per call | 569 tok | 428 tok |
| input tokens (fresh + cache read) | 4.03M | 8.18M |
| of that, tool output carried forward | 1.43M (36%) | 2.99M (36%) |
| **stale: >6 calls old, never referenced again** | **310k (7.7% of input, 22% of tool output)** | **596k (7.3% of input, 20% of tool output)** |
| tool results never referenced again | 176 / 323 (54%) | 267 / 488 (55%) |

"Never referenced again" is by construction, the same notion `prune.go`'s
`lastReference` uses: the call's subject (path, pattern or command) is never
touched by a later call, *and* no line of its output over 24 characters is
quoted in any later assistant text. Tool-output tokens are estimated at four
bytes per token — provider usage numbers price a whole request, not one
block — and a result is charged once for every later model call that still
carried it, which is what the provider actually re-reads.

### The ceilings these set

- **Item 1 (context pre-selection): 24% of model calls, and realistically
  10-14%.** Exploration is a quarter of model calls on both models, but a
  turn-start injection can only reach the exploration that happens *before*
  the model commits to an edit — 3.9 calls on qwen, 5.2 on devstral, 10-14%
  of a run. And some of those are the model reading the file it is about to
  change, which is necessary work, not waste. So the honest ceiling is a
  single-digit percentage of model calls, not a quarter.
- **Item 2 (memory / skill / tool-set recall): not sized here** — see its own
  section; the recall paths are already deterministic and capped.
- **Item 3 (compaction-time pruning): zero on this evidence.** 7.3-7.7% of
  input tokens *is* dead weight, and that is a real number — but item 3 is
  allowed to act only at the compaction boundary, and **these eighteen runs
  compacted zero times**. Peak context is ~26k against a `compact_at` of
  80,000. A consumer that only fires at a boundary nothing reaches saves
  exactly nothing, whatever its accuracy. Recorded as a negative result
  below; no live consumer built.

Commands:

    janetcode --session-profile <arm>.json
    janetcode --session-profile ~/.local/share/janetcode/sessions
    janetcode --session-profile <dir> --session-profile-stale 12

Caveat: the gated set's fixtures are 2-13 files. A profile taken on a
realistically sized repository would move the exploration share up and the
before-first-edit count up with it; these numbers are the ceiling *for the
tasks the A/B actually runs*, which is also the ceiling for any A/B run on
that set.

## 2026-09-21 — Item 5: step-level annotation as the measuring instrument

The round's real product. Until now an A/B on `evals/gated` compared nine
pass/fail bits per arm, and the honest verdict every time was "the spread is
larger than the effect". The instrument replaces that with hundreds of
labelled steps per arm.

Two halves, both deterministic:

- **Phase labels** (`agent.ClassifyCall`, shipped with `--session-profile`
  above): explore / edit / verify / other per tool call and per model call.
- **Waste labels** (`agent.WasteLabels`): the three by-construction rules
  that the Python corpus builder had, ported to Go so the builder, the
  profiler and any future report cannot drift — a call whose result came
  back an error or was denied; a call inside a repeat (three identical in a
  row, or three on the same tool+path inside a window of five); and, for a
  run that did not pass, a call after its last successful change and after
  its last green gate.

`--session-profile` prints both, plus a percentile bootstrap over **runs**
(not steps — a run's own steps are not independent) for the rates an A/B is
read on. Re-reading last round's four arms through it, with no new live run:

| per run, mean [95% bootstrap], n=9 | qwen off | qwen on | devstral off | devstral on |
|---|---|---|---|---|
| model calls | 27.7 [17.8, 37.2] | 23.6 [16.6, 30.0] | 51.0 [43.0, 58.7] | 40.9 [34.3, 48.1] |
| tool calls | 35.9 [23.3, 47.2] | 32.4 [23.9, 40.6] | 54.2 [44.7, 63.2] | 47.1 [37.9, 56.7] |
| explore model calls | 6.7 [3.8, 9.7] | 5.7 [3.8, 7.6] | 12.2 [7.8, 16.8] | 8.6 [5.0, 12.4] |
| model calls before first edit | 3.9 [2.9, 4.9] | 3.6 [2.8, 4.3] | 5.2 [3.2, 8.0] | 4.0 [3.1, 5.0] |
| **wasted tool calls** | **3.9 [0.9, 7.6]** | **1.6 [0.3, 3.2]** | **6.9 [3.6, 10.9]** | **5.7 [2.7, 8.7]** |
| input tokens (k) | 447.9 [242.9, 649.9] | 345.3 [203.5, 480.8] | 909.2 [628.1, 1191.2] | 694.1 [450.8, 952.6] |
| passed | 9/9 | 7/9 | 8/9 | 7/9 |

Pooled waste share of tool calls: qwen 10.8% off (7 errored, 28 loop, 0 dead
tail) against 4.8% on; devstral 12.7% off (6/56/0) against 12.0% on. Every
interval still overlaps its partner at n=9 — the instrument does not
manufacture significance, it only says honestly how much there is. What it
adds over nine bits is that the *direction* is now consistent across six
metrics on both models, which nine bits could never show.

**Validation against Jev's own labels**: not repeated, because the previous
section already did it — `--decisions-annotate`'s `wasted` answer agrees
with these by-construction labels 63% of the time, precision 0.76, recall
0.40, twice identically. The deterministic labels are free, exact and
complete; the model's are none of those. **The instrument uses the
deterministic labels and no decisions call at all.**

**The critic, retrained.** The instrument's by-product is the training set
the repository has wanted since 2026-09-08. Feeding `--eval-train` the four
A/B arm files plus the routing arms:

    janetcode --eval-train <dir of arm json>
    trajectory critic trained on 72 runs (55 passed, 17 failed) from 8 file(s)
    cross-validated accuracy 76% (always guessing the majority: 76%)
    not saved: the critic is no better than guessing

Seventeen failing runs against the thirteen annotated sessions' ten, and a
real eval feature vector rather than one folded out of a session log — and
it still exactly ties the majority baseline, so it is still not saved. The
blocker has not moved: more failing runs. But the source of them has —
recorded A/B arms are a renewable supply, annotation is not.

## 2026-09-21 — Item 3: compaction-time pruning, negative result, nothing built

The idea: at the compaction boundary, one batched call with a noul per old
tool result — "is this still needed for the remaining work?" — and drop or
stub the ones that are not.

**The free deterministic baseline already exists and is already shipped.**
`internal/agent/prune.go` prunes exactly there and only there, for exactly
the stated reason ("rewriting old messages invalidates the cached request
prefix"), and it already orders candidates by `lastReference` — the same
by-construction notion `--session-profile` uses for its stale share: the
call's subject is never touched again. `pruneTo` archives the full output
and leaves a placeholder. So a Jev consumer here would be re-deriving, with
a model call and a latency cost, a signal the harness computes for free.

That alone is the known rule ("a deterministic signal beats Jev wherever one
exists"). But the item does not even reach that argument, because of the
ceiling:

| | qwen3.7-plus | devstral-2512 |
|---|---:|---:|
| runs | 9 | 9 |
| **compactions** | **0** | **0** |
| peak context | ~21k | ~27k |
| `compact_at` | 80,000 | 80,000 |
| stale input tokens | 310k (7.7%) | 596k (7.3%) |

The dead weight is real — 7.3-7.7% of every input token is tool output over
six calls old that nothing ever touched again, and 54-55% of tool results
are never referenced again at all. It is simply **unreachable**: the only
boundary this item is allowed to act at was crossed zero times in eighteen
runs, because the tasks end at a third of the compaction threshold.

The EPR/receipts round (2026-09-20, above) is the precedent and it failed
the same way from the other side: the mechanism worked, the saving only pays
back on later turns, and short sessions never reach them. Same regime, same
answer.

**Status: negative result. No consumer, no flag, no corpus.** What would
change it: a task set whose runs actually compact — long-horizon sessions,
not a stricter gate. If that arrives, the first thing to measure is not Jev
but whether `pruneTo`'s existing `lastReference` ordering already recovers
the 7.5%, since it is free.

## 2026-09-21 — Item 2: memory, skill and tool-set recall, skipped on measurement

The instruction was to find the existing recall code first and, if selection
is already deterministic and good, say so and skip. It is, so this says so.

**Memory.** `(*Store).rank` scores name and description words +3, body words
+1, matched `Concerns` path and symbol segments +2 capped at 2, tie-broken
by an activation decay; `Recall` admits only scores of 4 or more, keeps at
most `MaxRecall = 4` entries inside a `RecallBudget` of 6 KB. Not
"everything up to a budget" — an admission threshold, a hard top-N and a
byte cap, all three.

**Skills.** `Select` scores a name substring +10, each declared keyword +5,
each shared non-stop word in the description or a heading +1, admits at
`score >= 3`, caps at `MaxSelected = 5` and `MaxBody = 16 KB`, and skips a
skill re-attached within the last six turns.

**Tool sets.** The recoverable narrowing this item asks for is already
built and has been for months: `Registry.AddDeferred` hides a tool from
`Defs` while `Registry.Get` still resolves it, so a model that calls a
deferred tool by name **gets the tool**, and `tool_search` reveals more on
request. Narrowing is therefore safe by construction. It is also the one
place with measurable bytes: tool schemas are ~14.3 KB against a first
request of 4,552-4,711 input tokens. But this repository has already paid
for cutting the prompt on a guess — the 2026-09-11 leanness round reverted a
440-byte cut to the harness section after it cost a pass and +7% tool calls
on evals 01-10, and recorded that "the prompt is exactly as sensitive as the
benchmarks say". A Jev call choosing which tools to hide is that cut with a
model picking the bytes.

**And there is no corpus and no ceiling.** The eval agent
(`internal/minimal`) wires neither the skill injector nor the memory store —
`ag.Prepare` is the repo map alone — so not one of the 72 recorded A/B runs
selected a single memory or skill. There is nothing to score a consumer
against, and no measurable waste to recover.

**Status: skipped on measurement, as instructed.** Revisit only with a task
set that actually loads memories and skills, and only after measuring the
deterministic selectors' own precision on it.

## 2026-09-21 — Item 4: ask-before-working, blocked for want of a corpus

The consumer is easy to write; the ground truth does not exist here.

The detector was defined deterministically first, as required: a
"should have asked" is a *follow-up human turn* (not a synthetic
harness-written one) that matches a correction vocabulary — `no,`, `not
what`, `I meant`, `actually`, `instead`, `revert`, `undo that`, `that's
wrong`, `don't`, `stop`, `wrong file`, `I said` — and that follows a turn
which made at least one edit. Direction was changed after work was done: by
construction, the question should have been asked first.

Run over every recorded session in the local store:

| | count |
|---|---:|
| session files | 35 |
| sessions with a real human prompt | 9 |
| **multi-turn human sessions** | **4** |
| follow-up human turns (the labellable population) | 4 |
| **redirects after an edit** | **0** |

Four labellable turns and zero positives. That is not a weak corpus, it is
no corpus: the eval sessions are single-prompt by construction, and the
handful of real sessions in this store were driven by an operator who does
not redirect mid-task.

A hand-built corpus was considered and rejected. With zero observed
positives there is nothing for invented rows to be validated *against* —
they would define the phenomenon rather than sample it, and the replay
against them would score the author's imagination. Shipping log-only on that
basis would put an unvalidated live call on the intake path for no
measurement, which is the failure mode this whole round exists to avoid.

**Status: blocked. Exact missing input: recorded multi-turn sessions with a
human who changes direction** — twenty or so follow-up turns with a handful
of positives would be enough to score a detector. Nothing built, no flag, no
corpus file.

## 2026-09-21 — Item 6: escalation routing, surveyed and scoped, not built

Feasibility first, as instructed, and the answer is better than expected:
**mid-turn model switching already works.** `(*Agent).call` asks
`routeStep` on every model call, so the tier is chosen per call, not per
turn. There is no missing hook.

What is missing is the direction. Today's `route_cheap` is default-first: it
starts on the strong model and dips to the cheap tier for a *mechanical*
step (`read_file`, `list_dir`, `glob`, `grep`, `repo_map`, or a green
`shell.IsCheck` bash), and `stepRoute.taint` ends the dip at the first edit,
failure or warning. Item 6 wants cheap-first with escalation on struggle.
The deterministic struggle signals it would escalate on all already exist
and are already counted: `max_repeat` (the loop counter the waste labeller
reads), the tool-error streak, `turnGates`' repair-round counters, and
`StaleCheck`. Jev would be a tiebreaker on a silent turn, which is the
narrowest role on this whole list.

**The blocker is a number, not a hook.** `routeCheapSmallContext = 20_000`
refuses cross-provider routing above 20k tokens, precisely because a model
switch costs a cold prompt cache. The Step 0 profile says a gated run's
input reaches 21.4k (qwen) to 26.5k (devstral) by its last call, growing
428-569 tokens a call. So a cheap-first turn would be forced back to the
strong model around call 30 on its own, with a cache miss at the switch, and
the escalation policy would decide nothing for most of the run. Raising the
ceiling is a separate change with its own cache-cost A/B.

The priority argument also holds against it. qwen3.7-plus cost $0.0296 for
nine gated runs; the dollars are already noise, so the only currency is wall
time — and wall time is what a cold cache at every escalation spends.

**Status: scoped, not built.** The next step, if revisited, is the
cross-provider context ceiling and its cache cost, measured on its own,
before any decision consumer is written. Nothing shipped, no flag.

## 2026-09-21 — Item 1: context pre-selection, and why the eval set cannot judge it

The consumer: one Jev call at turn start, concurrent with the intake
request, a noul per candidate file from the deterministic repo-map ranker's
top N ("is this file needed to fulfil the request?"), and the top-k injected
as **pointers** — path plus a reason — not contents.

Step 0 gives the ceiling: exploration is 24% of model calls on both models,
but a turn-start injection can only reach the part that happens before the
model commits to an edit, and that is 3.9 calls of 27.7 on qwen (14%) and
5.2 of 51.0 on devstral (10%). Some of those calls are the model reading the
file it is about to change, which is the work, not waste.

Then the fixtures answer the question before any ranker is scored. Over the
same eighteen off-arm runs, counting the distinct files each task's runs
actually read or edited against the fixture's file count:

| task | fixture files | distinct files touched | share of the fixture |
|---|---:|---:|---:|
| 71-gated-go-service | 13 | 10-11 | 77-85% |
| 72-gated-python-ledger | 9 | 7 | 78% |
| 73-gated-expr-layers | 2 | 5 (creates 3) | the whole thing |
| 74/75/76-routed | 2-4 | 1-2 | the whole thing |

**A run touches four fifths of its repository.** The correct answer to "is
this file needed?" is *yes* for almost every candidate, so any ranker — the
existing deterministic one, a Jev rerank of it, or a coin that always says
yes — scores near-perfect recall and near-perfect precision, and none of
them can be told apart. There is no deterministic baseline to beat because
the baseline is already at the ceiling. That is not a property of the
ranker; it is a property of a 2-to-13-file fixture.

So the measurement was moved to the one realistically sized fixture this
repository has, `evals/explore/61-explore-large-repo` — **59 files, and a
task whose answer is exactly two of them** (a constant in `billing/limits.go`
and the one file that hard-codes its value, `report/audit.go`). That is the
regime where pre-selection is worth something: recall of 2 in 59, where
precision is the costly side.

(Baseline run and its numbers: see the section below.)

**Status at this point: not built, and correctly so.** The evidence gate
says corpus, then replay, then two scored runs, then beat the deterministic
baseline. On the gated and routing sets the gate cannot even be reached,
because the corpus that falls out of them is degenerate. The costly error
this item was warned about — confidently pointing at the wrong files and
biasing the model — is unmeasurable on a fixture where every file is right.

## 2026-09-21 — The A/B's own confound, checked: two of the five "on" flags were inert

Before re-running anything, the previous round's arm definition was
verified against the code rather than against its own description. It does
not survive.

"Fully on" was recorded as `triage` + `intake` + `intake_route` +
`session_outcome` + `stale_check_nudge`. Two of those five could not have
been on:

- **`stale_check_nudge` is not wired into the eval agent at all.** It is set
  in exactly one place, `agent_build.go:419`, from `cfg.StaleCheckNudge`.
  Neither `internal/minimal` (the eval agent) nor `eval_gate.go` sets
  `ag.StaleCheckNudge`, and neither scratch config even contained the key.
  It was off in **both** arms of **both** models.
- **`session_outcome` fires only at session end in headless**, which an eval
  run never reaches — already noted last round as "0, not reached in eval".

So the thing the on arm actually measured was `triage` + `intake` +
`intake_route`, and the difference between the arms is attributable to those
three and nothing else. The good news is that the confound the re-run was
supposed to remove — a deterministic non-Jev flag differing between arms —
**never existed**: the deterministic flag was equally off on both sides. The
bad news is that a flag was reported as part of a measured configuration
without anyone checking that the harness could turn it on.

The lesson generalises past this flag: an arm definition is a claim about
the code, and it needs the same verification as a result. `--session-profile`
now makes the cheap version of that check possible — an inert consumer fires
zero times, and a per-consumer fire count of zero beside a flag that is
supposed to be on is the tell.
