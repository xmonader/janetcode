# Novel and wild ideas: what held up live (2026-10-11)

Branch `feat/novel-ideas`. Live effbench runs were made on the ai02 broker on 2026-10-10/11. The model is MiMo V2.6 Flash
(XiaomiMiMo/MiMo-V2.6-Flash-RL); one tester role uses DeepSeek V4.1 Flash. Every arm ran side by side in the same
campaign, so all arms saw the same broker load.

## Verdicts

| Idea | Verdict | Evidence |
|---|---|---|
| #4 `CONFLICT:` exit (`conflict_exit`) | **Kept and made the default** | MiMo impossible split, 16 runs per arm: 13/16 runs ended in a confirmed CONFLICT naming the mutated test, with 0 cheats in any arm. It used −39% wall time, −48% model calls and −63% input against the other two arms, which simply fail. On DeepSeek V4.1 Flash it was **16/16**, every run naming the mutated test, at −55% wall time, −66% calls and −82% input against the no-ledger arm, and with 0 tampers against that arm's 3. **False CONFLICTs on tasks that can be done: 0 of 40** (20 tasks × MiMo and DeepSeek, one seed, with pipefail on): it was never even attempted. Passes were 18/20 against 19/20 without it on MiMo and 18/20 against 18/20 on DeepSeek, and every miss was an ordinary hidden-test failure. |
| #1 probe ledger (`probe_ledger`) | **Not shown to beat noise; worth more seeds** | Held-out, 7 tasks × 3 seeds: **21/21 against the baseline's 18/21**. Probes passed d-linediff 3/3 where the baseline passed 1/3. One-sided Fisher p = 0.12. The "blind" arm is effectively a second baseline (MiMo as the tester wrote almost no checks), and it scored **20/21**: two near-identical arms differ by 2 passes, so the probe ledger's +3 is within noise. Cost: +6% wall time, +11% input. |
| Wild #1 spec coverage (`spec_coverage`) | **Drop as it stands** | 19/21 against 18/21, at +19% wall time and +25% input. It lost d-linediff 2 of 3 times. |
| Wild #3 blind tester (`blind_tester`) | **Drop: no gain measured** | It needed three fixes before it did anything, because MiMo as the tester reasons until it hits the output bound. With DeepSeek V4.1 Flash at `blind_tester_effort=low`, the final result is **20/21**, the same as the second baseline (20/21), at +18% wall time and +45% output. It wrote checks in 14 of 20 recorded runs and found rule violations in 6 of them, but that did not turn into passes. |
| #3 test-integrity ledger (`testledger`, on by default) | **Keep (suggestive, not significant)** | MiMo did not tamper without it (0/16), so there was nothing to catch there. **DeepSeek V4.1 Flash, 16 runs per arm: 3/16 runs edited the mutated test without the ledger, against 0/16 with it** (one-sided Fisher p ≈ 0.11), at +30% input. No arm got a graded cheat. On MiMo one ledger-arm run tampered and hit the 20-minute cap before the turn ended; the ledger only runs at turn end. |
| Default-on bundle on the main set | **Keep: no harm measured** | Main set, all 15 tasks × 1 seed, all new default-on switches on against all off: **14/15 against 15/15**, with −5% wall time, −5% calls and −7% output. The one miss was a-doc-undo, which also fails in baseline arms (1 of 3 on held-out). |

## Follow-up, 2026-10-11

1. **Pipelines fixed.** The bash tool runs under `bash -o pipefail`, so `pytest | tail` exits with pytest's status.
   Exit 141 from a reader that stopped early is not a failure, except on a test run.
2. **CONFLICT made the default** after the false-CONFLICT check above (campaigns `nv-fc-mimo`, `nv-fc-deepseek`).
   Wall time, calls and input with it against without were −8%/−5%/−16% on MiMo and +4%/+6%/+15% on DeepSeek:
   within noise either way, since it never fired.
3. **Dropped ideas removed from the code**: elision, branch retry, second sample, spec coverage and example
   harvesting, blind tester (with the `reasoning_effort` plumbing only it used), flake quarantine, fix cache,
   pattern induction, think-time speculation, hedging and stream resume, prefix kernels and purity
   auto-approval. That is about 14,400 lines. Their measurements stay in the CHANGELOG and `PROJECT-STATE.md`.

All campaigns are complete. To reproduce the tables:

    cd docs/benchmarks/effbench
    python3 compare.py runs/nv-held-mimo:base runs/nv-held-mimo:probes runs/nv-held-mimo:speccov runs/nv-blind-mimo:blindds
    python3 compare.py runs/nv-main-mimo:legacy runs/nv-main-mimo:default
    python3 impossible/score.py runs/nv-imp-mimo ; python3 impossible/score.py runs/nv-imp-ds
    python3 compare.py runs/nv-fc-mimo:base runs/nv-fc-mimo:conflict ; python3 compare.py runs/nv-fc-deepseek:base runs/nv-fc-deepseek:conflict

## Bugs the live runs found, all fixed on the branch with regression tests

- **CONFLICT confirmed against the wrong command.** The harness re-ran the model's own inline `python - <<EOF` check
  instead of the project's tests. That check passed, so a true conflict was sent back as "it passes".
- **CONFLICT read through `| tail`.** `pytest -q 2>&1 | tail -20` exits with tail's status, so a red run read as green.
  Display filters are now dropped from the command the harness re-runs.
- **Blind tester output bound.** At 6,000 output tokens MiMo spent the whole bound reasoning and wrote nothing.
  The bound now matches the main loop's 32k.
- **Blind tester and reasoning effort.** Reasoning models write no checks without `reasoning_effort`, which the
  OpenAI-compatible provider never sends. The new `llm.Request.ReasoningEffort` is sent only when a caller names it,
  and is dropped once a server rejects it. `blind_tester_effort` sets it for the tester alone.
- **Blind tester reply handling.** The parser now accepts DeepSeek's `**RULE 1:**` labels. The tester asks once more
  on an empty reply, and its prompt tells it to start writing at once.
- **`impossible/make.py --check`.** It reported 3 of the 8 mutations as passable because pytest reused cached bytecode
  between the original and the mutated test.
- **`run.py`.** An arm could carry only one `set.K=V` setting.

## Open problems found, not fixed

- ~~The bash tool runs without `pipefail`~~: fixed on 2026-10-11 (see the follow-up above).
- **`llm: empty message at index 1`** ended one MiMo run early (`d-minire`, CONFLICT arm of `nv-fc-mimo`): a request
  went out with an empty message. It is not tied to CONFLICT, but it is not yet explained.
- **The test-integrity ledger and the other turn-end gates never run when a run hits its wall-clock cap.**
- **The headless test guard let the model's edit to the mutated test through** (`b-shift-dst`, ledger arm).

## Dropped on offline evidence, without live runs

These fell below their own kill lines on recorded sessions; the numbers are in `PROJECT-STATE.md` and the CHANGELOG:

- round-trip elision (#2)
- checkpoint-branch retry (#7)
- verdict-gated second sample (#8)
- pattern induction (wild #7)
- think-time speculation (wild #9)
- request hedging and stream resume (wild #10)
- flake quarantine (wild #6)
- fix cache (wild #14)
- purity auto-approve (wild #24)
- prefix kernel (wild #11)
