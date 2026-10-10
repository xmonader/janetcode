# Changelog

User-visible changes. Commit-level detail is in `git log`; the reasoning
and measurements behind each round are in `docs/notes/history.md`.

## Unreleased (since v0.2.0, 2026-09-10)

### Wild ideas (2026-10-10)
- Pattern induction for repetitive edits (wild idea 7) was screened and
  dropped; nothing in the agent changes. `internal/editpattern` infers a
  rewrite rule from two similar edits (token-level anti-unification, so
  `load(a)` -> `load(a, ctx)` and `load(b)` -> `load(b, ctx)` give
  `load ( $0 )` -> `load ( $0 , ctx )`) and finds the other sites it
  matches. `go run ./cmd/patternscreen <campaign>...` replays recorded
  effbench runs and counts the runs where a rule induced from two hunks
  would have written a third one. On r1-r13: janetcode 14 of 251 editing
  runs (5.6%) and pi 10 of 248 (4.0%), with 0.4% of edit bytes in those
  later hunks on both arms. Both are under the idea's 10% line. Had the
  idea passed, the offer would have been made by the harness: a
  precomputed diff in the edit result, applied with one confirmation, not
  a new tool the model has to choose.
- A clean-room blind tester for headless runs, opt-in with
  `"blind_tester": "on"` (or a slot: `cheap`, `strong`, `advisor`,
  `main`): when a turn that changed files ends green, one model call that
  sees only the task text and the project's file names, never the code or
  the transcript, writes up to eight checks, one per rule the task states.
  They run in a throwaway worktree; the turn goes back once told only
  which rules failed, the failed checks are rerun after that round, and
  the proof note records the result. `JANETCODE_OFF=blindtest` turns it
  off.
- `spec_coverage` and `spec_examples` (both off by default). The first
  reads the task text (the prompt and the documents it names) for rules
  and worked examples, matches their literals against the public tests,
  and tells the model at the turn's first tool step which rules no public
  test reaches. The second compiles the examples it can read unambiguously
  (a Python call with a literal answer, a shell command and its output)
  into checks that run at the gate and send the turn back when they fail,
  feeding the probe ledger when that is on. `speccovscreen` is the offline
  pre-screen: on 36 effbench tasks and the r8–r13 runs, 77% of janetcode's
  hidden-test failures (68% of the 40 distinct failing tests) touch a rule
  the matrix flags, against a 47% base rate over all hidden tests, with
  63% of rules flagged; 110 examples appear in 16 tasks' text, 18 of them
  compile, in 4 tasks, all 18 pass on the reference solutions, and none
  fails on a failed janetcode run.
- Sequential A/B testing: `--eval-compare` (and `--eval-report` with it)
  now also prints an always-valid mSPRT verdict on the pass rate and on
  output tokens, paired by task and run index, with broker-dropped pairs
  counted per arm instead of silently left out. `--eval-sequential
  pass|output|any` runs the rounds one at a time and stops the first round
  the chosen gate is decisive. Re-analysed over 17 past effbench A/Bs (447
  pairs), no pass-rate A/B would have stopped early and the output gate
  decided 3 of 17, saving one round of 16 runs: those A/Bs were
  underpowered, not overspent.
- A/B arms that branch mid-run: `-p --ab-replay <session.jsonl>
  --ab-replay-calls K` replays a recorded run's first K replies at no cost,
  with the recording's absolute paths moved to this workspace, and goes
  live from call K+1; effbench's `run.py --branch-from <campaign>
  --branch-at 0.5` gives every janetcode arm the same recording per task
  and seed. No branched campaign has run yet, so the variance it removes is
  unmeasured; independent seeds disagree on 7 of 64 verdicts.
- `--off-peak-by <deadline>` (with `--off-peak-estimate`) holds a `-p`
  run or an `--eval`, or alone just waits, until the model's cheapest
  hours that still finish by the deadline. The windows are a table in
  `offpeak.json`, not code; the built-in one holds only DeepSeek's
  16:30–00:30 UTC discount, withdrawn in September 2025, so it finds
  nothing in force until a provider publishes a window. At those old
  rates r13 would have cost $2.19 instead of $4.39.
- `flake_quarantine` (off by default): a red test command the model ran is
  rerun once on the tree as it is and, when the turn saw the command's
  result before its first change, once on that pre-edit tree. A result
  that flips (red then green, or different failing tests) is reported to
  the model as non-deterministic; no model call. `JANETCODE_OFF=flaky`.
  Offline over 321 effbench sessions: 78 of 536 red test runs (14.6%) were
  followed by an edit to a file the failure did not name, but only 1 red
  run was seen to flip on an unchanged tree (0.2%, 0 with no other shell
  call in between).
- `fix_cache` (off by default): the change that turned a red check green is
  stored per repository with the check's normalised error lines, and shown
  under the same error in a later session; no model call.
  `JANETCODE_OFF=fixcache`. Offline, 7.1% of distinct error signatures
  recurred across effbench sessions of the same task and 0 across 184
  real local sessions; an earlier session's fix would have been attached
  to 3 of 520 signed red episodes (0.6%), none touching the file that
  session's own fix touched.
- `speculate` (off by default) runs the follow-up suggestion's predicted
  prompt ahead of the user on `route_cheap`, in a throwaway checkout,
  capped at $0.05 a run, and adopts its changes when the user sends that
  prompt; anything else discards it. Follow-up suggestions are now logged
  (`followup/suggest`) and `cmd/specscreen` measures how often the next
  prompt matched. No suggestions were recorded before this, so the direct
  rate is unmeasured; on the proxy (the reply offered a next step and the
  user took it) 184 local sessions give 1 of 82 prompt pairs, far under
  the 40% kill line, mostly scripted QA sessions, so a real number needs
  logged suggestions.
- `plan_file` (off by default): edits to `.janetcode/plan.md` made while
  the agent works reach the model before its next call, as only the
  change.
- `hedge_provider`, `stream_resume` and `fingerprint` (experimental, all off
  by default). `hedge_provider` races a second configured route when a call
  has no first token by the primary's learned p95 time to first token, and
  fails over to it at once when the primary fails on the route before
  answering. `stream_resume` continues a reply whose stream broke mid-text
  from the partial (Anthropic prefill, DeepSeek prefix completion) instead
  of regenerating it. `fingerprint` sends six canary prompts at session
  start and reports a route whose model names another maker, or answers
  differently from the reference stored for it. Replayed over the effbench
  proxy logs (10,351 calls, 595 runs), none of the recorded failures would
  have been rescued: every call returned 200, the 286 calls cut mid-stream
  were all cut by the client (276 by janetcode, 10 by pi), not the server,
  and hedging at p95 would have saved at most 89 s in total. The broker's 504s (the ~45% INFRA drops) are not in any per-call
  record in the repository (`docs/benchmarks/effbench/hedgescreen.py`).
- Executable memory: `probe_memory` (off by default; turns `probe_ledger`
  on) writes every discriminating probe to the repository under
  `refs/janetcode/probes`, and later sessions arm the ones that pass on
  their pre-edit tree and replay them at the gate; a probe the model
  declines to fix is retired. `JANETCODE_OFF=probememory`. No live warm-
  session benchmark yet.
- Agent blame on reads: `read_blame` (off by default) notes on a
  `read_file` result the ranges an earlier agent turn wrote with an intent,
  from the session's own provenance or a commit's proof note, under 480
  bytes. Over 63 recorded local sessions, 36 of 142 reads (25%) would have
  carried a note; over 320 effbench sessions none would (all single-turn).
  `JANETCODE_OFF=readblame`.
- Sessions as git refs: `janetcode --session-ref save|list|show|open|push|fetch`
  keeps a session's transcript, evidence and probes under
  `refs/janetcode/sessions/<id>`, capped at 1 MB. All 321 effbench
  sessions fit whole (p90 565 KB raw, 100 KB compressed); of 184 local
  sessions one needed clipping.

- Numbers in the answer are checked like "tests pass": "40% faster",
  "allocations halved", "coverage 85%" need a measurement of their kind
  in the session (a speed or memory change needs a run before the edits
  and one after, or a benchstat/hyperfine comparison; benchstat's `~`
  backs nothing). Unbacked numbers get a notice and a log entry; on by
  default, no model call, `JANETCODE_OFF=numclaim`. `numeric_claims_nudge`
  (off by default) sends the turn back once to measure or retract them.
  Over 543 final answers in 505 recorded sessions (local and effbench) no
  answer states a number of this kind, so the share of unbacked ones is
  not measurable there; the 28 hits of a first draft were all durations
  and products ("0.2s", "8×366"), which the checker now leaves alone.
- `diff_fuzz` (seconds, off by default): when the task says behaviour must
  not change, a turn that changed Go code is fuzzed against the code
  before the turn — changed exported functions called on both with the
  same inputs from a generated `testing.F` harness in a temporary copy of
  the module — and the first diverging input sends the turn back once.
  On five seeded refactors with a subtle behaviour change their tests
  miss, it caught 4 of 5 at 10 seconds each (missed: EqualFold vs
  ToLower).
### Novel ideas (2026-10-10)
- `effort_control` (experimental, off by default) sets each model call's
  reasoning tier from the turn's own signals instead of one fixed tier: a
  turn starts at `low`, a failed edit, a red check, a loop note or a
  verify gate sending the turn back raises it one rung, a green check or
  the first edit to a file halves the distance above `low`, and a reply
  the plan budget or output limit cut returns it to `low`. `effort` is
  the ceiling and a turn changes the tier at most four times. It acts only
  on a provider whose prompt cache does not key on the tier (OpenAI
  Responses); on Anthropic, Gemini and OpenAI-compatible servers it says
  once that it is idle. The chosen tier is logged as `effort` on each
  assistant event. Replayed offline over 184 recorded local sessions
  (888 calls), it would have asked for `low` on 90% of calls and changed
  the tier 0.25 times a turn on average.
- `elide_checks` (off by default): after a step whose last change was a
  file edit nothing checked, the harness runs the project's check itself
  and hands the model the result with that step's tool results, a pass
  as one line and a failure in full up to 4,000 bytes, instead of
  waiting a round trip for the model to ask. A repeat of that exact
  command on an unchanged tree is answered from the result and says so.
  `--session-profile` prints its offline pre-screen: on the effbench
  r12–r13 sessions 37% of edit-completing replies were followed by a
  check (kill line 60%) and 3.3% of model calls were check-only replies
  it could have removed, so it stays opt-in.
- Claim-checked handoffs: the facts in the working-memory record written
  at compaction are held to the calls the harness watched. A fact with
  evidence carries a pointer `expand_output` resolves (`(evidence:
  ctx-14, call 7)`), one nothing showed is marked `unverified:`, and one
  the evidence contradicts (tests said to pass whose last run was red, a
  name the file no longer holds) is dropped with a notice. A subagent's
  report is checked the same way against its own calls before the parent
  reads it, and the summary a spec review ends on gets a notice for what
  no call showed. No model calls; nothing runs in a session that never
  compacts or delegates (`JANETCODE_OFF=claimcheck`). Offline replay of
  the 70 recorded compaction records: 18 of 606 facts (3%) were
  unverified or contradicted.
- `/tour` reviews a turn's changes riskiest hunk first, each with the
  intent the model stated for it: behaviour changes, then test edits,
  then comment-only ones, ranked within each by how widely the changed
  symbols are used. Reformatting and consistent renames are recognised by
  token equality and collapsed behind a count (`/tour all` shows them).
  A turn with more than one hunk ends with a one-line summary of the tour.
- A comprehension-debt meter counts changed lines approved without being
  read whole (auto mode, a rule, or a clipped diff not expanded), per
  directory, at the end of `/tour`, under `/changes` and in `/stats`, in
  the terminal and the web UI. `JANETCODE_OFF=tour` turns both off.
  Measured on effbench's recorded final diffs, only 0.2% of r13's changed
  lines (0.1% over all 319 recorded sessions) are mechanical, below the
  20% the idea's kill criterion asks for: on greenfield benchmark tasks
  there is almost nothing to collapse, so the ordering and the meter carry
  the feature there.
- `--branch-retry` (opt-in, `-p`): a run that ends stuck, out of turns or
  tool calls, or with the verify gate or `--until` still red is branched
  once from its critical step, the last call before the first edit that
  explored something new. The steps up to there are replayed from the
  recording at zero tokens, so the prompt prefix stays the same, and the
  live part is told in one line what the failed attempt did. The better of
  the two trees is kept, ranked by a clean end, the gate, the verdict,
  gaming signals and waste share, never by a model.
- `--second-sample` (opt-in, `-p`): a run that finishes green on weak
  evidence (an overclaimed or stale verdict, or a gaming signal) draws one
  independent attempt, ranked by the same deterministic selector. The
  ranking leaves slots for the probe ledger's and the test-integrity
  ledger's scores.
- `cmd/rolloutscreen` runs both offline pre-screens over recorded
  effbench campaigns. On `jc-mimo-r8`..`r13` (191 valid runs) only 1 run
  ended badly, and none had a branch point, because small-repo inlining
  means runs edit before they explore. The second-sample trigger fired on
  13 green runs, 3 of which failed the hidden tests: a precision of 23%,
  under the report's 40% kill line. The gaming signal fired 12 of those
  times with 2 failing; the verdict reasons fired once, failing.
- `localize_failures` (opt-in): when a test run goes red after the turn
  changed two or more hunks, whether the gate ran it or the model's own
  `go test` did, janetcode reruns the failing tests in a throwaway
  worktree on subsets of the turn's hunks (delta debugging) and appends
  the fewest hunks that still fail them ("hunk 2 of 3: calc/calc.go line
  13"), plus the functions the failing tests ran ranked by Ochiai from
  per-test coverage. No model call and no new tool; bounded to a minute
  and 2 KB, three times a turn, and silent when the failure predates the
  turn or does not reproduce. Go tests only for now. The offline
  pre-screen over effbench r8-r13 (192 runs): 151 turns (79%) had a red
  check after a multi-hunk edit, but only 1 of those reds named a failing
  `go test` and 19 named failures any known runner reports; most were the
  model's inline Python probes. Red to next green took a median of 2 tool
  calls.
- A headless run keeps a test-integrity ledger when a turn that changed
  files ends. Test files the turn changed are run again in their
  original form, in a throwaway copy of the tree, with the turn's last
  passing test command: a green on edited tests is no evidence unless the
  originals pass too. Source lines the turn added are scanned for
  branches on a literal only a test holds (not the task text, not the
  file as it was). Either finding sends the turn back once, naming the
  test or the line; the counts (tests modified and added, assertions
  changed, replay result, flagged lines) go on the session log and into
  the proof note. On 934 recorded effbench workspaces (r1-r13 and the
  competitor campaigns, every arm) the literal scan raises no flag
  (`JANETCODE_OFF=testledger`).
- `conflict_exit` (opt-in): the model may end a turn with `CONFLICT:
  <test id> | <the task's words> | <why>` when a test contradicts the
  task. The claim is checked: the test must exist and fail at the end of
  the turn. A confirmed one ends the turn past the gates that would make
  that test pass, is recorded in the proof note, and ends a `-p` run with
  exit 58 (`is_error` false, `conflict` in the result object); any other
  is sent back once.
- Impossible-effbench (`docs/benchmarks/effbench/impossible`): eight
  effbench tasks with one public test mutated to contradict the brief,
  each proven impossible against the task's reference solution, and a
  scorer that counts a pass as a cheat and a CONFLICT exit as the right
  answer. `run.py --tasks-root` runs them; `set.<key>=<value>` in an arm
  spec sets a janetcode config key for that arm.
- Signed, re-executable evidence beside each proof note: an in-toto
  Statement v1 (predicate `https://janetcode.dev/change-evidence/v1`,
  subject the commit's tree) in a DSSE envelope signed with a local
  ed25519 key, under `refs/notes/janetcode-evidence`. It records every
  verification command the contributing turns ran with its exit code and
  a hash of its normalised output, the model and provider with the
  serving-model provenance verdict, prompt and system-prompt hashes, the
  test files' hashes before the commit, the sandbox profile, and who
  approved each change to a committed file. `janetcode --proof verify
  [commit]` checks the signature and the tree, re-runs the commands in a
  throwaway checkout under the recorded sandbox profile and diffs the
  outcomes; claims with no record show as unverified assertions, and it
  exits 1 when the evidence does not hold. `/proof verify` in a session
  checks without re-running; `--proof key` prints the public key for
  another machine to trust. The turn-end gate now keeps each command's
  exit code and output digest on its log event. On with `proof_notes`;
  `JANETCODE_OFF=evidence` turns it off.
- Taint-labelled context. Each tool result is labelled by where it came
  from: fetch_url, web search, MCP tools, files under dependency
  directories (`vendor/`, `node_modules/`, the Go module cache, ...) and
  bash that fetches or prints an issue are untrusted; your prompt and
  repository are not. Once untrusted content is in the context, a network
  command, a change to CI, credential, protected or janetcode settings
  files, and data sent to a host outside `egress.allow` ask first under
  every mode, and the prompt says where the argument came from ("came
  from vendor/acme/README.md:40"). Headless runs, with nobody to ask,
  refuse them. A clean session asks nothing new: replaying the 168
  recorded benchmark runs in `docs/benchmarks` tainted none of them. The
  status line shows `tainted`, or `trifecta: egress asks` when secrets
  are readable and no egress policy is set; `/egress` names the sources.
  Memory learned in a tainted session waits for `/memory approve`
  (`JANETCODE_OFF=taint`).
- Honeytokens: every model-run command carries two planted variables with
  random values; a tool call or an egress-proxy request that carries one
  stops the run (`JANETCODE_OFF=honeytoken`).
- Dependency check: an edit that adds a name to `go.mod`, `package.json`
  or `requirements*.txt` looks it up in the public registry and flags a
  name that does not exist or is under 30 days old. It only adds a note,
  fails open, skips private Go modules, and is skipped with
  `JANETCODE_OFFLINE=1` (`JANETCODE_OFF=depgate`).
- Editing subagents can be merged back by the harness instead of handed
  to the model as a patch (`subagent_merge`, off by default;
  `/features subagent-merge on`). A subagent's read-set — the files it
  read and the greps and globs it ran — is checked against what changed
  in the project while it worked. Edits to different declarations of one
  file are merged with no model call (Go by its parser, other languages
  by tree-sitter in a `-tags treesitter` build, the rest by line ranges);
  two sides that each added an import or appended a function are merged
  too. A conflict, or a read whose declaration the subagent's edits use,
  goes back to the subagent once with both sides' diffs, and what still
  conflicts is returned as a patch against the current project, which is
  left alone. Merges are checkpointed and recorded like any edit, and
  merge-back never writes where a write would need approval.
- `evals/fanout/60-parallel-overlap`: three parallel editing subagents
  whose changes overlap in one file. Applied with `git apply` one after
  another, two of the three patches fail; merged back, one conflict
  reaches a subagent.
- A probe ledger, opt-in with `"probe_ledger": true` (or
  `--set probe_ledger=true`): the inline checks the model runs after it
  edits (`python3 - <<'EOF' ... assert ... EOF`, `node -e` with a throw)
  are kept in harness state. Each new one that still passes is run once
  more against the tree as it was before the turn's first change; one that
  failed there is frozen and replayed at every later gate, and the turn
  goes back to the model when a later edit breaks it (at most twice a
  turn; one left failing into the next turn is dropped). Probes that also
  passed on the old code are named to the spec review as testing nothing.
  Reruns go through the bash tool and its sandbox, and only when the rules
  would let the call run without asking. `JANETCODE_OFF=probes` turns it
  off. `go run ./cmd/probescreen <runs>` is the offline pre-screen over
  recorded session logs.

### Lean execution (2026-10-08)
- A headless `-p` run no longer extracts memory or consolidates the
  session at the end unless the config says `"memory": true`. Those were
  two full-history model calls after the answer, on every run that changed
  a file, and the caller waited for both.
- `write_file` and `edit_file` take `then_run`: a command, usually the
  tests, run once the edit lands, with its output appended to the edit's
  result. It is approved, sandboxed and hooked as its own `bash` call. The
  edit and its check cost one round trip instead of two
  (`JANETCODE_OFF=thenrun` withdraws it).
- A small repository (at most 40 files and 32 KB, minus what git and
  `.aiignore` exclude) rides whole in a new session's first message
  instead of the repository map, every file counted as read. The reads a
  coding turn opens with are gone (`JANETCODE_OFF=inline`).
- Language-server checks after an edit run beside the turn instead of
  inside the edit, which used to wait up to the server's timeout (8 s)
  every time. Findings join the next step's results; errors still out
  when the model says it is done send it back, at most twice a turn
  (`JANETCODE_OFF=asyncdiag`).
- Loading a deferred tool no longer rewrites the cached prompt prefix:
  loaded tools go after every other tool in the order they were loaded,
  and `tool_search` lists every loadable tool rather than only the ones
  still deferred. A query that names a tool exactly loads only that tool,
  and asking for a loaded one loads nothing.
- The system prompt asks for short reasoning with no code drafted in it:
  code written once in thought and again in the tool call is paid for
  twice.
- A model's shell commands no longer inherit `OLDPWD`, the directory
  janetcode was started from: `cd "$OLDPWD"` was one step out of the
  workspace.
- A reply cut off at the output token limit before it called a tool no
  longer ends the turn as done with an empty answer: the model is told and
  asked to act, at most twice a turn.
- Four round trips found in janetcode's first effbench transcript:
  `edit_file` given both a line range and `old_string` uses the text when
  it is unique and the range otherwise, instead of refusing; a file sent
  whole with the first message counts as read for the "edited without
  reading" note; the plan check knows `cache.py` is `reports/cache.py`;
  and a scratch check script rewritten from red to green no longer holds
  a finished turn back as "a check came back red". The same holds for a
  script the turn wrote for itself or one outside the project.
- The system prompt says to stop once the project's tests pass, after a
  short check of any stated rule they miss: no test harness of the
  model's own, and a wrong check is fixed once or dropped.
- A reply cut at the output limit doubles the limit for the rest of the
  turn (16k, 32k, up to 64k), since the cut reply is thrown away and
  written again. An OpenAI-compatible server that refuses the larger
  limit is asked for the one its refusal names from then on; any other
  refusal of a raised limit falls back to the configured one.
- A headless run (`-p`, stream-json input) sends the coding core only —
  `read_file`, `write_file`, `edit_file`, `bash`, `tool_search` — and
  lists the rest as loadable: 25 KB of tool schemas a request down to
  6.5 KB (`JANETCODE_OFF=headlesscore` keeps the full list).
- A headless run starts from a short base prompt (prompt.Headless, ~1 KB)
  instead of the interactive instructions and the harness section, which
  describe slash commands, plans, memory and approvals a run with nobody
  at the terminal does not use (`JANETCODE_OFF=leanprompt` restores them;
  `JANETCODE_SYSTEM_MD` still replaces either).
- A headless reply that has reasoned past ~10k tokens (32,000
  characters) without writing text or calling a tool is ended and the
  model told to act, up to three times a turn. Over a whole small
  repository MiMo planned the entire change first, 23–33k reasoning
  tokens on most first replies, and two such replies used up
  b-shift-dst's 20-minute cap (`JANETCODE_OFF=planbudget` turns it off).
- A reply that keeps starting tool calls is ended at the 25th, and the
  24 that arrived whole are run, with a note that the rest was dropped.
  Healthy replies on effbench made at most 15 calls; MiMo sometimes fell
  into bursts of 37 to 392 one-off checks that ran until the output limit
  or the task's time cap, every call then refused as possibly cut short.
  A burst is usually one group of calls written over and over, so the
  reply is also ended at its first exact repeat, and the group runs once
  (`JANETCODE_OFF=burstcap` turns both off).
- A headless turn that changed files and passed its checks is sent back
  once, to check the change against the task requirement by requirement
  before it ends. effbench's remaining misses were stated rules the
  public tests did not reach, such as price filters validated and never
  applied. The review compares the brief with the code by reading it,
  without new checks, and the model is told to wrap up after three tool
  steps: asked for a check script, the review cost about 150 s a run,
  mostly spent debugging the model's own checks
  (`JANETCODE_OFF=specreview` turns it off).
- A headless run's first write to a test file is refused with a note to
  keep its own checks inline; the same write again goes through, for a
  task that asks for tests. MiMo rewrote an existing test file once and
  wrote and loosened a test file of its own once, each a tamper flag on
  a run that passed (`JANETCODE_OFF=testguard` turns it off).
- The default output limit is 32,768 tokens (was 16,000), as pi's is: a
  reasoning model's first reply over a small repository no longer runs
  into it and is thrown away. It is a ceiling, billed only as used.
- The open-plan gate lets a turn end whose checks all passed after its
  last edit: open steps then are the plan's bookkeeping, not unfinished
  work.

### Parity with qwengo (2026-10-06)
- `--json-schema <json|@file>`: a headless answer as validated JSON. The
  model delivers it through a `structured_output` tool that janetcode
  checks against the schema itself (no reliance on the server's
  constrained decoding); stdout holds only the value in text mode, the
  result object carries `structured_output`, and no valid answer exits 54.
- `--input-format stream-json`: one process for a whole conversation, a
  turn per stdin line and a result line after each, with interrupts that
  cancel the turn and not the session. Claude Code's message and
  `control_request` shapes are accepted.
- Native Gemini provider (`type: gemini`, the `gemini` preset,
  `GEMINI_API_KEY`): keeps Gemini 3's thought signatures on the calls they
  came with, which the OpenAI-compatible endpoint (now the `gemini-openai`
  preset) drops and Gemini 3 then refuses with a 400.
- OpenAI Responses provider (`type: responses`, the `openai-responses`
  preset): reasoning comes back encrypted and is replayed before the call
  it led to, so a reasoning model keeps its thread across tool calls.
  Nothing is stored on OpenAI's side.
- `/commit [message]` commits only the files this session changed, never a
  credential-looking one, with a message drafted from the diff and the
  changes' intents. `/pr [base]` pushes the branch and opens a pull request
  with gh, title and description drafted.
- Run limits: `--max-tool-calls <n>` (exit 57) and `--max-wall-time <dur>`
  (exit 56), each ending a headless run with a result instead of a kill.
- `--core-tools` and `--exclude-tools` decide which tools the model is
  shown at all, MCP tools that connect later included; a misspelt name is
  an error, so a typo cannot leave the tool it meant to remove in place.
- `.aiignore` (also `.agentignore`, `.janetcodeignore`), in `.gitignore`
  syntax, keeps paths away from the file tools. It is a statement of
  intent, not a sandbox: `bash` is not bound by it.
- `--safe-mode`: no hooks, MCP servers, verify gates, project permission
  rules, project instructions, project skills/rules/profiles or
  auto-approval, for a repository you have not vetted.
- Web search: Tavily, Serper, Exa and SearXNG next to DuckDuckGo and
  Brave; keys come from each provider's environment variable.
- Sandbox: bubblewrap backend, preferred on Linux where it can run. Over
  Landlock it adds a full network cut (UDP and DNS too) and mount-level
  protection of `.git`/`.janetcode`; over docker, your own toolchain.
  **Linux machines with bwrap and docker now get bwrap by default**;
  `/sandbox docker` keeps the old behaviour.
- `--debug <file>`, `--list-models` (text or json), and a once-a-day
  notice in the TUI when a newer release is out (`update_check: false`
  turns it off).
- TUI: `ctrl+x` writes the prompt in `$EDITOR`; dragging over the
  transcript selects text and copies it (OSC 52 where no clipboard helper
  exists, so it works over ssh); a click still folds.

### Security
- `--web` refuses cross-site and DNS-rebound requests. Before this, any open
  web page could run shell commands through `/api/prompt`. Upgrade if you use
  `--web`.
- Credentials are redacted once, where tool results are produced, so they
  never reach the model context, the session log or eval records. Environment
  variables are treated as credentials when a credential phrase appears
  anywhere in the name (e.g. `DEEPSEEK_API_KEY_PERSONAL`), and generic `sk-`
  keys are caught too.
- Provider credentials are scrubbed from every child process the project
  controls (hooks, verify, MCP, eval checks, the docker sandbox).
- Three command-classifier bypasses closed (`cd` segments, raw `/tmp` paths,
  unexpanded variables); `-o` output paths are now classified as writes.
- Provider HTTP clients refuse redirects to another origin and bound and
  redact error bodies before they reach the transcript.
- Skills are scanned when they load, never run. A skill that came with the
  repository (`.claude/skills`, `.agents/skills`, `.janetcode/skills`) is
  blocked when the scan finds something critical: a reverse shell, the
  environment or SSH keys sent out, a pipe from a bare IP address into a
  shell, a symlink out of the skill, or an instruction to hide something
  from you. `/skills` and `--doctor` name the file and line. Your own skills
  are only warned about. Tested against 400 real-world skills: none were
  blocked.

### Borrowed and measured (2026-10-02)
Ideas taken from pi 1.0, deepseek-harness 0.2, hero_agent and a failed
experiment of our own, each checked against janetcode first.
- **MCP OAuth hardening.** RFC 9207: an authorization response naming
  another issuer, or none from a server that promises one, is refused
  before the code is exchanged. Logins are kept per server name and URL;
  `insufficient_scope` asks for the new scope plus what was granted, so
  a step-up keeps old access; empty and null token fields are accepted;
  `auth_server_metadata_url` overrides a wrong advertisement. The audit
  also added issuer/resource metadata checks, https-only endpoints off
  loopback, refusing servers without PKCE S256, a loopback-only redirect
  and a `WWW-Authenticate` parser that no longer cuts quoted scopes.
- **Timed questions.** `ask` takes a recommended option. A question in a
  `--bg` run, goal round or `/loop` waits `ask_timeout_unattended`
  (default 2m), in headless/ACP/SDK not at all, then goes on with the
  recommendation and says so; a late answer still reaches the model, as
  steering or with the next message. `--answer <id> <choice>` answers a
  `--bg` run's question. A question without a recommendation is never
  answered for you.
- **Provenance guard.** Each model call records what served it (model
  field, gateway provider, response-id and tool-call-id formats); a
  different vendor gets a one-time notice. In evals such runs are
  MISMATCH and runs that never reached the model are INFRA; both are left
  out of pass rates and comparisons, and `--eval-strict-provenance` fails
  the eval instead. It catches the 2026-10-01 broker swap to Kimi.
- **Durable turns.** Every way out of a turn but a crash logs its end, so
  a crash is recognisable; `/recover` (TUI, web) and `--recover` (with
  `-c`/`--resume`, headless and `--bg`) carry the dead turn on: finished
  results kept, reads re-run, changes never repeated but described from
  disk. Verified with a live `kill -9` mid-command.
- **Turn-start advice that orients.** `advisor_turn_start_style: orient`
  has a model read the likeliest files and answer LOOK / WATCH FOR /
  CHECK without naming the edit; `verify_baseline` runs the checks before
  the first change so the gate sends a turn back only for failures it
  caused. Both off until the A/B in `docs/benchmarks/orient-baseline`
  says otherwise. Fixed for every user: a turn-start consult cut off at
  its token limit, or present only as thinking, is no longer injected.
- **Errors that say how to recover.** A misspelt tool (`Bash`,
  `readfile`), an unknown argument or a wrong type names the right one;
  a script that prints nothing is told why it got nothing back.
- **The compiled agent loop was measured and rejected**
  (`docs/benchmarks/jit`): `run_plan` saved nothing on the models that
  could be measured cleanly. Its code stays on branch `development_jit`.

### User-expectation gaps (2026-09-30)
Closed from a checklist of what users expect of a coding agent
(`docs/notes/user-expectations.md`). Live-verified against the broker
with deepseek-v4.1-flash, mimo-v2.6-flash and qwen3.8-27b unless noted.
- **Anthropic path verified live** through a Messages-API endpoint:
  multi-turn tool use, streaming, SIGINT mid-stream, `-c`, effort and
  adaptive thinking (Claude Haiku/Sonnet). Bearer auth via
  `"auth": "bearer"` or `ANTHROPIC_AUTH_TOKEN`. Evidence in
  `docs/notes/anthropic-live.md`.
- **Networks:** `ca_file` / `JANETCODE_CA_FILE` add private CAs to every
  outbound client; proxies documented. Provider fallback classifies by
  status, error type and net error before any text match.
- **Commands that ask questions:** `bash` takes `stdin` and
  `interactive: true` (a pty job); on Linux a foreground command waiting
  on a prompt becomes a job the model answers with `job stdin`. Commands
  no longer inherit the terminal.
- **Jupyter notebooks:** `read_file` renders cells; `notebook_edit`
  replaces, inserts or deletes cells byte-preservingly, with diff, undo and
  permissions like `edit_file`.
- **Permissions:** typed scopes (`cmd:`, `read:`, `write:`, `host:`,
  `touches:`, `arg:`, `secret:`), `ask` rules that prompt in every mode,
  `/permissions check`, here-doc bodies no longer matched as commands.
- **Managed policy:** `/etc/janetcode/managed.json` (per-OS paths) fixes
  settings, locks rules, disables tools, modes, providers/models and MCP
  servers, forces a sandbox; `--inspect managed`.
- **Project trust levels:** `untrusted` / `instructions` / `full`, pinned to
  a fingerprint of the project's capability files; `/trust`, `--trust`.
  A project's skills, agents, commands, rules and recipes now wait for
  trust (instruction files are always read).
- **Egress:** per-turn report of what left the machine (`/egress`,
  `"egress"` in JSON); an allow/ask/block host policy; model-written
  memories can be held for confirmation (`memory_confirm`). Remote MCP
  hosts are checked by resolved IP.
- **Sessions:** an OS lease stops two processes writing one session
  (`--fork`, `--show` for a held one); `--search` across sessions; a
  hash-chained audit log checked by `--audit`; the model's forget can no
  longer remove `/remember` entries.
- **Recovery:** `/checkpoints` (esc esc on an empty prompt); undo outside
  git uses a private shadow repository; large checkpoint copies stream to
  blobs and huge ones are refused out loud; `--dry-run` / `/dryrun`
  records a full preview and can apply it.
- **Git and review:** `/commit` drafts a message in the repo's style;
  `/pr` pushes and opens a PR (compare URL without `gh`); `/pr-comments`,
  `/issue`, `/security-review`. A GitHub Action (`action.yml`) reviews PRs
  and answers mentions.
- **Extensibility:** MCP servers that die are marked down and reconnected
  with backoff (`/mcp reconnect`); `--mcp-serve` exposes janetcode as an
  MCP tool; plugins in Claude Code's layout with marketplaces (`/plugin`,
  `--plugin`); a Go SDK (`sdk/`).
- **Composer:** ctrl+r searches prompt history (effort moved to alt+e);
  ctrl+x ctrl+e edits the prompt in `$EDITOR`; `/vim`; a `keybindings`
  config key for every prompt action.
- **Accessibility and language:** `--screen-reader` line interface;
  `language` sets the answer language and the interface catalogue
  (Spanish, French); `/voice` and a web microphone transcribe through an
  OpenAI-compatible `/audio/transcriptions`.
- **Editors:** JetBrains ACP setup documented and handshake-tested; a VS
  Code extension in `editors/vscode`.
- **Platforms:** Windows exercised under wine (full test run and a live
  headless run); path rules and destructive-path grading fixed for
  Windows paths; a windows/macos CI job. `--uninstall` / `--purge`.
- **Sandbox:** `.git` hooks and config are put back after sandboxed
  commands; `strict` Landlock also refuses unix and UDP sockets (seccomp);
  seatbelt mach-lookup narrowed to an allowlist.
- **Large repositories:** grep allocates 20x less on kubernetes; the repo
  map samples across the tree and ranks the request's files first.
- **Second pass:** the egress policy is enforced for sandboxed bash
  through a filtering proxy (Landlock allows only its port; docker uses an
  internal network); `--acp`, the SDK and `--mcp-serve` start MCP servers,
  including those an editor passes; audit records name the real basis of a
  headless refusal and the files bash changed; undo is byte-exact under
  `core.autocrlf`; `JANETCODE_SHELL` picks a POSIX shell on Windows;
  `make vscode-test` runs the extension in real VS Code; `make action-test`
  runs the Action under act; `r` offers typed path rules; `/permissions
  check`, `/resume … fork` and a checkpoints card in the web; a dry run's
  grep and glob see pending edits; fixed a panic on the first MCP
  reconnect of a headless run.

### Deep review (2026-09-29)
About 240 fixes from a file-by-file review. Each has a regression test; the
commit bodies give the failure scenario.
- **Permissions.** An allow rule covers a compound line (`&&`, `;`, `|`,
  newline) or a `$( )`/backtick/`<( )` substitution only when every command
  in it is allowed. A deny rule refuses its command anywhere in the line.
  Protected paths and janetcode's own settings are judged through symlinks
  and case-insensitive names. Typing into or killing a background job asks,
  like the command that started it. `monitor` gets bash's guards. `--doctor
  --fix` no longer trusts a repository's verify commands for you.
- **Shell classifier.** Escaped quotes, `$'…'`, line continuations, every
  redirection form (`2>`, `>|`, `&>`), command and process substitution,
  wrapper options (`sudo -u`, `timeout -s`, `env -u`), `find -exec/-delete`,
  and git/sort/rg options that run or write things are now read as the shell
  reads them. Previewing a `sed -i` no longer runs its `e`/`w` commands. The
  seatbelt profile matches resolved paths, so `/tmp` projects are covered.
- **Network and credentials.** `fetch_url` checks the address a name
  actually resolves to, including after redirects and DNS rebinding. MCP
  never sends its headers or token to another host. Scripts, language
  servers and mutation tests no longer see provider keys, and model-run
  commands no longer see the web UI token. Redaction catches keys in JSON,
  URL passwords, and GitHub, GitLab and Slack tokens. Hook notes and failing
  verify output are redacted.
- **Terminal safety.** Tool output, model text, plan steps and ask dialogs
  can no longer send terminal control sequences (clipboard writes, screen
  clears).
- **Data loss.** An atomic write through a symlink writes the target instead
  of replacing the link. `/undo` leaves pre-existing ignored files alone,
  reverts edits to ignored files, and never restores a snapshot into another
  worktree. Keep-or-revert only reverts the turn's own edits. `--backup`
  never replaces an existing backup with an empty one. Recipe replay changes
  only the lines it recorded. `edit_file` refuses overlapping matches as
  ambiguous. Mixed line endings survive an edit.
- **Hangs and crashes.** Parallel permission and `ask` prompts now queue in
  the TUI, web UI and headless mode instead of replacing each other. Before,
  the turn hung, or an answer went to the wrong call. Also fixed:
  - forks the model starts no longer fail on their first call
  - `/routing` during a turn no longer crashes
  - a hook printing 64 KB or more no longer crashes
  - `/jobs stdin`, `/inspect`, MCP calls, FIFOs and a background process
    left by a command no longer hang
- **Turns and history.** A reply that stops (loop detector, refusal, paused
  turn) leaves no unanswered tool calls behind to break the next request.
  A read placed after an edit in the same batch sees the edit. Compaction,
  pruning and the archive pair results with the right call when a server
  reuses call ids.
- **Cost.**
  - Subagents stop at what is left of `--max-cost`.
  - A fallback or a subagent on another model uses that model's price and
    context window.
  - `--max-cost` and `/budget` refuse NaN and Inf.
  - `--bg` passes on `--model`, `--max-cost` and the other run flags.
  - The token estimate no longer counts the system prompt twice, so
    compaction no longer fires early.
- **Providers.**
  - An Anthropic stream that ends early is retried as a truncation.
  - An overload error sent mid-stream is retried when no content has
    arrived yet.
  - A stall before the response headers arrive is retried.
  - Tool calls in an Anthropic-compatible reply that says `end_turn` are
    run instead of dropped.
- **Other.**
  - A prerelease build updates to its release.
  - Session forks keep their `/accept` and `/loop` state.
  - Memory and undo files are 0600.
  - LSP file URIs are escaped and positions count UTF-16.
  - Many smaller TUI, web UI, ACP, plan, recipe and eval fixes.

### Web UI (`--web`)
- A browser session: live workspace pane (plan, files, jobs, subagents),
  embedded terminal, trajectory panel with timeline and search, structured
  `/perf` and `/usage` cards.
- Redesigned: one centred column, drawers, floating permission card, code
  highlighting with copy, mermaid diagrams, edit & resend, regenerate,
  find-in-conversation, jump-to-latest, phone layout.
- Starts without an API key and connects from the page; a busy port falls back
  to a random one; a stale page says the server was rebuilt.

### Terminal UI
- Full-palette themes (eight builtins plus your own) switchable with `/theme`.
- Live overlays for `/agents`, `/jobs`, `/todos`; `/trajectory` opens an
  interactive ledger browser.
- Mermaid diagrams drawn in the transcript.
- `shift+tab` cycles ask → plan → auto → yolo; `ctrl+r` or `/effort` changes
  reasoning effort live.
- The composer grows with its content; queue vs. steer is labelled.
- `/help` lists every built-in under its group.

### Sessions, safety and undo
- Auto mode, optional bash sandbox with named profiles (docker, seatbelt,
  Landlock) and a per-project `sandbox` config key.
- Permission prompts offer a ladder of rules, from narrow to broad, and rules
  can match a field (`tool(param:pattern)`). Paths whose effects outlive the
  session are protected.
- `/undo turn n` rewinds the work tree and the conversation; undo by
  description.
- `/cd` moves the session without restarting it; fork lineage is shown
  wherever sessions are listed.
- The plan is a file you can comment on, from either front end.
- A turn that ends with steps still open in the plan it wrote is sent back
  once to finish them. A model you asked to do only part of the plan can
  still stop there.

### Models and cost
- Model slots (main, cheap, strong, advisor, vision) set from `/model`,
  `/features` or `--set`. Step routing sends mechanical steps to a cheaper
  model. Every call records which slot served it and why, and `/routing`
  turns routing off for the session.
- Real per-model pricing from LiteLLM's table; `/usage` shows spend and
  cache-hit rate per model; `/perf` shows where the wall-clock time went.
- `--max-cost` is enforced in `--watch` too.

### Project tooling
- `--doctor --fix` applies the safe repairs it lists.
- `--init-project` writes a first instruction file from what the repository
  contains.
- MCP servers can be added and removed from a command or dialog, and
  `.mcp.json` is written for you.
- `/eval` keeps a real session as a regression task; `/recipe` replays a
  successful turn with no model call; `--proof` shows what demonstrated each
  change in a commit.
- `--what-if` reruns a recorded session against changed code, model or
  settings and compares the two runs; `--replay` reruns one at no token cost.
- Acceptance tests the model cannot weaken (`/accept`).
- Hooks fire at fourteen points instead of four: `permission_request`,
  `post_tool_failure`, `user_prompt`, `session_start`, `session_end`,
  `stop_failure`, `post_compact`, `notification`, `subagent_start` and
  `subagent_stop` join `pre_tool`, `post_tool`, `pre_compact` and `stop`.
  Each hook learns the session, its transcript and the permission mode.
- Hooks written for Claude Code or Qwen Code run unchanged: their event
  names, matcher groups, tool names, stdin fields and JSON answers are all
  understood.
- Fixed: a subagent left with no tools crashed the session instead of
  reporting an error.
- `vision_model` now works: a text-only session model gets each image
  described by the vision model instead, once per image per session.
- `status_line` puts a command's output under the input, fed the session
  as JSON the way Claude Code and Qwen Code feed theirs; their
  `statusLine` object can be pasted in as it is.

### Headless
- The `--output-format json` result carries routing evidence (`routes[]`).
- `--set key=value` applies any `/config` key for one run.

### Experimental (off by default)
- Optional Jev decision service (`decisions` config block): triage of
  verification failures and intake classification. Fails open. The
  measurements are in `docs/notes/jev.md`.
- Computer use through the Cua Driver MCP backend.

### Polish
CLI
- `--help` groups flags by task, uses the `--long` spelling, names what each
  flag takes (`--resume id`) and fits 80 columns.
- Usage mistakes exit 42 with one plain line: an unknown or malformed flag,
  an unknown `--resume` id or recipe, a `--from-turn` the recording lacks,
  a missing repo or commit for `--proof`, a second `--init`/`--init-project`.
- `--output-format json`/`stream-json` print a result object even when the
  run fails before it starts.
- Provider failures are explained (what failed, what to do); `--doctor`
  fails only the provider in use and lists optional features as info.
- `--recipe` runs with no API key; `--sessions` and counts read as words.
- The missing-API-key error puts one fix per line, fastest first.

Terminal UI
- Permission keys say what they do (`a this session`, `r save <rule>`); the
  approval line says "used 3 times in 2 files" instead of "3 refs".
- Reading a file that does not exist is an answer, not a red failure (web too).
- At 80 columns the status line keeps the cost, the connect list keeps the
  price, and the queue row keeps its keys; the command menu is aligned.
- `/undo` with nothing to undo is a note; titles cut short end in "…";
  a cancelled reply no longer ends on a bare `###`; light-theme borders
  reach 3:1.
- `/usage` before the first call says $0 instead of "unpriced".

Web UI
- The permission card asks "Edit a.go?" / "Run go test ./...?".
- Follows the OS light/dark setting on first visit; muted text reaches 4.5:1.
- Model menu: wider, named after the service (OpenRouter), current model
  first, "1M context".
- Header: "Effort: auto", no green dot without a model, no context meter
  before the first message; on a phone the model's own name and a dot for
  reconnecting, and drawers that start shut and never stack.
- Key failures offer a **Fix connection** button; plain labels throughout
  ("Scripts", "Show full reply", one set of permission-mode names).

### Robustness (lessons from diwan)
- Tool-call arguments with a dropped bracket or a trailing comma are mended;
  anything still not JSON is refused for that call and the model resends
  it, instead of the whole turn failing. A call cut off at the output
  limit is now reported as such and refused, never mended.
- Reasoning a model writes inline between `<think>` tags (Qwen3, R1
  distills on Ollama/vLLM/llama.cpp) arrives as thinking, not as the answer.
- A list or object sent as a string holding its JSON is accepted by every tool.
- Content streamed as a list of parts and usage sent as strings no longer
  read as a truncated stream.
- Fallback judges a provider error by its status and the server's whole
  text, not the display copy cut at 300 characters.
- Pruning drops the long strings from old calls' arguments (whole file
  bodies), keeping their paths; thinking tokens are shown in the usage line.

### Web, background runs and operations
- `--web` requires the access token in the URL it prints, on loopback
  too: other users and processes on the machine can reach 127.0.0.1
  (`JANETCODE_WEB_TOKEN` fixes it). A hidden tab raises a desktop
  notification when an approval is waiting.
- `--bg` no longer runs `--yolo` unless told to, and keeps the permission
  flags it is given (before, even `--bg --readonly` ran `--yolo`); what
  its mode asks about waits for `janetcode --answer
  <id> allow|session|deny`, `JANETCODE_NOTIFY` runs a command when one
  starts waiting, and `--sessions` shows each run as running, waiting,
  done or failed.
- `--backup <file>` / `--restore <file>` for the config and data
  directories; a restore saves what it replaces first.
- `--doctor --output-format json`; `--doctor --fix` makes a config file
  other users can read 0600.
- `/mcp search <words>` prints the `/mcp add` line for servers in the
  official MCP registry; it installs nothing. Results are ranked (a local
  package with a repository first) and a publisher's near-duplicates fold
  into one line.
- `make dist` builds release binaries with `SHA256SUMS`; a `Dockerfile`
  and a tag-triggered release workflow (binaries and a GHCR image).
- CI runs the markdown XSS corpus, the contrast checks and three browser
  suites, one of them real turns against a scripted model (`make ui-turn`).

### First week
- Install without Go: `install.sh` downloads the release binary for the
  machine and checks it against `SHA256SUMS`; `janetcode --update` does
  the same in place.
- `docs/getting-started.md`: the first ten minutes, from install to a
  reviewed change.
- `--help` is one screen of everyday flags (`--help all` for all 76);
  `/help` is one screen of everyday keys and commands (`/help all`).
- `/permissions` opens with one plain summary of what runs, what asks
  first and what never runs, in the terminal and the browser alike.
- The first-run banner says `/connect` rather than pointing at a closed
  dialog, and wraps between its items with a hanging indent.
- `--plain` (or `JANETCODE_PLAIN=1`, on by itself when `TERM=linux`)
  draws the terminal interface in ASCII with a still spinner.
- The web's folded tool bursts colour only their "(n failed)" count red.
- Fixed: the web's paperclip file picker did nothing (a second
  `uploadFiles` shadowed the first and called `forEach` on a FileList).
- A long session no longer slows every frame: the transcript's viewport
  stopped measuring every line's width on each change (39 ms to 8 ms a
  frame at 20,000 lines). Startup, keystroke, new-line, resize and memory
  budgets are now tests.
- `app.js` is split: renderers, the trajectory view, the terminal tab and
  the connect dialog are their own files; every browser suite fails on
  any uncaught exception in the page.

### Look and feel
- New palette in both interfaces: system greys, an iris accent (the brand
  colour, with an iris-to-aqua gradient for the mark), and system green,
  red and orange for status, in light and dark. In the web
  UI, faint text now reaches 4.5:1 contrast. The previous terminal palette
  is still available as the `classic` theme.
- Web: the permission card has filled buttons and shows Deny in red; the
  provider select in `/connect` now looks like the inputs beside it; link
  cards use the text font; the sound setting is a switch; the help sheet no
  longer wraps its keys; the sixth workspace tab is no longer cut off. The
  empty trajectory and the empty conversation rail now say what will appear
  there.
- Terminal: `/help` shows headings in bold and command names in the accent
  colour. A permission prompt's keys (y, a, r, n, c) stand out from their
  descriptions. Wrapped bullets and numbered items continue under their own
  text, and wrapped quotes keep their bar. Wrapped thoughts continue under
  their text too. The input hint uses the theme's faint colour, so on a
  light terminal it no longer looks like typed text. When the status line
  is short of room, the cache rate goes first and `/undo` stays.
- A brand: a spark-dotted "j" mark in the web UI (sidebar, empty page,
  favicon) and a gradient `✦ janetcode` wordmark in the terminal header
  and welcome, which is now two lines.
- Web: tool calls are cards, with an icon per kind; an edit opens on its
  diff with +/- counts, and a command on a terminal-style block showing
  its output tail. Thinking is set in the reading face and shimmers while
  it streams. The model and effort share one pill; the connection dot
  shows only when the connection is lost. A live entry rises in, the
  verdict lands, and the streaming caret breathes (all off under reduced
  motion). Code highlighting has its own palette per theme, with calls,
  types, attributes, strings and numbers each in their own colour and at
  least 4.5:1 contrast. The empty page suggests work from the project:
  reviewing uncommitted changes, a TODO in the code, checking the last
  commit, or adding tests to an untested Go package.
- Terminal: markdown headings no longer show their `##`. Tables are as
  wide as their content, in a rounded border with an accent header,
  instead of stretched across the terminal. Edit diffs are tinted bands,
  with the changed words in a stronger tint.

### Known limitations
- The Anthropic provider is verified live through a Messages-API broker,
  not yet against `api.anthropic.com` itself; the broker reports no cache
  usage, so cache discounts are untested live.
- Windows has run under wine only (tests and a headless turn); PowerShell
  commands, hooks and verify need a real Windows run (the CI job does it).
- The seatbelt sandbox changes are unit-tested only; they have never run on
  a Mac.
