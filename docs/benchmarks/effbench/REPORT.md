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

## Result (campaign `jc-mimo-r4`, 2026-10-09)

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

## Caveats

- **Tuned on these tasks.** Every change was found in transcripts of these 8
  tasks. The changes are general (edit handling, gates, tool list, output
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
