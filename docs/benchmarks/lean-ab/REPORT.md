# Lean execution: before and after (2026-10-08)

The round of changes taken from hero_agent's effbench (CHANGELOG "Lean
execution"): headless memory opt-in, `then_run`, small repository inlined
in the first message, background language-server checks, cache-stable tool
loading, a short-reasoning prompt rule, `OLDPWD` scrubbed, and cut-off
replies sent back.

- **old:** `04dc725d`, the commit before the round. **new:** `13fd1857`
  for runs 3–4, `02cd23bf` (everything but the cut-off fix) for runs 1–2.
  **noinline:** new with `JANETCODE_OFF=inline`.
- Model `xiaomi/mimo-v2.6-flash` on the ai02 broker; tool-call ids
  `call_…`, so MiMo was served, not the Kimi substitution of 2026-10-01.
- Tasks 21, 25, 51, 52, 55, 56, 59, 60: each edits an existing small
  repository. Four runs per build, `harnessbench --janetcode-driver
  headless`, old and new launched side by side so both saw the same load.
- Raw rows: `old-run*.json`, `new-run*.json`, `noinline-run*.json`.

## The broker's provider mix is a confounder

The broker spreads `xiaomi/mimo-v2.6-flash` over several upstreams
(`served.provider`). They do not answer alike: on GMICloud the old build's
median output was 1.1k tokens a task, on Novita 3.0k. Ten of the old build's
32 runs went to GMICloud and none of the new build's, which made the new
build look like it wrote 27% more. Compare on one provider.

## Result, Novita-served runs only (old 22, new 32)

| per task | old | new | change |
|---|---:|---:|---:|
| passed | 20/22 | 30/32 | |
| wall time | 121 s | 87 s | **−28%** |
| model calls | 6.7 | 3.9 | **−41%** |
| tool calls | 9.2 | 4.3 | **−53%** |
| fresh input | 11.5k | 9.5k | −17% |
| cached input | 64.7k | 40.7k | **−37%** |
| output | 4.84k | 4.75k | −2% |
| cost | $0.0031 | $0.0028 | −12% |

All runs, providers mixed: passes 30/32 both, wall −18%, calls −36%, tool
calls −48%, cached input −29%.

## Inlining off (32 runs, mostly Novita)

Model calls 6.9 a task (new: 3.9, old: 6.7), wall 127 s, cached input
68.7k. Without the repository in the first message the new build is no
better than the old one on steps: inlining carries most of the gain.

## Notes

- `then_run` went unused by MiMo in the transcripts read (59-memo-race,
  three reproductions): it already puts an edit and the bash call that
  checks it in one reply, which is one round trip. The field costs 221
  bytes of schema; keep it for models that do not batch, and measure it.
- The cut-off fix was prompted by a new-build run on 55-config-provenance
  that spent all 16,000 output tokens on one reasoning-only reply and ended
  "done" with nothing written. Runs 3–4 (with the fix) passed 16/16.
- Per-task output varies 3–5x between runs of the same build (52: 3k to
  16k); four runs are enough for calls and time, not for output per task.
