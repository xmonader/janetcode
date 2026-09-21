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
