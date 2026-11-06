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
