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
