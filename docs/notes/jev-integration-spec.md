# Jev integration spec: typed decisions, fail-open (2026-09-20)

Companion to `docs/notes/jev.md` (why Jev, what survives revision). This
is the integration contract: the package boundary, the availability
model, the consumers in order, and the measurement gates between them.

Status: proposed. Nothing in this file ships until Phase 0's offline
calibration replay passes.

## Design rules (the whole spec in five lines)

1. **Go is sovereign.** Jev advises; deterministic code decides. Exit
   codes, shell classification, tool Guards, confinement and permissions
   never read a Jev answer.
2. **Fail-open to today.** Every Jev call site behaves exactly as it
   does now when Jev is absent, disabled, slow, or wrong-looking.
3. **No live consumer before offline evidence.** Each consumer earns its
   place on recorded sessions before it touches a live turn.
4. **Advisory and typed.** Jev never generates text janetcode shows the
   user; it answers questions janetcode phrased, and janetcode renders.
5. **Off by default, cheap when on.** Opt-in config; a session without
   Jev configured is byte-identical in behaviour to today's.

## Package boundary

```
internal/jev            — client, types, availability. Deps: llm (Retrier), nothing else.
internal/agent/jevtriage.go   — verify-failure triage consumer (Phase 1)
internal/agent/jevcritic.go   — trajectory second signal (Phase 2)
internal/agent/jevintake.go   — intake classification (Phase 3)
```

`internal/jev` must not import `agent` (archtest rule: `agent` may
depend on `jev`, not the reverse; add the pair to
`internal/archtest/deps_test.go`). Config keys follow the existing
`boolPtr` pattern in `internal/config/set.go`.

### Types

```go
type Question struct {
    ID           string   // stable, snake_case; logged
    Instructions string   // the actual question text
    Kind         Kind     // Noul | Choice | Score
    Options      []Option // Choice/Score: id + description
}

type Answer struct {
    ID           string
    Probability  float64            // Noul: P(true)
    Choice       string             // Choice/Score: selected option id
    Distribution map[string]float64 // Choice/Score: full spread
    Confidence   float64            // TypeSafe's margin statistic
}

type DecisionProvider interface {
    // Evaluate answers every question against one state, in one call.
    // Errors are always soft: callers must treat any error as "no
    // opinion" and proceed exactly as if Jev were not configured.
    Evaluate(ctx context.Context, state State, qs []Question) ([]Answer, Meta, error)
    Available(ctx context.Context) bool // cheap; cached probe result
}

type Meta struct {
    Model    string        // versioned id from the response (jev-1.13.0), never the alias
    Latency  time.Duration
    TokensIn int
}
```

`State` is a deliberately small struct per consumer (below), rendered to
text by the consumer, not by the client — the client never sees agent
internals.

## Availability model (works when Jev is not there)

Availability is a ladder; every rung is a clean no-op:

| rung | condition | behaviour |
|---|---|---|
| 0 | no `decisions` config block | feature invisible; zero code path difference |
| 1 | configured, no API key | one startup notice ("decisions: configured but no key; off"), off for session |
| 2 | probe fails at startup | off for session, one notice; `--doctor` row explains |
| 3 | call error mid-session | caller proceeds; error counter increments |
| 4 | 3 consecutive call errors, or p50 latency > 1s over 10 calls | circuit opens; off for session, one `EvNotice` naming why |
| 5 | circuit open | no further calls this session; next session retries from rung 2 |

Implementation notes:

- Probe at startup is one minimal Evaluate (one Noul, empty-ish state),
  2s timeout, run concurrently with the existing provider checks so it
  adds no wall time. Its result is cached for the session.
- Every call carries a context deadline (default 800ms, config
  `decisions.timeout_ms`, ceiling 2s). A timeout is a soft error like
  any other.
- Retries: none on the live path. Jev's value is cheap immediacy; a
  retried decision is a late decision. The offline annotation tool
  retries via `llm.Retrier` because it is not on a turn's critical path.
- `JANETCODE_OFF` / `--eval-off` / `--off` gain the switch `decisions`
  (add to `switches` in `internal/agent/stats.go`, `KnownSwitch`,
  `ParseSwitches`). Evals default to **off** unless a task explicitly
  enables them, keeping every published benchmark comparable.
- Headless `--output-format json` sessions get the same behaviour; the
  result envelope gains a `decisions` block (see Observability).
- The feature catalogue needs an entry (`internal/feature`,
  `TestEveryCommandIsCatalogued` will enforce it) and `docs/manual`
  gains a section, gated by `TestEveryFeatureIsInTheManual`.

## Consumers

### Phase 0 — offline annotation (no live path at all)

A subcommand-ish tool (shape TBD: `harnessbench --annotate` or
`janetcode --decisions-replay <sessions>`) that replays recorded session
logs through candidate question sets and writes labeled JSONL beside
`docs/benchmarks/`. Purpose, in order:

1. **Calibration check.** Bin predicted probabilities against outcomes
   we can compute from the logs (gate eventually passed, turn count,
   verdict). If 0.8-confidence answers are not ~80% right on our own
   data, stop; nothing live ships.
2. **Question-set selection.** The question sets below are hypotheses;
   replay picks the ones whose answers correlate with outcomes.
3. **Critic training data.** Labels feed `internal/critic`'s predictor,
   which currently cannot train for lack of failures — Jev's most
   durable contribution may be a better local, free, offline model.

### Phase 1 — verify-failure triage

Hook point: `internal/agent/gate.go`, inside `verifyGate`, after
`res.OK() == false` and before `a.appendHarnessText(res.Feedback())`.

State (rendered text, capped ~2 KB): failing command, bounded head+tail
of its output (reuse `internal/verify`'s capture), one-paragraph summary
of the turn's changed files (names + hunk counts from provenance, not
diffs — diffs blow the context and hit the counting weakness), the
user's original request.

Questions:

| id | kind | text |
|---|---|---|
| `caused_by_change` | Noul | "Is this failure plausibly caused by the change just made?" |
| `failure_class` | Choice | environmental / flaky / unrelated / patch / inconclusive |
| `worth_repair` | Noul | "Is another automated repair attempt likely to fix this?" |

Action mapping (deterministic rules over the answers, logged):

- `caused_by_change` ≥ 0.7 → proceed exactly as today (feedback to
  model), plus a one-line `EvNotice` with the class.
- `caused_by_change` ≤ 0.3 **and** `failure_class` ∈ {environmental,
  flaky, unrelated} with confidence ≥ 0.7 → do **not** send the model
  back; emit a notice naming the class and the probability, count the
  round against `verify.MaxRounds` anyway (a skipped repair is still a
  spent round — this keeps the fail-open guarantee: the worst case of a
  wrong answer is today's behaviour minus one round).
- anything in between → today's behaviour, notice only.

Explicitly not done: reinterpreting exit codes, suppressing the real
failure output, blocking the turn from ending.

### Phase 2 — trajectory second signal

Hook point: `internal/agent/trajectory.go` `critique()`, same cadence
(every 5 tool calls), alongside — not instead of — the fitted
`Predictor`.

State: the user's goal, the todo plan if any, counts the harness already
has (edits, failed edits, gate failures — computed in Go, never asked of
Jev), last 3 tool call names+statuses, critic score if present.

Questions: `on_goal` (Noul), `stuck` (Noul), `recommended` (Choice:
continue / compact / ask_user / stop), `success_likelihood` (Score 1–5
with described rungs).

Action: the existing warning fires when the local predictor says so
(unchanged). Jev's answers are logged and shown only when they
*disagree* with the local signal — that disagreement is the information.
If replay shows Jev's `stuck` probability predicts failed sessions
better than the predictor's score, a later phase may let a high
`stuck` + low local score fire the warning.

### Phase 3 — intake classification

Hook point: first user prompt of a session, before the first model call
(runs concurrently with first-turn preparation; never blocks it past
the deadline).

Questions: `kind` (Choice: explain / implement / debug / review /
docs / other), `complexity` (Score 1–5), `needs_plan` (Noul).

Action (phase 3a): log only. Action (3b, only after replay shows value):
feed `needs_plan` ≥ 0.8 into the todo hint, and record kind/complexity
in the session meta for later model-routing analysis. Model routing
itself stays on `route_cheap`'s deterministic rules — Jev intake data
informs *future* rules, not live ones.

## Configuration surface

```jsonc
{
  "decisions": {
    "provider": "typesafe",        // or a gateway URL
    "api_key_env": "TYPESAFE_API_KEY",
    "model": "jev-1.13.0",         // pin a version; aliases rejected with a config error
    "timeout_ms": 800,
    "triage": true,                // per-consumer toggles, all default false
    "trajectory": false,
    "intake": false
  }
}
```

Rules: unconfigured = absent (rung 0). Consumers all default off even
when `decisions` is configured — the block enables the *client*, each
consumer is opted into. `/doctor` gains a `decisions` row (configured?
key present? probe result? circuit state). `JANETCODE_OFF=decisions`
kills everything regardless of config.

## Observability

- New session event type `decision` (alongside `critic/score`,
  `verify`): Extra carries `{consumer, questions, answers, meta}` —
  model version, latency, tokens, confidences. Sleep consolidation
  (`sleepfold`) lists it under facts but never lets it become a memory
  entry verbatim.
- `--output-format json` result gains `decisions: {calls, errors,
  timeouts, circuit_open, total_latency_ms}` mirroring the existing
  `recoveries` block.
- `/stats` gains one line when active: `decisions: N calls · M errors ·
  avg Xms`.
- The verdict fold (`FoldOutcomeIn`) does **not** count a triage
  decision as a check; the gate's own events remain the record.

## Privacy and safety

- State sent to TypeSafe contains: command lines, bounded command
  output, file names, the user's prompt. It must pass through the same
  credential scrubbing the bash tool applies to the environment
  (`ScrubEnv` patterns) before rendering, and the memory store's
  secret regexes (`internal/memory/secret.go`) run over the rendered
  state; a hit truncates the offending field to `[redacted]`.
- Diffs are never sent (size + counting weakness); provenance summaries
  are.
- This is a third-party call from the turn loop, which today makes none
  except the model provider. `--doctor` must say so plainly, and the
  config template's comment must name it.
- Jev answers are never shown as janetcode's own confidence; UI strings
  say "decisions model says …" or stay silent.

## Testing

- All offline against a fake server, per repo convention: canned
  Evaluate responses, injected timeouts, a flapping server for the
  circuit-breaker rungs, a slow server for the deadline.
- The key invariant tests: **with the fake server returning garbage,
  errors, and silence, every consumer's behaviour equals today's** —
  the same assertions as the existing gate/trajectory tests with Jev
  enabled-but-broken.
- Threshold mapping table is table-driven and pure; tested
  exhaustively (it is 9 lines of Go).
- Race detector over the circuit breaker; `TestEvalDoesNotAllocate`
  must stay green with decisions off.

## Gates between phases

| transition | requirement |
|---|---|
| → Phase 1 | replay: calibration curve on recorded gate failures is monotone; `caused_by_change` agrees with the post-hoc truth (did a later edit fix it?) ≥ 70% |
| Phase 1 on by default | evals 51–62, ≥5 runs per side, `--eval-compare`: pass rate not worse, repair calls per solved task down |
| → Phase 2 | replay: `stuck` separates failed from clean sessions better than chance on the session set |
| → Phase 3 | replay: intake `kind` agrees with human labels on a hand-labeled sample of 50 real prompts |

Any gate failing → the consumer stays off and the note in
`docs/notes/jev.md` is updated with the numbers. The feature is
removable: each consumer is one file behind one config flag, and
deleting `internal/jev` must leave the tree building.

## Explicitly out of scope

- Live model routing by Jev (steproute stays deterministic).
- Permission and shell-safety decisions (advisory warnings only, and
  only after Phases 1–2 have shown calibration).
- Anything during eval benchmarks by default (`decisions` off).
- Windows-specific work beyond compile-clean; the client is plain HTTP.
