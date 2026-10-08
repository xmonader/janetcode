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

While it runs, every task prints `start <task>` to stderr when it begins
and, every thirty seconds until it ends, a progress line —
`… <task>: 2m30s elapsed, 14 tool calls` — so a slow model is not
mistaken for a hung run. Under the single-run table a legend explains its
terse columns: `cmp` is how many times the history was compacted
(summarised), and a cost of `?` means the model has no price, so the run
could not be costed.

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

`EVAL_OFF=coach,prune` runs an ablation with named behaviours disabled.
The names are the list `--eval-off` prints on a misspelling (and the
usage string of the flag): `accept`, `coach`, `critic`, `extratools`,
`faults`, `impact`, `intent`, `keeprevert`, `ladder`, `lineedit`, `loops`,
`memoryrank`, `mermaid`, `opentodos`, `oversized`, `plan`, `proof`, `protected`, `prune`, `recipe`,
`repomap`, `richskip`, `routing`, `script`, `scrub`, `sleep`, `syntax`, `thenrun`, `todo`, `undoabout`,
`verdict`, `verify`, `working`. The variant name is recorded in the results file so
a comparison says what it compared. The same names work as
`JANETCODE_OFF=script` for a real session.

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
  an error: a kept session needs some definition of done. A session that
  ran `/accept` and got its tests approved puts the approved command
  first (`<accept command> && <verify.json commands>`), since
  `verify.json` alone usually passes at the base commit already.
- **Files and locked** — an approved `/accept` session's acceptance test
  files, relative to the project. They are written into the work
  directory before the run and written again just before the check, so a
  run cannot pass by weakening them; the prompt that was the `/accept`
  request says they exist and must pass unchanged, which is the step a
  plain replay of the request would otherwise lose. A test file too large
  for accept mode to have kept its content is left out, with a warning.

Once the task is written, `/eval keep` prepares its work directory at
the start state and runs the check once. A check that already passes
there cannot fail when the task runs, and is reported as
"this check passes before any work — the eval cannot fail"; the task is
still kept. This runs as long as the check does, so the session shows as
busy meanwhile and esc cancels it.

Harness notes merged into a user turn (an `/undo` notice, a recalled
memory, a background job update) are not part of the kept prompts. A
reason you gave when denying a tool call is guidance delivered mid-turn;
a task's prompts are per turn and have nowhere to put it, so it is left
out and `/eval keep` says how many were dropped.

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

`evals/` holds eleven sets, and they are not interchangeable:

| set | where | what it is for |
|---|---|---|
| 01–20 | `evals/*.json` | the easy set: one prompt, one or two tool calls |
| 21–26 | `evals/*.json`, tagged `hard` | harder single prompts |
| 51–62 | `evals/*.json`, tagged `multistep` | the discriminating set (below) |
| 31–35 | `evals/large/` | 1M-token corpora, for context behaviour |
| 41–43 | `evals/session/` | multi-prompt sessions, for what survives across turns |
| 51–59 | `evals/fanout/` | one prompt over many files: the set the tool-surface, script, line-edit, intent and compaction-pressure ablations run on |
| 61 | `evals/explore/` | one change to find in a repository too big to read |
| 71–73 | `evals/gated/` | long tasks that ship a strict `.janetcode/verify.json` (build + vet + gofmt + tests), so `--eval-gate` has a gate that can actually fail |
| 81–86 | `evals/hard/` | six gap tasks ported from the mixai research repository: small Python programs whose stated behaviour a cheap model gets subtly wrong (occurs check, vector-clock concurrency, integer cent splitting, a regex engine that may not import `re`, deterministic topological order, a PN-counter merge). Each ships its `pytest` specification as a `locked` file, so an edit to the test is restored before grading, and a `.janetcode/verify.json` that runs the same tests inside the run, so `--eval-gate` has a gate that fails exactly when the task is unsolved. This is the set an escalation or advisor arm is measured on, because the easy sets are passed by every model |
| 91–98 | `evals/hidden/` | the held-out-acceptance set: small-to-mid Go repositories whose prompt states the whole specification, whose visible tests cover the happy path only, and whose grading tests are **held out** — they are never written into the work directory and are laid over a scratch copy by the gate alone (see `internal/eval/hidden.go`). This is the only set whose gate the model cannot run for itself, and so the only one on which a gate failure, a repair round, triage or a cascade escalation can be measured at all. 91–95 carry held-out tests and a reference solution proving them satisfiable; 97 and 98 are controls whose gate fails for a reason outside the repository (a credential the harness does not hold, a service that is not listening), so a triage skip or veto is scoreable in both directions. A control's failure text must not tell the model how to fake the environment: the first version of 97 asked for a binary named `svc-lint` and a run built one and installed it in `~/bin`; the first version of 98 named a port in the ephemeral range and a run bound it. Both now fail in ways an unprivileged process cannot arrange |
| 74–76 | `evals/routing/` | short tasks that exercise request routing: two one-look questions a cheap model should be able to answer, and one trivial edit that must stay on the strong one. They are the mixed set the intake-routing A/B needs beside the gated tasks, and they ship no gate — there is nothing for a gate to check in an answer |

`--eval` takes one file or one directory and does not descend, so
`--eval evals` is the top-level sets only and `--eval evals/fanout` is
that set alone. The numeric prefixes are unique within a directory, not
across them: `51` is the price-chain bug at the top level and the
many-files survey under `fanout/`, and the two never load together.

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

## Held-out acceptance tests

A verification gate the model can run for itself never fails. Every set
before `evals/hidden/` ships its grading tests inside the work directory,
so the model runs them before it ends a turn and the gate only ever
confirms — sixteen gate commands and zero gate failures on `evals/hard/`.
Triage, a repair round, a cascade escalation and a gate-triggered advisor
consult all fire on a gate *failure*, so none of them can be measured
against such a gate at all.

A task in `evals/hidden/` therefore has two test suites. Its `files` carry
a visible one that covers the ordinary path; its `hidden` map carries the
held-out one, and the runner never writes that into the work directory.
`hidden` is a flat map of file names; they are laid out as the package
`acceptance/`, in a directory of the runner's own outside the workspace,
beside a generated `gate.sh`. The gate copies the workspace into a fresh
scratch directory, discards anything the run put in `acceptance/`, lays
the held-out files over the copy, runs one command there (`hidden_check`,
by default `go test ./acceptance/`) and deletes the copy. `--eval-gate`
appends that script to the task's own verification commands under the
display label `acceptance tests`, so the script's path never appears in
what the model reads: the gate failure it is sent back with is the
command's output, which for `go test` is the failing test names and the
assertion messages, exactly what a CI failure shows.

Three properties make it an instrument rather than a set of files, and
each is a test:

- the held-out sources are never in the work directory, and neither the
  gate script's path nor the held-out directory appears in the failure
  text (`TestHeldOutTestsNeverReachTheWorkspace`);
- the fixture as shipped FAILS its held-out tests, and the task's
  `reference` solution — kept in the task file and never written into a
  work directory — PASSES them (`TestHiddenSetIsDiscriminating`);
- the control tasks' gates fail at base for a reason no code change can
  fix (`TestControlTasksFailTheirGateEnvironmentally`).

Because the held-out tests are their own package, they can only exercise
the module's *exported* API, and they never run the model's own tests.
That is deliberate twice over: it is the API's contract that a second
call site, a backward-compatibility promise or an error path is stated
in, and it keeps a file the model wrote out of the only process that can
see the held-out sources.

The residual, stated plainly: this is leak-proofing against discovery,
not against a model that deliberately writes exfiltration code. A package
the held-out tests import could print their source in an `init`, and the
output would reach the model. Nothing in a recorded session has done so,
and `TestHeldOutTestsNeverReachTheWorkspace` is what would catch the
accidental case.

### An eval run is not sandboxed

Worth knowing before writing any task whose checks mention something
outside the repository. The eval runner sets `AutoApprove`, and
`exec.go`'s approval test begins `!modes.AutoApprove`, so the bash tool's
`Guard` — which in a session puts every command that changes a file
outside the workspace in front of the user, whatever the permission mode
— approves itself in an eval. An eval run can therefore write anywhere
the user running it can.

That is not hypothetical. Building the control tasks produced both halves
of it in one evening: a run read a README saying the checks call a house
linter, compiled a binary, installed it as `~/bin/svc-lint` and made the
gate pass; another read that the checks publish to a local service on a
port, started a listener called `indexservice` and bound it. Both
"fixed" the environment rather than the code, and both destroyed the
control they were meant to be measured by — the second of them by
binding a port in the ephemeral range, which then broke the control's
own test for every later run on that machine.

The lesson for a task author is narrow and practical: **a gate step that
fails for an environmental reason must fail in a way an unprivileged
process cannot arrange.** A missing credential the harness does not hold
works, and so does a TEST-NET-1 address (RFC 5737) that cannot be routed
to; a named binary does not, a port in the ephemeral range does not, and
a low port only works on a host where nothing already listens on it —
this one had something on 81.

A task's `check` is `"$EVAL_HIDDEN_GATE" "$EVAL_DIR"`, so the grade and
the gate are the same judgement and "first-attempt pass" (the gate's
first verdict) and "final pass" (the grade) can be read apart.

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

## Measuring step routing

`route_cheap` (see [Configuration](configuration.md)) is an experimental,
off-by-default setting that sends a turn's mechanical steps to a cheaper
model. Its value has to come from an eval run, the same way any other
harness change does — not from turning it on and reading a transcript —
so `EVAL_ROUTE=<model>` (`--eval-route <model>` directly) sets it for a
run and records `route:<model>` as the variant, exactly like `EVAL_OFF`
records `off:<behaviour>` (both together are recorded, comma-separated,
when both are set):

```sh
make eval EVAL_RUNS=3 EVAL_JSON=evals/baseline.json
EVAL_ROUTE=claude-haiku-4.5 make eval EVAL_RUNS=3 \
  EVAL_JSON=evals/routed.json EVAL_COMPARE=evals/baseline.json
```

The comparison this prints is the whole case for the feature: the
pass-rate delta with its bootstrap interval, and cost per solved task
with its own paired interval (see "cost per solved task" above — nothing
new was added for this, the same numbers a prompt or a harness change is
judged on). What would justify turning `route_cheap` on by default in a
real project:

- the pass-rate delta's interval does not cross into a regression (the
  router must not be trading correctness for cost);
- the cost-per-solved-task ratio's interval is below 1 — a "real saving"
  in the comparison's own words, not a point estimate inside the noise;
- the saving survives on the `multistep` set (51–62) specifically, not
  only on the easy set — a turn with no headroom in it (see "The task
  sets" above) cannot show whether routing away the wrong step ever costs
  a task, and multistep tasks are the ones with steps to route in the
  first place.

A regression here is worth reading closely rather than discarding: the
router is deliberately simple (no ML, a short list of named rules — see
`internal/agent/steproute.go`), so a pass-rate drop on a specific task
usually names the rule that let a step through it should not have.

## Which model answered, and runs that never reached it

A result belongs to the model that produced it, and a gateway is free to
answer one model name with another model. On 2026-10-01 the ai02 broker
answered every name — Claude, DeepSeek, Qwen, MiMo — with Kimi for
hours, in the middle of an A/B; the "73% cheaper" it measured was the
swap, and the report had to be withdrawn
(`docs/benchmarks/jit/REPORT.md`). Nothing in the answers said so.

Every model call now records what its response said about who served it
— the response's model field, a gateway's `provider` field (OpenRouter's,
the broker's), the upstream's own model name when given, the shape of the
response id and of the tool-call ids — and checks it against the model
asked for. The check compares *vendors*, not names: a gateway renaming
`claude-sonnet-4-5` to `anthropic/claude-sonnet-4.5` is fine, and a host
is not a vendor (Amazon Bedrock serves Claude, a `gpu_octo` route serves
MiMo). It calls a mismatch only on evidence that names a different
vendor: a provider field like `"kimi"`, a model name of another family, or
a tool-id shape only one vendor mints (`toolu_…` is Anthropic's; `tool_`
and 24 characters is Kimi's, which is how the broker's swap gave itself
away while echoing the requested name everywhere else).

In an eval, each run gets one of four outcomes:

| outcome | meaning | counted |
|---|---|---|
| `PASS` / `FAIL` | the requested model answered; the check passed or not | yes |
| `INFRA` | the run ended outside the model — a 429 or 5xx after the retries, an exhausted balance, a rejected key, the network, a stream that stayed dead — or timed out without a single answer, or was cancelled before it started (a `--max-cost` stop) | no |
| `MISMATCH` | at least one call was answered by another vendor's model | no |

`INFRA` and `MISMATCH` runs are in neither the numerator nor the
denominator of the pass rate, are left out of the per-task comparison
(`--eval-compare` pairs only what both arms measured), and are reported
on a line of their own, with what they cost:

```
!! not scored: 15 answered by ANOTHER VENDOR'S MODEL than the one requested (provenance mismatch) · 4 lost to the provider or the network before the model could fail them — left out of the pass rate, the CI and the per-task comparison ($0.0412 spent on them)
```

Each result carries `infra`, `mismatches`, `mismatch` (the first
verdict) and `served` (the last call's provenance) in the results file.
An eval in which no run reached the requested model exits non-zero
rather than passing on nothing, and `--eval-strict-provenance` makes any
mismatch fail the whole eval. In an ordinary session the same check says
it once — "requested xiaomi/mimo-v2.6-flash, but the response was served
by moonshot, not xiaomi (tool ids tool_24)" — and each assistant event
in the session log carries `served` when it changes and `mismatch` when
it disagrees.

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
| `--eval-route <model>` | set `route_cheap` for the run (experimental step routing) and record the variant, like `--eval-off` |
| `--eval-timeout <seconds>` | replace every task's own timeout, so a benchmark gives each harness the same wall budget |
| `--eval-max-turns <n>` | cap the model calls one task may spend (`--max-turns` is the same cap for a real run) |
| `--eval-no-tool-limit` | ignore each task's `max_tool_calls`, for comparing against harnesses that enforce no tool budget |
| `--eval-strict-provenance` | fail the whole eval when any run was answered by another vendor's model than the one requested, instead of leaving those runs out (see [Which model answered](#which-model-answered-and-runs-that-never-reached-it)) |
| `--eval-gate` | run tasks under a verification gate (the work directory's `.janetcode/verify.json`, else the checks for its project type) and wire the configured decision provider, so failure triage can be measured. Off by default: a gate changes tool counts, pass rates and cost, so results recorded without it are not comparable with results recorded under it |
| `--eval-compact-at <tokens>` | compact earlier than the default, to force compaction on tasks that would not otherwise reach it |
| `--eval-facts <n>` | after each run that compacted, probe up to n facts mined from what compaction removed and record how many the model still recalls |
| `--eval-facts-blind` | probe without `expand_output`: what the compacted context alone retains |
| `--eval-train` | fit the trajectory critic on the runs recorded so far |
