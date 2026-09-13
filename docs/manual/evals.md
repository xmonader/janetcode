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

## Keeping a session as an eval

Every task above starts from a prompt someone wrote by hand. `/eval keep
[name] [--base <rev>] [--self-contained] [--check <cmd>]` writes one from
a session that actually happened: a bug that got fixed, a feature that
got built, whatever the conversation in front of you turned out to be.
It is idle-only — it reads the session log, not the model — and it needs
a git repository, because what it writes down is not just what was said
but the tree it was said over:

- **Prompts** — the session's own user turns, in order, so replaying the
  task sends the same conversation the session had (tool results,
  slash commands and mid-turn steering notes are not prompts and are
  left out).
- **Repo and Base** — the commit `HEAD` pointed to when the session's
  first turn ran. This is recorded once, automatically, the moment a
  session's first turn starts in a git repository (see
  [sessions](sessions.md)). A session begun in a repository with no
  commits yet has an empty `Base`, and is still kept: the whole starting
  tree travels as `patch` instead (see below). A session with no
  recorded start at all — one begun before this feature existed, or
  where recording failed — has nothing to fall back on and needs
  `--base <rev>` instead, which resolves `<rev>` to a commit and uses it
  as `Base` in place of anything recorded (a session that did record a
  start still honors `--base` over it, when both are given). `--base`
  keeps no `patch` of its own: it names a starting commit, not a
  starting moment, so there is nothing uncommitted to carry.
- **Patch** — a `git diff --binary` of whatever was uncommitted at the
  session's recorded start, so an experiment that started mid-edit
  replays mid-edit too. Omitted when there was nothing uncommitted, or
  when `--base` was given without `--self-contained`.
- **Check** — `--check <cmd>`, or, when that is left off, the project's
  `.janetcode/verify.json` commands joined with `&&`. Neither present is
  an error: a kept session needs some definition of done.

The task is written to `.janetcode/evals/<name>.json` (plus a
`<name>.patch` beside it when there is one), where `<name>` defaults to a
slug of the session's title. `/eval keep` refuses to overwrite an
existing task; pass a different name instead. Running it later is the
same as any other task:

```sh
janetcode --eval .janetcode/evals
```

A task with `base` set runs in a detached git worktree of `repo` checked
out at that commit, with `patch` applied, instead of an empty directory —
so the base commit has to exist in whatever clone runs it, and a run
against one that does not fails with a clear error naming the fix rather
than a raw git message. Committing `.janetcode/evals` (task, patch and
all) into the repository is what makes that true for everyone who clones
it; when it is not — a session kept before its base was ever pushed — the
same check `/eval keep` used to decide this shows up as a warning when
you keep it: push the base commit, or re-keep with `--self-contained`.

A task with no `base` but a `patch` (an unborn repository at session
start, or any `--self-contained` keep) runs in a plain, freshly
`git init`-ed directory with `patch` applied instead of a worktree, so it
never depends on `repo` having that commit at all. `--self-contained`
asks for this on purpose, trading `base`'s smaller patch (just what was
uncommitted) for a self-sufficient one (the whole starting tree, as a
diff from the empty tree) — the way to keep a session whose base commit
you do not intend to push, or hand a task to someone who has no access to
`repo` at all.

## The task sets, and which one an ablation can use

`evals/` holds four sets, and they are not interchangeable:

| set | where | what it is for |
|---|---|---|
| 01–20 | `evals/*.json` | the easy set: one prompt, one or two tool calls |
| 21–26 | `evals/*.json`, tagged `hard` | harder single prompts |
| 51–62 | `evals/*.json`, tagged `multistep` | the discriminating set (below) |
| 31–35 | `evals/large/` | 1M-token corpora, for context behaviour |
| 41–43 | `evals/session/` | multi-prompt sessions, for what survives across turns |

An ablation is only as good as the headroom in the set it runs on. On
qwen3.7-flash the easy set passes 60/60 and the hard set 87/90 across
five harnesses, so no harness change can move either: a feature worth
nothing and a feature worth a lot both score 100%. That is what the
`multistep` set (51–62) exists to fix. Each is a single prompt a capable
model gets right only if it plans, runs something and reads the result
back:

- 51 a wrong total that is two bugs in two files under the failing test
- 52 a signature change across a package, three callers and its tests
- 53 a CLI with six rules, every one of them exercised
- 54 a data script against a fixture whose edge cases the prompt does
  not list, then re-run on a second fixture it has never seen
- 55 a value that appears nowhere in the tree because it is a product,
  so it has to be traced through a four-layer config loader
- 56 two failing tests with unrelated causes
- 57 three packages that depend on each other in order, from nothing
- 58 a migration judged on a golden file and on a shape the golden does
  not cover
- 59 a memoising cache that has to be race-free and compute once per key
- 60 a contract where the obvious fix for the failing test breaks the
  passing one
- 61 a semver library and CLI in node, boundaries on every range form
- 62 a shell validator with six rules and a third fixture

Every check is offline and deterministic; several append hidden tests the
model never sees, and several re-run the model's work on an input it was
never given, so a solution that fits the visible fixture and nothing else
fails. `max_tool_calls` is 45–60 throughout and the budget is ten
minutes, so neither the tool limit nor the clock decides a result.

`--eval-only multistep --eval-runs 3` is the run; `docs/benchmarks/multistep/`
has the calibration and what the ablations measured on it.

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

## Every eval flag

The environment variables above are what `make eval` sets; each is a flag
on `janetcode --eval <file-or-directory>` and can be given directly.

| flag | what it does |
|---|---|
| `--eval <path>` | run the tasks in this file or directory and exit |
| `--eval-jobs <n>` | tasks to run at once (default 2) |
| `--eval-only <names>` | run only these tasks, by name or tag |
| `--eval-runs <n>` | repeat every task this many times, so the report is rates rather than coin flips |
| `--eval-json <file>` | write the run's numbers for a later comparison |
| `--eval-report <file>` | re-read a saved results file and print it again |
| `--eval-compare <file>` | pair against a baseline results file and print the delta with a bootstrap interval |
| `--eval-label <text>` | name this variant, so two runs can be told apart in a results file |
| `--eval-keep` | keep the work directory of every task, to look at one that failed |
| `--eval-off <behaviour>` | turn one harness behaviour off for the run: an ablation |
| `--eval-timeout <seconds>` | replace every task's own timeout, so a benchmark gives each harness the same wall budget |
| `--eval-max-turns <n>` | cap the model calls one task may spend (`--max-turns` is the same cap for a real run) |
| `--eval-no-tool-limit` | ignore each task's `max_tool_calls`, for comparing against harnesses that enforce no tool budget |
| `--eval-compact-at <tokens>` | compact earlier than the default, to force compaction on tasks that would not otherwise reach it |
| `--eval-facts <n>` | after each run that compacted, probe up to n facts mined from what compaction removed and record how many the model still recalls |
| `--eval-facts-blind` | probe without `expand_output`: what the compacted context alone retains |
| `--eval-train` | fit the trajectory critic on the runs recorded so far |
