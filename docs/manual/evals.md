# Evals

Part of the [janetcode manual](README.md); the [README](../../README.md) has the short version.

`make eval` runs the tasks in `evals/*.json` against the configured
provider and prints pass/fail, time, tool calls and cost per task:

```
     task                             time  tools       cost
PASS 01-create-file                  4.2s      1    $0.0004
FAIL 03-fix-failing-test            31.0s      9    $0.0031
     check failed: exit status 1: --- FAIL: TestSumTo
…
18/20 passed · 71 tool calls · 4m12s · cost $0.06
```

A task is a prompt, optional setup files, and a shell `check` that must
exit 0 in the work directory (`EVAL_OUTPUT` names a file with the model's
final text); `max_tool_calls` fails tasks that flail. `EVAL_ONLY=go,files`
narrows by name or tag, `EVAL_JOBS=4` runs more at once, and
`--eval-json` (written to `evals/last-results.json` by `make eval`) keeps
the numbers for comparison. Evals use minimal agents: core tools, no hooks,
memory, skills or MCP, so a change in results comes from the model or the
prompts. They cost real money and are not part of `make test`.

One run of twenty tasks is a coin flip per task, so the harness is built
to measure differences rather than single numbers. `EVAL_RUNS=3` repeats
every task and reports rates (`2/3  05-rename-symbol`) with a 95%
interval on the total. `make eval-baseline` keeps the last results as
`evals/baseline.json`; a later `EVAL_COMPARE=evals/baseline.json make
eval` pairs the two runs by task and prints the pass-rate delta with a
bootstrap interval, which tasks improved or regressed, and cost per
solved task:

```
pass-rate delta +8.3% [+1.7%, +15.0%] over 20 shared task(s) · significant improvement
cost per solved task $0.0031 → $0.0027 (-$0.0004)
  paired ×0.88 [0.75, 0.99] over shared tasks · real saving
context per solved task 26k → 27k (×1.03) · compactions 0 → 0
  paired ×1.03 [1.00, 1.05] over shared tasks · real increase
improved:  05-rename-symbol, 13-multi-file-package
```

Cost and context get the same paired bootstrap the pass rate gets, because
they are what a context-pipeline change is usually claimed on and this task
set swings 21% between identical runs. A ratio is printed with the interval
on the line beneath it and labelled `inside the noise` when it does not
clear 1 — a point estimate on its own is how this project once published a
"12% better" that had to be withdrawn.

The run also prints which tools the model actually reached for:

```
tool adoption over 15 run(s):
  read_file        15/15 runs (100%) · 252 call(s), 164 from inside a script
  execute_code      9/15 runs (60%) · 14 call(s)
  never called: expand_output, grep, repo_map, todo, write_file
```

A tool nothing ever calls is not a feature, and only a set of runs can say
so; `todo` is listed above for that reason and not by accident.

`--eval-report <file>` re-reads a saved results file and prints all of
that again, and with `--eval-compare <base>` the paired comparison of two
saved files — no provider, no API calls, no money. Re-analysing data you
already paid for should be free, and it means a published claim can be
checked against its own published data.

`EVAL_OFF=coach,prune` runs an ablation with named behaviours disabled
(`coach`, `critic`, `faults`, `loops`, `plan`, `prune`, `repomap`,
`richskip`, `script`, `syntax`, `todo`, `verdict`, `verify`, `working`);
the variant name is recorded in the results file so a comparison says what
it compared. The same names work as `JANETCODE_OFF=script` for a real
session.

## The two janetcode drivers in harnessbench

`harnessbench` can run janetcode two ways, and they measure different
things:

- `--janetcode-driver eval` (the default) drives janetcode's own eval
  runner: one process per batch of five, minimal agents, results read
  back out of `--eval-json`.
- `--janetcode-driver headless` drives it the way every other CLI in the
  comparison is driven: one `janetcode -p --output-format json` per task,
  with its result object read off stdout.

Both now start from a private home — `HOME`, `XDG_CONFIG_HOME`,
`XDG_DATA_HOME` and `XDG_CACHE_HOME` all point into a temporary
directory — so neither reads the operator's config, sessions, memory,
skills, MCP servers or permission rules, and both are given the same
model-call cap (`--eval-max-turns` on one side, `--max-turns` on the
other) with the tasks' `max_tool_calls` unenforced on both.

What is left is a real difference rather than a harness artefact: the
headless driver runs the whole product, so its agent carries the tools a
session has (`fetch_url`, `web_search`, `task`, `job`, `skill`, `memory`,
`goal_status`, `todo`, `tool_search`) where the eval agent carries nine.
That shows up as a larger prompt — measured at 1.5k–3k more input tokens
per task on evals 01–05 — and a correspondingly higher bill, and it is
the point of having both: one asks what the model can do, the other what
the product costs.

## Prompt A/B

The prompt is the other half of the harness, and it can be swapped for a
run without a rebuild. `JANETCODE_SYSTEM_MD=<file>` replaces the base
instructions (the environment section and the project instructions still
follow them: a prompt with no working directory or date is not one anybody
meant to write), and `--append-system-prompt "<text>"` — or `@file` — adds
text at the end. Both work for an eval run and for a real session.

A results file records which file replaced the base, what was appended and
a sha256 of the base as it was read, so two runs that differ only in their
prompt can be told apart afterwards, and a file that has since changed
cannot be mistaken for the one measured:

```sh
janetcode --eval evals --eval-runs 5 --eval-json base.json
JANETCODE_SYSTEM_MD=prompts/terse.md janetcode --eval evals --eval-runs 5 \
  --eval-label terse --eval-json terse.json
janetcode --eval-report terse.json --eval-compare base.json
```
