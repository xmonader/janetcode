# janetcode against pi on hero_agent's effbench

hero_agent's effbench is the benchmark its herox agent was measured on, on
2026-10-08, against pi, Claude Code, Qwen Code, Hermes and OpenClaw. `run.py`
here imports it unchanged and adds a janetcode arm. The janetcode arm is built
like effbench's competitor arms:

- a throwaway `HOME`;
- its own metering proxy;
- every other credential scrubbed from the environment.

So janetcode is seeded, graded by the hidden tests, checked for tampering and
cheating, and metered by the same code that measured herox and pi.
`compare.py` prints hero_agent's table format.

- **Model:** `XiaomiMiMo/MiMo-V2.6-Flash-RL` on the ai02 broker (effbench's
  `mimo` config, 131k context), the same model herox's MiMo row used.
- **Tasks:** effbench's default subset: 8 tasks × 2 seeds, a 20-minute cap
  per task.
- **Arms:** run side by side in one campaign, so they see the same broker
  load.
- **pi:** effbench's own build of the latest pi
  (`~/.cache/effbench/pi-eb326d265`), with `-nc -ns` and pi's defaults.

## Inkling 2: reasoning replay per model; the unverified-edits note dropped (2026-11-27)

**janetcode against pi 1.6.1** (`h2h-ink-small`, `h2h-ink-mini`, bin/janetcode-h2h4, 8 tasks × 3 seeds,
via Together and PrimeIntellect):

| model | arm | pass | calls/run | input/run | output/run | cost/run |
|---|---|---|---|---|---|---|
| inkling-2-small | janetcode | 23/24 | 15.4 | 281k | 35.0k | $0.052 |
| | pi | 23/24 | 15.0 | 266k | 18.2k | $0.026 |
| inkling-2-mini | janetcode | 21/24 | 19.1 | 331k | 77.6k | $0.040 |
| | pi | 22/24 | 18.3 | 440k | 27.1k | $0.018 |

This is the first model on which pi is cheaper. Without its earlier reasoning,
inkling re-derives its plan on the next call: janetcode's first two calls
reasoned for about 10k tokens each, against about 10 for pi's. All three
janetcode losses on mini were d-cronexpr hitting the 1200 s timeout.

**Replay on inkling-2-small** (`ink-replay`, bin/janetcode-h2h5):

| arm | pass | calls/run | input/run | output/run | cost/run |
|---|---|---|---|---|---|
| drop | 24/24 | 19.6 | 402k | 41.7k | $0.064 |
| replay | 24/24 | **13.1** | **335k** | **24.3k** | **$0.044** |

Replay cut calls by 33%, output by 42% and cost by 31%. 86aa7b07 makes replay
the default for `inkling*` models, and the `replay_models` setting adds more
globs; every other model still drops reasoning. pi stays cheaper on inkling
($0.026), so a gap remains.

**Closing the rest of the gap, two things tried** (`ink-inline`, `ink-cap`, bin/janetcode-h2h6, which replays by default for inkling):

| campaign | arm | pass | calls/run | input/run | output/run | cost/run |
|---|---|---|---|---|---|---|
| ink-inline | janetcode | 24/24 | 14.1 | 372k | 24.7k | $0.049 |
| | no attached repo (`JANETCODE_OFF=inline`) | 24/24 | 20.8 | 456k | 25.7k | $0.054 |
| | pi | 24/24 | 16.3 | 291k | 18.0k | $0.033 |
| ink-cap | full replay | 24/24 | 15.1 | 413k | 24.4k | $0.047 |
| | `reasoning_replay_chars=2000` | 24/24 | 16.0 | 329k | 31.1k | $0.048 |
| | `reasoning_replay_chars=6000` | 24/24 | 15.5 | 340k | 29.1k | $0.048 |

- **The attached repository stays on.** Without it, the first call reasons for
  about 5 tokens instead of 8–14k, but the run makes 48% more calls and costs
  more.
- **Capping replayed reasoning does not pay.** It trades input for output at
  the same cost. Full replay stays.
- **What remains is structural.** janetcode's prompt averages 26k tokens against
  pi's 18k, since the attached repository (about 5.5k tokens) rides on every
  call. inkling also writes more output under janetcode. On this model
  janetcode costs about 45% more than pi for the same passes, and it is the
  only model measured where that is true.

**The unverified-edits note** (3ce604ca, reverted in cd958f22): one note when a
headless turn ended on edits with no check after them.

- `unv-oss` (gpt-oss, 8 × 5): on 5/40 at 314k input/run, off 6/40 at 240k. It
  fired in 16 of 40 runs and turned none of them into a pass.
- `unv-kimi`: it never fired (24/24 and 23/24).

## gpt-oss-20b: surviving tool calls the provider refuses (`h2h-oss`, `oss-replay`, `oss-toolfix`, `oss-toolfix2`, 2026-11-26)

On ai02, gpt-oss-20b is served via OpenRouter by Groq, which checks every tool
call against its schema and refuses the bad ones with an HTTP 500 mid-stream.

- **Before:** in `h2h-oss` (bin/janetcode-h2h2), the refusals ended 11 of 24
  runs. 9 of them were `apply_patch` called with `patch` instead of `input`.
- **pi 1.6.1 on the same model:** 0/24, with 1.7k output tokens per run.
- **Replay does not help this model either** (`oss-replay`): drop 4/24 at
  108k input against replay 3/24 at 188k.

Fixes:

- f1634dc5: `apply_patch` takes `patch` as well as `input`.
- cfac638e: a refused call becomes a harness note and the model tries again, at
  most 3 times per turn.
- e87f7107: `bash` takes `cmd` as an alias, renamed before the permission
  checks, and the note lists the real tools when the model calls one from its
  training (`search`, `open_file`).

`oss-toolfix2`, 8 tasks × 5 seeds, same window:

| arm | pass | runs ended by a refusal | refusals recovered | calls/run | input/run | cost/run |
|---|---|---|---|---|---|---|
| old (h2h2) | 3/40 | 9 | 0 | 14.4 | 169k | $0.0122 |
| new (h2h4) | **8/40** | 5 | 39 | 16.9 | 191k | **$0.0112** |

The pass gain is near the noise floor, but the mechanism is direct: 39 calls
that used to end a run were recovered. The remaining run-ending refusals are
turns that used all 3 recoveries.

## janetcode against the latest pi (`h2h-ds`, `h2h-kimi`, 2026-11-26)

The setup:

- **janetcode:** bin/janetcode-h2h1 (83d375cc), which no longer replays reasoning and retries gateway errors for up to 3 minutes.
- **pi:** 1.6.1, built from pi bf3f6a222.
- **Runs:** 8 tasks × 3 seeds per model.

On Kimi, the broker's key pool ran dry for the last seed. Every request returned
429 ("all API keys for provider kimi are unavailable"), so the six seed-3 runs of
each arm made no progress, and the table counts live runs only. That window led
to acd35fb8: a 429 that names its retry window now spends the 3-minute budget
instead of two retries.

| model | arm | pass | calls/run | input/run | uncached/run | output/run | cost/run | wall/run |
|---|---|---|---|---|---|---|---|---|
| deepseek-v4.1-flash | janetcode | 24/24 | **10.2** | **133k** | **19k** | 22.8k | **$0.017** | **142 s** |
| | pi | 24/24 | 16.6 | 349k | 31k | 24.0k | $0.023 | 191 s |
| kimi-for-coding | janetcode | 18/18 | **9.8** | **142k** | **17k** | 23.0k | – | 175 s |
| | pi | 17/18 | 14.3 | 257k | 26k | 20.2k | – | 178 s |

Against pi, janetcode passes as much or more, with:

- 39% fewer calls on DeepSeek and 31% fewer on Kimi;
- 62% less input on DeepSeek and 45% less on Kimi;
- 26% lower cost on DeepSeek.

## Reasoning replay costs input and buys nothing (`rb-ds`, `rb-kimi`, 2026-11-26)

Same binary (bin/janetcode-rb1, 67c80b7d), 8 tasks × 3 seeds, four arms:

- `full`: replay all earlier reasoning (the old default).
- `chars`: `set.reasoning_replay_chars=1500`.
- `last`: `set.reasoning_replay_last=3`.
- `off`: `JANETCODE_OFF=reasoning`.

A broker 502 burst killed 112 runs before their first call. Those were deleted
and rerun on the same binary. That burst led to caa56ca2, which retries
gateway errors for up to 3 minutes.

| model | arm | pass | calls | input/run | uncached/run | output/run | cost |
|---|---|---|---|---|---|---|---|
| deepseek-v4.1-flash | full | 24/24 | 11.8 | 334k | 62k | 30.9k | $0.69 |
| | chars | 21/24 | 11.0 | 185k | 22k | 21.7k | $0.45 |
| | last | 24/24 | 11.2 | 186k | 75k | 25.3k | $0.63 |
| | off | 24/24 | 10.2 | **124k** | 23k | 29.7k | $0.56 |
| kimi-for-coding | full | 24/24 | 8.2 | 157k | 30k | 17.5k | – |
| | chars | 24/24 | 9.5 | 136k | 21k | 22.0k | – |
| | last | 24/24 | 8.7 | 134k | 46k | 19.2k | – |
| | off | 23/24 | 8.7 | **112k** | 19k | 17.7k | – |

- **No replay compared with full replay:** the same passes within noise, with 63% less input on DeepSeek and 29% less on Kimi. With GLM's 448k vs 280k (`syn-glm-replay`), that makes three models. The earlier DeepSeek gap (`syn-ds-replay`, 22/24 vs 24/24) was noise.
- **`last` breaks the cache:** each call strips one older message, so its uncached input is the highest of the four arms.
- **`chars` is cache-safe but saves less than `off`.**

**Decision:** stop replaying by default. Replay only when the provider rejects a request for missing reasoning.

## gpt-oss-20b: cache misses are the broker's, not janetcode's (`cache1`, 2026-11-26)

The first campaign with the miss attribution of 8af95ba7 (`cache_misses` in the
headless result, `own_cache_misses` in the run record): one arm, 8 tasks × 2
seeds, on ai02. Five runs died on a broker 502 before their first call, and two
passed. Of the 11 runs with calls, 70% of the input was read from cache.

None of the 531k missed tokens was a prompt janetcode changed. 288k were
requests that only appended to the previous one, and 243k were runs where the
endpoint reported no cache read at all. The proxy logs show the pattern:

- When the cache serves, a run reads 95–97% of each prompt (b-promo-clock 97%
  and 95%, b-report-paging 91%), and each call misses only 1–16 tokens, which
  is block rounding.
- Other runs get calls with zero cache reads scattered between hits
  (c-rs-logstore-s2: 14 of 37). That is a request landing on a replica without
  the prefix.

janetcode already sends a per-session `prompt_cache_key` (internal/llm/openai),
and the broker does not route on it. The janetcode-vs-pi cache gap on this
broker is therefore routing noise, and there is no prompt-side fix to make.

## gpt-oss-20b: apply_patch pays; edit escalation still unmeasured (`bob-oss-edit`, 2026-11-26)

Binary `6e04eb0c`, `openai/gpt-oss-20b` routed through ai02 (a weak model, picked so
edit misses are common), the 8 default tasks x 3 seeds, three arms in one window,
$0.55 in all.

| arm | switches off | pass | calls/run | input/run | cost |
|-----|--------------|------|-----------|-----------|------|
| on      | none                       | 9/24 | 18.3 | 261k | $0.164 |
| noedit  | `editescalate,pathrepair`  | 2/24 | 17.9 | 251k | $0.168 |
| nopatch | `applypatch`               | 4/24 | 21.9 | 342k | $0.218 |

**apply_patch: kept.** Offered, the model edits almost only through it (232 calls
in `on`, two `edit_file` misses); without it, 66 `edit_file` misses, 20% more calls
and 31% more input for fewer passes. The 76 unmatched hunks in `on` are retried
in the same call budget.

**editescalate / pathrepair: not attributable.** In `on` the escalation never
fired (one closest-line hint): with apply_patch on offer the model barely uses
`edit_file`. In `nopatch` it fired (8 escalations, 5 closest-line hints, 3
already-applied notes), but that arm has no control without it. No call named a
`~` or `home/...` path. So the 9 vs 2 between `on` and `noedit` comes from switches
that hardly ran, and at ~20% pass rates is within what two identical arms can
differ by.

**The rerun settles it (`bob-oss-edit2`, same binary, a later window):** `on` 3/24
(15.9 calls, 220k), `on2` (the identical arm) 5/24 (15.8, 226k), `noedit` 3/24
(15.7, 229k). Two copies of one binary differ more than `on` and `noedit`, and `on`
itself fell from 9/24 to 3/24 between windows: on gpt-oss-20b a pass gap under
~6 of 24 is noise, and so is the 9 vs 2 above. The escalation fired once in 72
runs. editescalate and pathrepair stay on as idle-free guards for `edit_file`
users; they remain unmeasured. apply_patch's pass gain (9 vs 4) is inside that
noise too; what stands is the mechanism (66 `edit_file` misses without it, 2
with it) and its lower calls and input.

## kimi-for-coding: edit escalation and path repair (`bob-kimi-edit`, 2026-11-23, kept, unmeasured)

Binary `d6eef258`, the 8 default tasks x 3 seeds, one window; arm `off` is the
same binary with `JANETCODE_OFF=editescalate,pathrepair`.

| arm | pass | calls/run | input/run | wall/run |
|-----|------|-----------|-----------|----------|
| on  | 22/24 | 12.8 | 237k | 304 s |
| off | 24/24 | 12.7 | 224k | 277 s |

Neither switch fired. The whole campaign had two edit misses (one per arm,
`a-catalog-paging`), both first misses that the existing near-miss diff already
answered, so the closest-line hint and the second-miss escalation never ran; no
call named a `~`, `home/...` or doubled-workspace path for path repair to fix.
The two `on` failures are wrong logic caught by hidden tests
(`a-build-incremental` missing-source plan, `d-cronexpr` steps and names), not
edits or paths. So this is the same binary twice, within the 2/15 self-noise
measured before. Kimi rarely misses an edit or a path on these tasks: the switches
cost nothing when idle and stay on, aimed at weaker models. Measuring them needs
a model that misses more, or tasks built to provoke misses.

## glm-4.7: janetcode against pi, Kimi Code, Qwen Code and Claude Code (`syn-glm-agents`, `syn-glm-claudecode`, 2026-11-12)

Every agent ran glm-4.7 on the Z.ai coding plan: 8 synthetic tasks × 3
seeds, a 20-minute cap per task. Janetcode was `bin/janetcode-m10` with its
defaults. Claude Code went through Z.ai's Anthropic endpoint, so it ran in a
campaign of its own (`syn-glm-claudecode`) on the same 24 task-seeds. The
other four ran side by side in `syn-glm-agents`.

| agent | passed | wall/run | median | calls/run | input/run | hit | uncached/run | output/run |
|---|---|---|---|---|---|---|---|---|
| janetcode | **24/24** | 625 s | 547 s | **12.6** | 436k | 89% | 49.2k | 35.0k |
| pi | 23/24 | **472 s** | **469 s** | 19.2 | **410k** | 92% | **32.8k** | **24.5k** |
| Kimi Code | 24/24 | 629 s | 673 s | 21.7 | 848k | 94% | 52.9k | 30.3k |
| Qwen Code | 18/24 | 743 s | 628 s | 20.0 | 742k | 94% | 45.9k | 34.2k |
| Claude Code | 23/24 | 628 s | 562 s | 26.3 | 922k | 93% | 62.9k | 29.1k |

- **Failures:**
  - **pi:** `d-todocli` seed 2.
  - **Claude Code:** `a-build-incremental` seed 3, which ran into the cap.
  - **Qwen Code:** all six `d-cronexpr` and `d-todocli` runs. Three of them
    ran into the cap after five calls of about 50k output tokens each.
- **The tamper flag:** effbench flagged Qwen Code's `b-report-paging` seed 3
  run as tampered. It added a `test_contract.py` of its own, which the
  hash of guarded `test_*.py` files counts as a change. The hidden tests
  passed, so this run counts as a pass.
- **Janetcode against pi:** janetcode is the only agent besides Kimi Code to
  pass all 24, and it makes the fewest calls by a third. pi sends 6% less
  input, a third less uncached input and output, and finishes 25% sooner.
  So janetcode's lead is reliability and pi's lead is still the cheap start
  described below.
- **The other three** send 1.7–2.1× janetcode's input for no more passes.

## glm-4.7: the rules-first line pays here (`syn-glm-rules`, 2026-11-12, kept)

The same binary (`bin/janetcode-m10`) ran with its defaults and with
`JANETCODE_OFF=rulesfirst`, 8 synthetic tasks × 3 seeds. The default arm's
last 10 runs died when the disk filled up for a while at 15:10. They were
rerun in the same campaign, and effbench skipped the runs that had already
finished.

| arm | passed | wall/run | median | calls/run | input/run | hit | uncached/run | output/run |
|---|---|---|---|---|---|---|---|---|
| default | **24/24** | **484 s** | 523 s | **12.0** | **369k** | 87% | **47.4k** | **29.5k** |
| no rules-first | 23/24 | 547 s | 469 s | 13.2 | 438k | 89% | 48.2k | 32.1k |

Without the line the model sends 19% more input and fails one more run
(`d-cronexpr` seed 2). On DeepSeek the same switch saved 23% of input for
one fewer pass (`syn-ds-start`). The line stays in the default. Its effect
depends on the model, so the gap to pi on GLM has to close somewhere else.

## Where pi's leverage is (source of the benchmarked build `eb326d265`, and the GLM logs)

**Nothing in pi's harness is clever.** The system prompt is 2.8 KB: a
preamble, four tools (read, bash, edit, write), generic edit rules, "be
concise". There are no turn-end gates, nudges, plan checks or loop
detectors in the headless path. Tool output is cut at 2,000 lines / 50 KB.
Reasoning is sent back on every request in the field it came in, the
same as janetcode since `2cd18dce`, and nothing old is ever stripped.
effbench declares the model to pi with `reasoning: false`, so pi sends
no thinking parameters at all.

**Its leverage is how it starts.** pi's first request is the brief alone,
1.4k tokens. Its first reply is a 60-token `read`. janetcode's first
request carries the whole repository (17k characters on these tasks) and
asks the model to list the task's rules before its first edit, so the
first reply designs the whole change:

| GLM-4.7, 24 runs | first reply | its wall time | prompt at call 2 | prompt at call 6 |
|---|--:|--:|--:|--:|
| janetcode | 13.5k tokens (median) | 168 s (41% of the run) | 18k | 25k |
| pi | 62 tokens | 2 s | 3k | 19k |

That plan's ~10k reasoning tokens are then replayed on every later call,
which is the whole input gap (448k against 396k) and most of the wall-time
gap (413 s against 346 s).

**It is not call efficiency.** Tool calls a run, split at the first green
test run (GLM):

| | pi | janetcode |
|---|--:|--:|
| before green | 17.5 (7.0 reads) | 8.9 (0 reads) |
| after green | 7.3 (2.9 own checks, 3.0 test runs) | 8.5 (5.0 own checks, 2.4 test runs) |
| model calls | 17.7 (1.4 tools a call) | 14.7 (1.2) |

janetcode reaches green in half pi's calls. Both drift after green;
janetcode's tail leans more on ad-hoc check scripts. Harness-injected
text is negligible: about 500 characters a run.

**So the trade is cheap early calls against one expensive plan.** pi's
reads cost little because its context is small when it makes them.
janetcode's plan saves later calls on every model, and costs more than it
saves on a model that reasons at length (GLM, MiMo). On Kimi the attached
repository won (`syn-kimi-inline`: reading files cost 30% more calls).
`syn-ds-start` (DeepSeek, 8 tasks × 3 seeds, binary `79d71434`) measured
the start on its own:

| arm | passed | wall/run | calls/run | input/run | uncached/run | output/run | first reply |
|---|---|---|---|---|---|---|---|
| default | **24/24** | 120 s | 11.2 | 245k | 32.0k | 20.6k | 8.1k tok, 40 s |
| `OFF=inline` (no attached repository) | 23/24 | 108 s | 16.5 | 350k | 30.8k | 22.3k | 106 tok, 1 s |
| `OFF=rulesfirst` | 23/24 | **91 s** | **10.1** | **188k** | **24.0k** | **15.7k** | 7.0k tok, 41 s |
| `OFF=inline,rulesfirst` (pi-shaped) | 23/24 | 112 s | 17.0 | 321k | 36.5k | 19.2k | 106 tok, 1 s |
| pi | **24/24** | 113 s | 17.0 | 348k | 33.3k | 19.3k | 106 tok, 1 s |

- Copying pi's start does not pay: without the attached repository
  janetcode makes pi's 17 calls and sends pi's input. The repository stays.
- The rules-first line (`a96edb03`, added 2026-11-10 and never measured,
  since Kimi was out) costs 23% of the input, 24% of the output and 24% of
  the wall time. The one miss without it (a-catalog-paging s2) is within
  noise; the other two arms that missed, missed the same task. To be
  confirmed on GLM before it comes out of the default.

## GLM-4.7: replay costs input and buys no passes; pi uses fewer tokens (`syn-glm-replay`, 2026-11-11)

`glm-4.7` on Z.ai's coding plan (config `glm-4.7`, backend `zai` in
`run.py`, key `ZAI_CODING_PLAN_API_KEY`), the 8 default tasks × 3 seeds,
binary `79d71434`. The plan bills no per-token cost. GLM is slow here:
the campaign took 3 h 24 min at 3 jobs.

| arm | passed | wall/run | median | max | calls/run | input/run | hit | uncached/run | output/run |
|---|---|---|---|---|---|---|---|---|---|
| replay (default) | 24/24 | 413 s | 450 s | 758 s | **14.7** | 448k | 91% | 41.2k | 30.8k |
| `JANETCODE_OFF=reasoning` | 24/24 | 507 s | 420 s | 1200 s | 14.9 | **280k** | 92% | **23.3k** | 37.2k |
| pi | 24/24 | **346 s** | **341 s** | **669 s** | 17.7 | 396k | 92% | 32.8k | **25.2k** |

- Every arm passed everything, so this says nothing about reliability.
- Replay: 60% more input and 77% more uncached input for the same number
  of calls, 17% less output and 19% less wall time. One noreplay run hit
  the 20-minute cap and still passed.
- Against pi, janetcode with replay makes 17% fewer calls but sends 13%
  more input, 26% more uncached input, writes 22% more output and is 19%
  slower. On this model pi is the leaner agent. Without replay janetcode
  sends 29% less input than pi but writes 48% more output.
- GLM reasons at length (30–37k output tokens a run against DeepSeek's
  19k), and every token of it is sent back on each later call. Replay
  paid for itself on DeepSeek and does not here.

**Open.** Bound what is replayed (for example only the reasoning of
replies since the last edit, or a size cap) and A/B it on both models.

## DeepSeek: reasoning replay measured, and four loop fixes (`syn-ds-replay`, `syn-ds-lsp`, 2026-11-10)

Kimi on ai02 was still out (upstream credential rejected), and so were the
broker's own MiMo and DeepSeek boxes. Both campaigns ran on the routed
`deepseek/deepseek-v4.1-flash` (config `deepseek-flash`), the 8 default
tasks × 3 seeds. The routed `xiaomi/mimo-v2.6-flash` (config `mimo-flash`)
answers too, but a reply takes up to seven minutes.

**`syn-ds-replay`: reasoning sent back or dropped, same binary (`4ee37868`+), and pi.**

| arm | passed | wall/run | median | max | calls/run | input/run | hit | uncached/run | output/run | cost/run |
|---|---|---|---|---|---|---|---|---|---|---|
| replay (default) | **24/24** | **111 s** | **94 s** | **301 s** | **10.0** | 211k | 85% | 32.5k | 19.1k | $0.0181 |
| `JANETCODE_OFF=reasoning` | 22/24 | 181 s | 127 s | 1200 s | 11.8 | **157k** | 77% | 36.2k | 26.4k | $0.0230 |
| pi | **24/24** | 129 s | 107 s | 318 s | 14.6 | 272k | **91%** | **24.6k** | **18.8k** | **$0.0157** |

- Replay costs 34% more input a run, most of it cached, and saves 15% of
  the calls, 28% of the output and 21% of the cost. The two passes are
  within the noise floor (2 in 15); one noreplay run hit the 20-minute cap.
- Against pi: the same passes, 31% fewer calls, 22% less input and 14%
  less wall time. pi is still 13% cheaper a run, because its cache hit is
  91% against 85% and it sends 24% less uncached input.

**`syn-ds-lsp`: Python diagnostics reachable or hidden, same binary (`1f0ba002`).**
pyright is a `pip --user` install, and the benchmark's throwaway `HOME`
hides it, so no Python task in any earlier campaign had language-server
checks. The `lsp` arm sets `PYTHONUSERBASE` so it starts.

| arm | passed | wall/run | calls/run | input/run | hit | uncached/run | output/run | cost/run |
|---|---|---|---|---|---|---|---|---|
| base | 23/24 | 110 s | 10.2 | 215k | 80% | 43.4k | 18.5k | $0.0177 |
| lsp | 24/24 | 117 s | 10.5 | 241k | 86% | 34.5k | 19.5k | $0.0180 |

No difference outside noise. The benchmark's missing pyright has not been
distorting the results.

**Four fixes, each from session logs, none A/B'd on its own.**
- `d47bcc96`: a 5xx or 429 error frame inside an open stream is resent.
  It was final: the MiMo smoke run ended with exit 59 on "Upstream idle
  timeout exceeded" after a seven-minute first reply.
- `4ee37868`: a product edit restarts the wrap-stop count, at most twice a
  turn (`JANETCODE_OFF=wrapstopedit`). Runs the harness ended passed 10 of
  36 on Kimi, against 379 of 495 for runs the model ended; 20 of the 36
  had edited a product file in their last ten calls, some one call before
  the stop.
- `82e28c4a`: the plan check no longer flags expressions, virtual paths,
  data files, ids, built-in exceptions, dunders or library members. It
  went into 134 of 552 Kimi runs with about 200 names, none of them wrong.
- `1f0ba002`: "diagnostics unavailable" is said once a session, not after
  every edit (50 of the 77 diagnostics notes in syn-kimi-final).

**The cache-hit gap to pi (85% against 91%) is not a harness defect.**
Uncached input over the 24 runs, from the proxy logs:

| | first call | new text on later calls | prefix the provider did not serve from cache | total |
|---|--:|--:|--:|--:|
| janetcode (241 calls) | 141k | 402k | 237k | 780k |
| pi (351 calls) | 31k | 395k | 164k | 591k |

- janetcode never shortened or rewrote its history: every request grew.
- The provider missed the whole prefix on about one call in ten for both
  agents (21 of 217 follow-up calls, pi 26 of 327). 14 of janetcode's 21
  were the second call of a run, where the miss re-bills the 5–7k-token
  first request with the repository attached; pi's first request is 1.6k.
- So the 190k gap is the attached repository paid for once (110k) and,
  in most runs, a second time (about 75k). Reading the files instead
  cost 30% more calls on Kimi (`syn-kimi-inline`), so it stays attached.

**Open.** The Kimi replay A/B (`syn-kimi-replay`) still waits for the
quota.

## Kimi: why janetcode missed where pi passed — reasoning was never sent back (2026-11-10, offline analysis)

From the session logs, not a new run (the ai02 Kimi quota is exhausted).

**Cause.** Kimi on ai02 returns its reasoning in the `reasoning` field.
pi sends it back on every later request, in that same field (pi-ai
`openai-completions`, `thinkingSignature: "reasoning"`). janetcode stored
it and dropped it from every request, so each reply started without the
design the previous one had reasoned out.

- syn-kimi-final lean a-catalog-paging-s1. At call 1 the model reasoned:
  "for descending, item_key < key, or item_key == key and id > …". At
  call 2, with that reasoning gone (its thinking was one line), it wrote
  `search()` with `(key(p), p.id) > (bound_key, bound_id)` for both
  directions. Hidden tests `test_cursor_with_descending_and_ties` and
  `test_walk_every_sort…` failed. pi got this right first time on all 3 seeds.
- All 552 janetcode sessions. A plancarry notice marks a reply that
  reasoned at length and then edited, after which the plan is lost.
  - Runs with that notice: 66% pass.
  - Runs without: 74% pass.
  - d-cronexpr: 3/18 with a lost plan, against 27/61 without.

**Second defect: wrap-up timing.** The wrap-up count ran from the first
green test run and ignored edits. In the same run:
- call 4: tests green;
- call 5: the model's own check found a real tie-break bug;
- call 6: it fixed the bug;
- call 12: the note said the change had been "checked since it first
  passed", and the model then dropped its red cursor checks.

Across all sessions the note fired in 104/160 failed runs and 126/392 passed ones.

**Fixes.**
- `2cd18dce`: the thinking block records its field (`llm.Block.Via`) and
  is sent back there. The plancarry note is skipped when the reasoning is
  replayed. `JANETCODE_OFF=reasoning` turns replay off.
- `a092fd39`: an edit of a product file restarts the wrap-up count.

A wire check against a local SSE stub confirmed the second request
carries `"reasoning"` on the assistant tool-call message.

**To measure.** `syn-kimi-replay` (replay / noreplay / pi, 8 tasks × 3
seeds, `bin/janetcode-m6`) is queued and starts when the quota returns.
Expect more input tokens per call, mostly cached, and fewer misses on
a-catalog-paging and d-cronexpr.

## Kimi: master, a lean arm and pi (`syn-kimi-final`, `syn-kimi-plan`, 2026-11-08)

`syn-kimi-plan` (5 hard tasks × 3 seeds, binary `6608d9ec`+): master 11/15, 15.5 calls, 231k
input; plan budget off 10/15, 16.0 calls, 239k; plan budget, spec review and edge rules
off ("lean") 9/15, 14.1 calls, 204k. The plan budget is not why
d-cronexpr fails.

`syn-kimi-final`: the 8 default tasks × 3 seeds, three arms in one window.

| arm | passed | wall/run | median | max | calls/run | input/run | hit | uncached/run | output/run |
|---|---|---|---|---|---|---|---|---|---|
| master | 19/24 | 426 s | 324 s | 1210 s | 16.6 | 256k | 92% | 20.1k | 14.7k |
| lean | 19/24 | **283 s** | **234 s** | **576 s** | **11.5** | **147k** | 89% | **16.3k** | **12.8k** |
| pi | **22/24** | 376 s | 292 s | 1210 s | 15.2 | 221k | 90% | 21.6k | 13.8k |

- Lean passed as often as master at 31% fewer calls, 43% less input and
  34% less wall time. Against pi it used 24% fewer calls, 33% less input,
  25% less uncached input and 7% less output, and its slowest run took
  576 s (pi: one 1210 s timeout).
- Lean's five misses: a-catalog-paging 0/3 and a-build-incremental 1/3.
  The spec review was added for a-catalog-paging, and lean also scored
  1/3 there in `syn-kimi-plan`, against master's 2/3. (Spec review off
  alone scored 2/3 in `syn-kimi-endrounds`, so the evidence is thin.) The new
  headless default (`7caa0ca2`) drops the plan budget and edge-rules note
  and keeps the spec review.

**Confirmation (`syn-kimi-confirm`, `7caa0ca2` against pi, void).** From
07:15 ai02 answered 429 to both agents ("all API keys for provider kimi
are unavailable"). Only 9 task-seeds were clean on both sides: janetcode
7/9, 431 s, 15.2 calls, 231k input, 20.5k uncached, 16.2k output; pi 9/9,
342 s, 15.9 calls, 262k, 25.5k, 17.0k. It is rerun as
`syn-kimi-confirm2` once the quota is back.

## Kimi: overnight A/Bs (2026-11-08)

Each A/B: a-build-incremental, a-catalog-paging, b-promo-clock, d-cronexpr
and d-todocli × 3 seeds, the same binary in every arm, `kimi-for-coding`
on ai02.

**Noise floor.** The first wraprules and literal campaigns compared a binary with
itself: `JANETCODE_OFF` drops a name the switch registry does not list,
and neither was listed (fixed in `82d2695b`, with a test that every
`Off[...]` name the code reads is registered). Identical arms scored 11/15
against 10/15 and 9/15 against 11/15, and calls a run differed by up to
34%. A difference of one or two passes in 15 runs is noise.

| campaign | arm | passed | wall/run | calls/run | input/run | uncached/run | output/run |
|---|---|---|---|---|---|---|---|
| `syn-kimi-rules2` | on | 9/15 | 360 s | 17.6 | 267k | 20.7k | 16.0k |
| | wraprules off | 9/15 | 367 s | **16.0** | **227k** | 19.2k | 14.3k |
| | literal off | 10/15 | 414 s | 21.5 | 354k | 23.3k | 17.2k |
| `syn-kimi-endrounds` | on | 9/15 | 405 s | 19.5 | 309k | 22.0k | 16.9k |
| | specreview off | 10/15 | 346 s | 17.2 | **256k** | 19.9k | 15.3k |
| | edgerules off | 10/15 | 345 s | **16.1** | 298k | 20.4k | 16.1k |
| `syn-kimi-inline` | repo attached | 9/15 | 411 s | **19.0** | **339k** | 22.7k | 17.5k |
| | model reads files | 11/15 | 446 s | 24.7 | 391k | 26.3k | 18.3k |

- **wraprules** (`7e7d769a`): the wrap-up note listed the brief's strict rules that no check exercised.
  It fired in 8 runs and 3 of them passed, with no passes gained and 1.6
  calls a run added. Dropped in `e6b0ae20`.
- **literal** (prompt line: use the task's names verbatim): one pass
  fewer with it, fewer calls. Kept, since the line is cheap; to watch.
- **specreview and edgerules**: turning either off cost no passes and
  saved 12–17% of calls. Tested together with the plan budget in
  `syn-kimi-plan`.
- **The attached repository**: having the model read files costs 30% more calls for two
  passes, which is within noise. Kept attached.
- **Provider stop**: Kimi on ai02 ended two streams with finish `repeat`
  (StopOther). The turn ended there after one call, and one of the two
  runs failed. Such a reply is now sent back like a cut-off one
  (`6608d9ec`).
- **Plan budget**: in 9 campaigns, the budget cut a reply in all 27
  d-cronexpr runs, and 6 of those passed. pi passes that task 2/3. This
  is under A/B in `syn-kimi-plan`.

## Kimi: owncheck dropped, wrapstop kept (`syn-kimi-owncheck`, `syn-kimi-wrapstop`, 2026-11-07)

Both A/Bs ran one switch off against the same binary, on a-build-incremental,
a-catalog-paging, c-ts-eventsource, d-cronexpr and d-todocli × 3 seeds.

**`owncheck` (`b2407e6c`, reverted in `571ebd06`).** When the project tests
pass and the model's own check has failed three times at the same place,
one note asks it whether the check or the code is wrong.

| arm | passed | wall/run | median | calls/run | input/run | uncached/run | output/run |
|---|---|---|---|---|---|---|---|
| on | 7/15 | 474 s | 417 s | 22.0 | 465k | 25.9k | 21.1k |
| off | **10/15** | 453 s | 399 s | 22.7 | **405k** | 24.1k | 19.1k |

It lost three passes and saved nothing. Part of the note says to drop a
check the task does not state, and the misses suggest the model also
dropped checks the task did state.

**`wrapstop` (`863dc5f0`, kept).** Ten calls after the wrap-up note, a turn
whose change still passes is ended by the harness.

| arm | passed | wall/run | median | max | calls/run | input/run | uncached/run | output/run |
|---|---|---|---|---|---|---|---|---|
| on | 12/15 | **397 s** | 425 s | **615 s** | **16.5** | **231k** | 20.2k | 17.1k |
| off | 12/15 | 541 s | 422 s | 1200 s | 24.8 | 528k | 27.9k | 22.0k |

It ended only one run (d-cronexpr s1, a miss either way), so most of the
gap is the off arm's runaway runs: a-catalog-paging s3 timed out, and
d-cronexpr ran at 46 calls a run. Those are the runs it exists to cut off,
and it costs nothing when it does not fire, so it is kept. The size of
its saving is not measured.

## Kimi: synthetic, janetcode with wrap-up and slim tools against pi (`syn-kimi-pi2`, 2026-11-07)

janetcode `eb5fe36f` (wrap-up note and slim tool schemas on) and pi, the 8
default tasks × 3 seeds, 20-minute cap, `kimi-for-coding` on ai02.

| arm | passed | wall/run | median | max | calls/run | input/run | hit | uncached/run | output/run | tools bytes |
|---|---|---|---|---|---|---|---|---|---|---|
| janetcode | 17/24 | 472 s | 409 s | 1200 s | 23.6 | 500k | 95% | 25.0k | 17.0k | 2321 |
| pi | **23/24** | **410 s** | **283 s** | 1210 s | **15.0** | **210k** | 90% | **21.8k** | **14.0k** | 2956 |

| task | janetcode pass | janetcode calls | pi pass | pi calls |
|---|---|---|---|---|
| a-build-incremental | 1/3 | 14.7 | 3/3 | 16.3 |
| a-catalog-paging | 1/3 | 48.7 | 3/3 | 23.0 |
| b-promo-clock | 2/3 | 8.0 | 3/3 | 11.7 |
| b-report-paging | 3/3 | 10.7 | 3/3 | 11.0 |
| c-rs-logstore | 3/3 | 19.3 | 3/3 | 18.0 |
| c-ts-eventsource | 3/3 | 18.3 | 3/3 | 18.7 |
| d-cronexpr | 1/3 | 50.7 | 2/3 | 10.7 |
| d-todocli | 3/3 | 18.7 | 3/3 | 11.0 |

- pi is ahead on every column but cache hit. janetcode's input gap is two
  runaway runs: a-catalog-paging s2 (95 calls, timeout) and d-cronexpr s3
  (88 calls, timeout), each debugging a check of its own after the tests
  passed. Without them janetcode is at 17.5 calls and 255k input a run.
- The five other misses are one hidden rule each, on tasks where pi
  passed every seed. The wrap-up note fired in 13 runs (8 passed) and not
  in 11 (9 passed); it fires on the long runs, so this does not separate
  cause from effect.
- Two fixes for the runaway runs are under A/B (`owncheck`, `wrapstop`).

## Kimi: the wrap-up note and slim tool schemas (2026-11-07, both kept)

Both A/Bs on a-build-incremental, a-catalog-paging, d-todocli,
b-promo-clock and c-ts-eventsource × 3 seeds, one switch off against the
same binary.

**`wrapup` (`5337ca25`, `syn-kimi-wrapup`).** Once a headless turn is
green (VerifiedBy) and the model has made 8 more calls since it first
was, one note: finish, or check one more rule and name it. pi makes 7.1
calls after its first green run; janetcode made 12.3.

| arm | passed | wall/run | median | calls/run | input/run | uncached/run | output/run |
|---|---|---|---|---|---|---|---|
| on | 11/15 | 356 s | 358 s | **14.9** | **226k** | 20.3k | **14.5k** |
| off | 11/15 | 361 s | 332 s | 17.8 | 303k | 20.8k | 15.7k |

It fired in 6 runs; the model made about 4 more calls and finished. 16%
fewer calls, 25% less input, same passes.

**`slimtools` (`49d882c0`, `syn-kimi-slim`).** A headless run advertises
only the fields Kimi used (3,267 calls: then_run 0, background 2, stdin 1,
anchors 1, line edits 24). Tool list 6,515 → 2,321 bytes; first call
6.46k → 5.57k tokens.

| arm | passed | wall/run | median | calls/run | input/run | uncached/run | output/run |
|---|---|---|---|---|---|---|---|
| slim | 12/15 | 371 s | 351 s | 16.3 | 246k | 20.5k | 15.6k |
| full | 13/15 | 366 s | 378 s | 15.1 | 227k | 19.9k | 15.7k |

The ~900 tokens a call it removes are lost in the spread of calls per run
(a-catalog-paging: 29 against 15 calls); the one extra miss is
a-build-incremental s3, a task that misses about one run in three on
either arm. Kept for the fixed per-call saving; to watch.

## Kimi: the harness reruns checks, and a check file (`syn-kimi-autocheck`, 2026-11-07, dropped)

`b5c8e5e2` added `autocheck` (after an editing reply, the harness reran the
model's last two check commands and appended the output to the edit's
result) and `scratchcheck` (one check file in a session dir outside the
repository, edited instead of retyped). A/B against the same binary with
both off: a-build-incremental, a-catalog-paging, d-todocli, b-promo-clock,
c-ts-eventsource × 3 seeds.

| arm | passed | wall/run | median | calls/run | input/run | uncached/run | output/run |
|---|---|---|---|---|---|---|---|
| on | 12/15 | 399 s | 416 s | 23.4 | 471k | 25.4k | 15.0k |
| off | 13/15 | 400 s | 387 s | **18.7** | **306k** | **21.4k** | 15.7k |

- Same wall, 25% more calls, 19% more uncached input. Output fell 5%.
- The model kept running the tests itself (3.5/run, 1.3 of them straight
  after an autocheck had shown the result) and ran its check file itself
  5.1 times a run besides the 7.4 autocheck reruns. The check file added
  3.5 edits of it per run: a script call became an edit call.
- Reverted (`d6a639b8`, `ca3fc6d5`). With Kimi, a harness-side rerun does
  not replace the model's own: it adds to it.

## Kimi: synthetic, janetcode against pi (`syn-kimi-pi`, 2026-11-07)

janetcode `0a58b3e9` and pi side by side, the 8 default tasks × 2 seeds,
20-minute cap, `kimi-for-coding` on ai02.

| arm | passed | wall/run | median | max | calls/run | input/run | hit | uncached/run | output/run |
|---|---|---|---|---|---|---|---|---|---|
| janetcode | **14/16** | 482 s | 492 s | 1115 s | 19.8 | 373k | 94% | 23.9k | 18.2k |
| pi | 13/16 | **428 s** | **300 s** | 1168 s | **17.1** | **254k** | 91% | **22.9k** | **14.6k** |

- Every miss is one hidden test. janetcode: a-build-incremental s2 (a
  failed build keeps its cache entry), d-cronexpr s1 (`MON/2`). pi: both
  a-build-incremental runs (`plan()` reads an output that is not built
  yet), d-cronexpr s2 (`prev` at an exact match).
- Wall is model time (janetcode 424 s/run of 482, pi 348 of 428). janetcode
  writes 25% more output: on d-todocli s1 (1115 s, 41 calls) it wrote a
  150-line acceptance script of its own and spent 25 calls running it. It
  passed; pi passed the same task in 281 s.
- The same janetcode build passed 15/16 at 363 s/run in `syn-kimi-lean`
  the day before: between windows, wall moves about 30%.

## Kimi: realrepo and synthetic, master against a pi-shaped lean arm (2026-11-06)

Model `kimi-for-coding` on ai02. Realrepo: 5 bug fixes in caddy and hugo
(`runs/realrepo-tasks`), 3 seeds, 15-minute cap. Synthetic: the 8 default
tasks, 2 seeds. pi and Qwen Code are from `rr-kimi-3arm` (2 seeds, same
tasks; pi runs with `-nc -ns`, so it never sees a repository's AGENTS.md).

| realrepo (`rr-kimi-lean`) | passed | wall/run | median wall | input/run | uncached/run | calls/run | output/run |
|---|---|---|---|---|---|---|---|
| janetcode master `8ed57614` | 15/15 | 237 s | 168 s | 140k | 11.9k | 15.3 | 4.8k |
| lean arm | 15/15 | 310 s | 190 s | 204k | 13.6k | 20.6 | 6.1k |
| pi | 10/10 | 214 s | 202 s | 108k | 12.0k | 14.0 | 5.3k |
| Qwen Code | 10/10 | 353 s | 208 s | 499k | 24.1k | 17.8 | 7.6k |

| synthetic (`syn-kimi-lean`) | passed | wall/run | median wall | input/run | calls/run | output/run |
|---|---|---|---|---|---|---|
| janetcode master | 15/16 | 363 s | 327 s | 222k | 14.8 | 13.5k |
| lean arm | 12/16 | 314 s | 218 s | 194k | 13.8 | 12.1k |

The lean arm was the hypothesis that janetcode's extra checking was what
made it slower than pi on realrepo. In `rr-kimi-final`, janetcode made 241
tool calls to pi's 133, with 32 test-suite runs to pi's 2. The arm runs
`lean2-system.md`, a prompt in pi's shape. It also sets
`JANETCODE_OFF=specreview,edgerules,plancarry,editecho,testhint,thenrun`.

The hypothesis was wrong on both sets:

- **Realrepo:** without the prompt's rules the model wandered more. It made
  more calls and read more input, and one caddy run took 686 s.
- **Synthetic:** without the spec review, a-catalog-paging validated its
  price filters and never applied them, twice. That is the miss the review
  was added for.

Master stays the headless configuration.

Against pi on realrepo, master's median is better and its uncached input
and output match pi's. Its mean is behind pi's because of one run.
rr-hugo-replacere s2 followed hugo's AGENTS.md ("Use `./check.sh` when
you're done"), which runs hugo's whole suite. It polled the backgrounded
job with `sleep` until the 900 s cap, after the package's own check had
passed. Without that run, master averages about 185 s per run.

## Latest (campaign `jc-mimo-r13`, 2026-10-10)

janetcode `5c3ed1a2` against pi on all 16 tasks × 2 seeds: janetcode
30/32 valid, pi 26/32 valid (27 passed, one flagged); janetcode 4%
faster, 20% fewer calls, 30% less output. Over r12 and r13: janetcode
60/64 valid, pi 55/64. The runs from r8 on are below, misses and all.

## Result (campaign `jc-mimo-r8`, 2026-10-09)

All 16 tasks, the default subset and 8 held-out tasks never used for
tuning, × 2 seeds, janetcode `6df57039` and pi side by side:

| arm | runs | passed | time | speed-adjusted | calls | input | cached | fresh input | output | tamper |
|---|--:|--:|--:|--:|--:|--:|--:|--:|--:|--:|
| janetcode | 32 | **28/32** | **10,762 s** | **10,762 s** | **424** | **7.71M** | 91% | **702k** | **724k** | **0** |
| pi | 32 | **28/32** | 12,716 s | 12,864 s | 591 | 8.06M | 90% | 767k | 1,190k | 1 |

- Same passes; janetcode is 15% faster (16% speed-adjusted), makes 28%
  fewer calls, sends 4% less input and 8% less fresh input, and writes
  39% less output.
- pi's one tamper flag is on a-http-ratelimit s1, graded PASS; counted
  as invalid, pi has 27/32.
- Misses: janetcode a-catalog-paging s1 and d-cronexpr s2 (one hidden
  test each) and both d-minire runs; pi a-doc-undo s1, c-ts-jsonpatch s2
  and both d-minire runs. Nobody has passed d-minire on MiMo here.
- One janetcode d-minire run spent twelve minutes on a reply that
  started 392 tool calls. `6e50c06f` caps a reply at 24 calls;
  `jc-mimo-r9` measures it.

### The repeat run: `jc-mimo-r9` (`6e50c06f`, burst cap added)

| arm | runs | passed | time | speed-adjusted | calls | input | fresh input | output | tamper |
|---|--:|--:|--:|--:|--:|--:|--:|--:|--:|
| janetcode | 32 | 27/32 | **11,459 s** | **11,459 s** | **419** | **7.86M** | **688k** | **744k** | 1 |
| pi | 32 | **29/32** | 12,772 s | 13,073 s | 642 | 10.27M | 893k | 1,241k | 0 |

The efficiency result repeats (10% faster, 35% fewer calls, 40% less
output), but pi passed two more, and janetcode's tamper flag makes its
count 26/32 valid. Over r8 and r9 together: janetcode 55/64 (54 valid),
pi 57/64 (56 valid), janetcode 13% faster. So the two are within noise
on passes, not ahead.

- The tamper: on b-invoice-totals s2 the model rewrote `test_public.py`
  with `write_file` (same content, final newline lost) and wrote a test
  file of its own. `58479681` tells a headless run to leave tests and the
  brief alone and keep its own checks inline.
- d-cronexpr s2 ended with no call completed: five replies in a row each
  carried 24 calls, only 3–9 of them distinct, one group of edits and
  checks written over and over, until the turn's 120-call ceiling.
  `8d768119` ends a reply at its first exact repeat.
- c-go-cache (both seeds) and d-cronexpr s1 each missed one hidden test.
- janetcode passed d-minire once, the first pass of that task by either
  agent in any campaign.

### The third run: `jc-mimo-r10` (`8d768119`, repeat cut and the tests rule)

| arm | runs | passed | time | speed-adjusted | calls | input | fresh input | output | tamper |
|---|--:|--:|--:|--:|--:|--:|--:|--:|--:|
| janetcode | 32 | **28/32** | **11,184 s** | **11,184 s** | **422** | **8.02M** | **683k** | **708k** | **0** |
| pi | 32 | 27/32 | 13,418 s | 13,892 s | 639 | 11.01M | 1,129k | 1,319k | 1 |

- janetcode passes one more, and pi's tamper flag (d-cronexpr s1, graded
  PASS) makes pi's count 26/32 valid: janetcode 28 valid against 26.
- janetcode is 17% faster (19% speed-adjusted), makes 34% fewer calls,
  sends 27% less input and 40% less fresh input, and writes 46% less
  output.
- Misses: janetcode a-catalog-paging s2 and c-go-cache s2 (hidden tests
  after the public ones passed) and both d-minire runs; pi
  b-invoice-totals s2, c-ts-jsonpatch s1 (at the cap), d-linediff s2 and
  both d-minire runs.
- No janetcode reply hit the 120-call turn ceiling; d-cronexpr passed on
  both seeds.

Over r8, r9 and r10 together (96 runs each): janetcode 83 passed, 82
valid; pi 84 passed, 82 valid. Equal on valid passes over the three
runs, with janetcode ahead in two of them; janetcode took 14% less time
(33,405 s against 38,906 s), 32% fewer calls (1,265 against 1,872) and
42% less output (2,176k against 3,750k).

### Spec review: `jc-mimo-r11` (`f6cd0fae`)

`f6cd0fae` sends a headless turn that changed files and passed its checks
back once, to check the change against the task requirement by
requirement.

| arm | runs | passed | time | speed-adjusted | calls | input | fresh input | output | tamper |
|---|--:|--:|--:|--:|--:|--:|--:|--:|--:|
| janetcode | 32 | 29/32 | 16,042 s | 16,042 s | 656 | 15.26M | 1,072k | **1,194k** | 1 |
| pi | 32 | 29/32 | **13,796 s** | **13,489 s** | 670 | **12.77M** | **960k** | 1,319k | 0 |

- The review works on correctness: every miss janetcode had in r10
  outside d-minire passed (a-catalog-paging, c-go-cache both seeds). On
  a-catalog-paging s1 it found two bugs the public tests missed:
  descending sorts came out ascending, and the cursor went wrong on
  descending sorts.
- It costs too much: it fired on almost every run and added 3 to 24
  calls, about 150 s a run, mostly spent debugging the model's own check
  scripts. janetcode went from 17% faster than pi (r10) to 16% slower.
- The tamper: on d-cronexpr s2 the model wrote a `test_cronx.py` of its
  own and edited its assertions to match the code, against the prompt.
  Valid passes: janetcode 28, pi 29.

Two changes follow. `1b033e85` refuses a headless run's first write to a
test file, with a note to check inline (a second write to the same path
goes through, for a task that asks for tests). The next commit tells the
model to wrap the review up after six tool steps, and to trust the
task's words over a check of its own that disagrees. `jc-mimo-r12`
measures both.

### Test guard and a capped review: `jc-mimo-r12` (`e4dfa3a4`)

| arm | runs | passed | time | speed-adjusted | calls | input | fresh input | output | tamper |
|---|--:|--:|--:|--:|--:|--:|--:|--:|--:|
| janetcode | 32 | **30/32** | 16,400 s | 16,400 s | 633 | 13.94M | 1,000k | **1,133k** | **0** |
| pi | 32 | 29/32 | **14,838 s** | **14,707 s** | **629** | **10.07M** | **879k** | 1,314k | 0 |

- janetcode passes one more, with no tamper flag; the test guard refused
  a test-file write on two runs, and both passed. janetcode passed
  d-minire s2, its second pass of that task in any campaign; pi missed
  d-linediff s1 and both d-minire runs.
- The broker was slower for both arms than in r10.
- The review still cost what it did in r11: about 5,100 s over the 32
  runs (53–375 s each), more than the whole gap to pi. The cap of six
  steps was advisory and runs went to 17; most of the time went on
  writing and running new check scripts.

The next commit makes the review read instead of check: compare the
brief with the code, fix what does not hold, run the tests once, and
wrap up after three tool steps. `jc-mimo-r13` measures it.

### A review that reads: `jc-mimo-r13` (`5c3ed1a2`)

| arm | runs | passed | time | speed-adjusted | calls | input | fresh input | output | tamper |
|---|--:|--:|--:|--:|--:|--:|--:|--:|--:|
| janetcode | 32 | **30/32** | **12,792 s** | **12,792 s** | **541** | 12.18M | **876k** | **900k** | **0** |
| pi | 32 | 27/32 | 13,323 s | 13,634 s | 675 | **10.75M** | 1,018k | 1,286k | 1 |

- janetcode leads on passes and on cost together: 30 valid against pi's
  26 (pi's a-http-ratelimit s1 is a flagged PASS). It is 4% faster (6%
  speed-adjusted), makes 20% fewer calls, sends 14% less fresh input
  and writes 30% less output; its total input is 13% higher, the cached
  context the review rereads.
- The review cost fell from about 150 s a run to the point where
  janetcode is faster than pi again, and the passes held at r12's 30.
- Misses: janetcode a-build-incremental s2 and d-minire s2 (it passed
  d-minire s1, at the cap); pi c-rs-logstore s1, c-ts-eventsource s2 (at
  the cap), d-todocli s1 and both d-minire runs.

Over r12 and r13, the two runs with the test guard and the review:
janetcode 60/64 valid, pi 55/64 valid.

## Result on the tuning tasks (campaign `jc-mimo-r4`, 2026-10-09)

| arm | runs | passed | time | speed-adjusted | calls | input | cached | fresh input | output | tamper |
|---|--:|--:|--:|--:|--:|--:|--:|--:|--:|--:|
| janetcode, headless base | 16 | 15/16 | **5,520 s** | **5,520 s** | **166** | **2.39M** | 88% | **288k** | **413k** | 0 |
| janetcode, full prompt | 16 | **16/16** | 5,877 s | 5,911 s | 202 | 4.25M | 91% | 363k | 443k | 0 |
| pi | 16 | **16/16** | 6,489 s | 6,735 s | 336 | 4.84M | 92% | 410k | 487k | 0 |
| herox lean (hero_agent `final-mimo`, 2026-10-08) | 16 | 13/16 | 5,028 s | — | 234 | 3.01M | 90% | 296k | 357k | 6 |

- **Speed-adjusted** recomputes each arm's wall time at the first arm's
  decode speed in the same run, using hero_agent's definition. herox's row
  comes from another campaign, so its times are not comparable to this window
  (its pi ran at 5,841 s there); its passes and tokens are.
- **Full prompt (build `0f223ab3`)** beats pi on every column with the same
  pass count: 9% faster, 40% fewer calls, 12% less input, 11% less fresh
  input, 9% less output.
- **Headless base**, the same build started from the ~1 KB `prompt.Headless`
  (then `lean-system.md`), is 15% faster than pi. It makes half the calls,
  sends half the input, and writes 15% less output. It missed one task:
  `c-rs-logstore` seed 2, the `invalid_header_is_corrupt_even_if_short` rule.
  herox missed that same rule on several models. `95dc3f87` adds one line
  for that class to the headless base; campaign `jc-mimo-r6` measures it.

## After r4: held-out tasks and the plan budget (2026-10-09)

Three campaigns, all MiMo through ai02, run partly in parallel (arms of
one campaign always share the window; campaigns do not, so compare rows
within a table, not across).

**r6: the edge-rule line (`95dc3f87`), 8 tuning tasks × 2 seeds.**

| arm | passed | time | calls | input | output |
|---|--:|--:|--:|--:|--:|
| janetcode | **15/16** | 9,051 s | **196** | **3.25M** | 534k |
| pi | 14/16 | **8,519 s** | 316 | 5.31M | **487k** |

**r5: held-out tasks, never looked at before (build `dedc0555`), 8 × 2.**

| arm | passed | time | calls | input | output |
|---|--:|--:|--:|--:|--:|
| janetcode | 10/16 | **11,566 s** | **165** | **2.44M** | **623k** |
| pi | **11/16** | 12,054 s | 327 | 5.14M | 669k |

The calls and input advantage holds on unseen tasks: half of pi's. Passes
and time do not separate the two. Both janetcode d-minire runs, and one
b-shift-dst run, ended at the 20-minute cap after one to three calls: the
first reply reasoned 23–33k tokens planning the whole change, a second
reply planned again, and nothing was written. Every such reply in r4–r6
was janetcode's first, the reply to the inlined repository; pi's median
reply reasons about 100 tokens.

**r7: the plan budget (`daf2d337`) on and off, same binary, 16 tasks × 1.**
A headless reply that has reasoned past 32,000 characters (~10k tokens)
without text or a tool call is ended and the model told to act.

| arm | passed | time | calls | input | output |
|---|--:|--:|--:|--:|--:|
| janetcode, budget | **14/16** | 9,065 s | 198 | 3.32M | 456k |
| janetcode, no budget | 13/16 | 9,093 s | 197 | 3.57M | 574k |
| pi | 13/16 | **7,917 s** | 273 | 3.93M | **451k** |

The budget cuts output 21% at no cost in passes. pi is faster in this
window; 1,000 s of the gap is d-linediff and d-minire, where pi gave up in
eight minutes (both failed) and janetcode ran to the cap (it passed
d-linediff). In r7 the budget fired once a turn, and one d-minire reply
told to act planned for ten minutes again, so `1d4d3e56` lets it fire up
to three times a turn. Campaign `jc-mimo-r8` measures that build
(`6df57039`) against pi on all 16 tasks × 2 seeds.

## What got janetcode there

The first janetcode-against-pi run (`jc-mimo-r1`, build `13fd1857`):

- janetcode passed 13 of 15 runs; pi passed 16/16.
- It used 4.96M input against pi's 4.33M.

Every change after it came from a transcript of a janetcode run losing to pi.
In commit order:

| commit | what | measured cost before the change |
|---|---|---|
| `58abbb09` | `edit_file` given both a line range and `old_string` settles on one instead of refusing | a refused first reply, a whole round trip |
| `58abbb09` | inlined files count as read for the "edited without reading" note | 1.2–2.6k tokens of a file the model already had, per refusal |
| `58abbb09` | the plan check matches `cache.py` to `reports/cache.py` | a false "does not exist" note |
| `58abbb09`, `8c849761` | a red scratch check the model rewrote green is retired | 2–4 calls after green, on a false "a check came back red" |
| `8c849761` | prompt: stop when the tests pass; no harness of the model's own | 10–12 calls debugging a 305-line scratch harness after green |
| `5c9d164d` | a reply cut at the output limit doubles the limit for the turn | a cut reply is thrown away and regenerated |
| `f1226498` | a headless run sends the coding core: 5 tools, 6.5 KB instead of 22 tools, 25 KB | ~6k tokens of tool schemas on every call |
| `c59692c1` | open plan steps do not hold a turn whose checks passed | 2 calls to tick boxes |
| `b634a73e` | default output limit 32,768 (was 16,000), as pi's | r3: 9 cut replies, 1,971 s and 144k output tokens thrown away |
| `dedc0555` | a headless run starts from `prompt.Headless` | the full prompt reasoned 123k tokens against 83k on the same 7 runs |
| `95dc3f87` | the headless base names ordered and "even if" rules | the one r4 miss |
| `daf2d337`, `1d4d3e56` | a headless reply that reasons past ~10k tokens without acting is sent back to act | 23–33k-token first replies; three held-out runs ended at the cap without an edit |
| `6e50c06f` | a reply that keeps starting tool calls runs its first 24 | bursts of 37–392 calls, all refused at the limit, up to twelve minutes each |
| `58479681` | the headless base says to leave tests and the brief alone | r9's tamper flag |
| `8d768119` | a reply is ended at its first exact repeat of a call | r9 d-cronexpr: 120 calls, 3–9 distinct per reply, nothing passing |

The round before it (`docs/benchmarks/lean-ab`) supplied the base:
- small repositories inlined in the first message;
- `then_run`;
- background language-server checks;
- cache-stable tool loading;
- headless memory opt-in.

Campaign history, all on MiMo through ai02:

| campaign | janetcode build | janetcode passed | time | calls | pi in the same window |
|---|---|---|---|---|---|
| `jc-mimo-r1` | `13fd1857` | 13/15 | 6,843 s | 219 | 16/16, 6,570 s, 282 calls |
| `jc-mimo-r3` | `f1226498` | 16/16 | 6,696 s | 188 | (pi from r1) |
| `jc-mimo-r4` | `0f223ab3`, full / headless base | 16/16, 15/16 | 5,877 s, 5,520 s | 202, 166 | 16/16, 6,489 s, 336 calls |
| `jc-mimo-r6` | `95dc3f87` | 15/16 | 9,051 s | 196 | 14/16, 8,519 s, 316 calls |
| `jc-mimo-heldout-r5` | `dedc0555` (held-out tasks) | 10/16 | 11,566 s | 165 | 11/16, 12,054 s, 327 calls |
| `jc-mimo-r7` | `daf2d337` budget / no budget (16 tasks × 1) | 14/16, 13/16 | 9,065 s, 9,093 s | 198, 197 | 13/16, 7,917 s, 273 calls |
| `jc-mimo-r8` | `6df57039` (16 tasks × 2) | 28/32 | 10,762 s | 424 | 28/32, 12,716 s, 591 calls |
| `jc-mimo-r9` | `6e50c06f` (16 tasks × 2) | 27/32 (1 tamper) | 11,459 s | 419 | 29/32, 12,772 s, 642 calls |
| `jc-mimo-r10` | `8d768119` (16 tasks × 2) | 28/32 | 11,184 s | 422 | 27/32 (1 tamper), 13,418 s, 639 calls |
| `jc-mimo-r11` | `f6cd0fae` (16 tasks × 2) | 29/32 (1 tamper) | 16,042 s | 656 | 29/32, 13,796 s, 670 calls |
| `jc-mimo-r12` | `e4dfa3a4` (16 tasks × 2) | 30/32 | 16,400 s | 633 | 29/32, 14,838 s, 629 calls |
| `jc-mimo-r13` | `5c3ed1a2` (16 tasks × 2) | 30/32 | 12,792 s | 541 | 27/32 (1 tamper), 13,323 s, 675 calls |

## Caveats

- **Tuned on these tasks.** Every change up to r4 was found in transcripts
  of the 8 default tasks; r8 includes 8 held-out ones, though the plan
  budget was itself found in held-out transcripts (r5). The changes are general (edit handling, gates, tool list, output
  limit, prompt length), but that is the same objection raised against
  herox's result. Campaign `jc-mimo-heldout-r5` runs janetcode against pi on 8
  other effbench tasks, two per group, that were never looked at: a-doc-undo,
  a-http-ratelimit, b-invoice-totals, b-shift-dst, c-go-cache, c-ts-jsonpatch,
  d-linediff and d-minire.
- **Small sample.** Two seeds per task. Per-task wall time varies 2–3× between
  seeds of the same arm (pi's d-cronexpr: 513 s and 1,180 s in r1), so totals
  over 16 runs are the unit to compare, not single tasks.
- **One model.** All of this is MiMo V2.6 Flash.
- **pi's limit.** pi runs with a 32,768-token output limit in effbench.
  janetcode's default was raised to the same value during this round.
