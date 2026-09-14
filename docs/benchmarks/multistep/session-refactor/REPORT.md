# Multistep set before and after the September consolidation

Question: did the work between 0244f7d and 2704c9d — nine new features,
the race fixes and two consolidation refactors, including a new minimal
eval agent that now carries a context window — change how well the
harness does on the discriminating set?

Setup: `qwen/qwen3.7-flash` on OpenRouter (the model the committed
baseline uses), `--eval-only multistep --eval-runs 3 --eval-jobs 4`, a
private HOME, both commits built in detached worktrees and run at the same
time. `evals/` is identical at both commits, so the runs pair by task.
(`before-0244f7d.json` carries the label `pre-2704c9d`; it is the 0244f7d
binary.)

```
base    32/36 (89%) · 495 tool calls · $0.047 ($0.00146 per solved) · 98k ctx/solved
current 33/36 (92%) · 567 tool calls · $0.056 ($0.00171 per solved) · 127k ctx/solved
pass-rate delta +2.8% [-11.1%, +19.4%] · not significant
cost per solved ×1.20 [0.89, 1.62] · inside the noise
context per solved ×1.34 [0.86, 2.09] · compactions 0 → 0 · inside the noise
improved:  60-fence-aware-trim, 61-js-semver
regressed: 56-two-failures, 62-env-schema-check
```

Reading: no measurable change. The context rise is mostly 61-js-semver
now solving 3/3 with long runs instead of failing quickly. The two
regressed runs are model reasoning errors (a round-up where round-half-up
was asked; a misread output rule), each 1 of 3, passing in the other
runs. Three runs per task cannot see a regression smaller than roughly
11–15 points, so this rules out a large regression, not a small one.

Re-analyse offline:

```
janetcode --eval-report after-2704c9d.json --eval-compare before-0244f7d.json
```
