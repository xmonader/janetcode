# Sessions

Part of the [janetcode manual](README.md); the [README](../../README.md) has the short version.

Every conversation is saved as append-only JSONL under
`~/.local/share/janetcode/sessions/`.

A session's first turn in a git repository also records the commit `HEAD`
pointed to (empty when the repository has no commits yet) and a snapshot
of the work tree at that moment, so the session can later be turned into
a regression eval with `/eval keep` (see [evals](evals.md)) that replays
it against the state it actually started from. A session begun before
this existed, or where recording otherwise failed, has no such state
recorded; `/eval keep --base <rev>` is the fallback for keeping one of
those anyway.

Every session gets a title after its first turn: the model is asked for a
few words (a headless run uses the first prompt instead), and `/title
<text>` renames one. `/fork [title]` branches the conversation: a new
session starts with a copy of the messages and pins, the original stays
as it was, and `/resume` lists both so you can try two approaches from the
same point. `/fork stay [title]` makes the same copy but stays on the original
session; later messages land only on the live one, and the copy is listed
with a parent link (`fork_of`). `/history <words>` searches every saved session in every
directory for the words and shows the best matching line, and the turn it
is in, with the session id to `/resume`. The same search runs from the
shell as `janetcode --search "<words>"`, and in the web interface the
filter above the conversation list searches what was said once it has
three letters, showing the matching line under each conversation.
`janetcode --show <id>` prints a saved conversation as Markdown without
opening it.

## One process per session

A session is run by one janetcode process at a time. The process that
opens it — the terminal interface, `--web`, a `-p` run, a `--bg` child, an
editor over `--acp` — takes its lease, a lock on the `<id>.lock` file beside
the session that the operating system drops when the process ends, however
it ends: a crash or a `kill -9` never leaves a session locked. A second
process that tries to run turns in the same session is refused before
anything is written, and told who has it:

```
session 20260929-101500-a1b2 is open in another janetcode (the terminal interface, pid 4121, since Sep 29 10:15); only one process can run turns in a session at a time
  janetcode --resume 20260929-101500-a1b2 --fork   continue in a copy of it
  janetcode --show 20260929-101500-a1b2           read it without opening it
```

Inside a session, `/resume <id> fork` is the same way on, in the terminal
and the web page alike; clicking such a session in the web page's
conversation list reports the conflict and offers to continue in a copy.
Without the lease, two processes appending to one file interleaved their
turns into a history neither of them wrote, and each saved its undo stack,
`/accept` lock and loops over the other's. `--bg` hands the lease to the
child it starts, so `--resume` of a background run that is still going is
refused the same way; `--answer` and `--sessions` read its sidecars and need
no lease. Within one process the lease is shared, so nothing refuses a
session the same process already has open.

`janetcode --bg "prompt"` starts that prompt in a detached child, prints
the session id, and returns. Reopen it with `--resume <id>`. Armed `/loop`
jobs are stored on the session and restored when that session is resumed.

The child runs in the permission mode you give it (the default, or
`--auto-edits`, `--auto`, `--readonly`, plus any `--allow`, `--deny`, `--core-tools` and `--exclude-tools`
rules). A call that mode asks about waits until you answer it; nothing
runs unasked unless you add `--yolo`:

```sh
janetcode --bg --auto-edits "fix the tests"   # edits run, commands wait
janetcode --sessions                          # [background: waiting for an answer: bash go test ./...]
janetcode --answer <id>                       # shows the request and its preview
janetcode --answer <id> allow                 # or session (this tool for the rest of the run), or deny
```

`--sessions` shows every background run's state (running, waiting, done
with the first line of its answer, or failed with the error), so it is
also the "what happened while I was away" view. Set `JANETCODE_NOTIFY` to
a command and it runs each time a run starts waiting, with
`JANETCODE_SESSION`, `JANETCODE_ASK` and `JANETCODE_ANSWER` (the command
that allows it) in its environment and the provider keys removed, so the
request can reach your phone:

```sh
export JANETCODE_NOTIFY='curl -s -d "$JANETCODE_ASK — $JANETCODE_ANSWER" ntfy.sh/my-private-topic'
```

The child's own log is the `.bg.log` beside its session file.

### When the model asks a question

A question from the model (the `ask` tool) waits in the same sidecar.
The model is asked to give each question a recommended option, and a
background run waits for an answer only `ask_timeout_unattended` (default
2m), then goes on with the recommendation and says in its reply which
choice it assumed. The question stays open after that: answer it late and
the model gets the answer before its next step, with a note that it went
on without it, so it can change course.

```sh
janetcode --answer <id>                       # Which port?  1. 8080  2. 9000 (recommended)
janetcode --answer <id> 1                     # an option's number, its label, or your own words
janetcode --answer <id> "8080; alpha"         # several questions: one answer each, separated by ;
```

A question without a recommendation is never answered for you: it waits
for `--answer` however long that takes.

The same waiting applies everywhere a turn may have nobody watching it:
a goal round or a `/loop` firing in the terminal or the browser waits
`ask_timeout_unattended`, and a turn you typed waits `ask_timeout`
(default: as long as it takes). The terminal and the page show the
recommendation, preselected, and the countdown; once the turn has gone
on, the question stays up and an answer still reaches the model (esc, or
Dismiss, closes it). A headless `-p` run, an editor over ACP and the SDK
have nobody to ask: a question with a recommendation goes on with it at
once, and one without is refused, as before.

```sh
janetcode --sessions        # list sessions for this directory
janetcode --continue        # resume the newest one
janetcode --resume <id>
janetcode --bg "fix the tests"
```

Inside the TUI, `/resume` lists the sessions for the directory and
`/resume 2` reopens one.

## A turn the session died in

A janetcode that is killed, crashes or loses its machine in the middle
of a turn leaves that turn open on the log. Reopening the session says
so — `turn 4 was interrupted during its tool calls (2 finished, 1
running, 0 not started); /recover carries it on` — and `/recover` (in
the terminal and the browser) or `--recover` (with `-c` or `--resume`,
headless or `--bg`) carries it on as the next turn, with no new message
from you. What the model gets for the step it was in:

- a call that **finished** keeps its real result (also when you resume
  without recovering: the result is on the log even though the step's
  answers never reached the history);
- a **read** that was running runs again;
- a **change** that was running (an edit, a write, a command) is never
  run again: the model is told it may or may not have happened, with
  the evidence — whether the file now holds what the write would have
  written, whether the edit's new text is there and its old text gone,
  or the repository's short status for anything else;
- a call that **never started** (it was waiting for your approval) most
  likely did not run, and the model is told to check before issuing it
  again.

A model call that was streaming is simply made again. A turn you
stopped yourself, or one that ended on an error, is not "interrupted":
every way out of a turn but a crash records its end. Sessions written
before this existed are never offered a recovery. What is not covered,
and why, is in `docs/notes/durable-turns.md`.

## The audit log

Every event a session records — each model call with the model, provider,
route, usage and cost it was made with; each tool call and its result;
what authorized each call that could change something (a permission
rule, the mode, a hook, or your answer at the prompt) and what refused one;
and every file a tool changed, with the sha256 of its content before and
after — also gets a record in `<id>.audit` beside the session. Each record
carries the hash of the one before it, so the chain cannot be edited in the
middle without every record after it changing.

```
janetcode --audit <session>
```

checks that chain against the session and prints what the session did,
from the record alone, without running a model, a tool or anything on the
network. At the bottom it says whether the two agree:

```
turn 1
  user       delete the build directory
  model      deepseek-v4.1-flash [openai, in 1840 out 96, $0.0004]: I will remove it.
  permission bash allowed by the user: allow
  call       bash {"command":"rm -rf build"}
  result     bash ok: (no output)

audit: intact — 5 events vouched for by 6 chained records
head 3f0c…  (keep it to show later that nothing was cut off the end)
```

An event changed after it was written, one removed from the session, one
added to it by anything but janetcode, and a record edited in the audit log
are each named, and the command exits 1. Both files are in janetcode's data
directory, which the file tools and the command guard already treat as
janetcode's own: the model has to ask before it can touch either. What a
chain cannot show on its own is the end of both files cut off together;
the head hash `--audit` prints is the thing to keep somewhere else if that
matters. A session recorded before the audit log existed gets one from its
next event on, and the report says how many earlier events nothing vouches
for. `--replay` prints the same verdict before it replays a recording.

A run with no one to ask — `-p` whose stdin is not a terminal — refuses
what its mode leaves to ask by itself, and the record says so:
`REFUSED by headless: no one to ask (deny)`, not "the user".

A shell command names no paths, so for bash the work tree is read around
the call: `git status` before and after, and the sha256 of every file
either lists. Each file the command created, changed or deleted gets a
`changed` record against the call, as a file tool's does. The limits: only
in a git repository (a directory outside git has no index to compare
with); ignored files are not seen; a background job still writing after
its call returned is credited to the next bash call; and a work tree with
more than 500 changed files, or a status slower than five seconds, is
skipped rather than hashed.

## Rewinding a session

`/undo turn n` (see
[the interface manual](interface.md#going-back-to-before-a-turn-undo-turn-n))
puts the work tree back to before turn n and rewinds the conversation
with it. The session file is where that happens, and the rule the file
lives by does not bend for it: the log is append-only, so nothing is cut
out. The dropped turns' events stay on disk exactly as they were
written. What is appended is one more record — a replacement event that
shadows the surface events of those turns and stands in their place,
carrying the note the model sees.

That is the same mechanism compaction uses: a summary event replaces the
messages it summarises rather than deleting them. Resuming a session
folds the log into the model-visible conversation by following those
replacements, so a `--resume` after a rewind sees the rewound
conversation and not the dropped turns — and the live session sees it
for the same reason, because the rewind re-folds the log rather than
editing the messages in memory by hand. `janetcode --resume <id> --inspect events` still
shows every event, dropped turns included: the file remains the full
record of what happened, including the fact that it was rewound.

Turn numbers are not reused. The log keeps the dropped turns, so the
next turn carries on after the highest number the session has ever
written rather than starting again at n — two different turns keyed the
same number is exactly what a resume refuses to produce.

A compaction between turn n and now is the one case a rewind declines:
the summary replaced the messages that marked where turn n began, so
that point can no longer be located in the conversation. The files go
back anyway and the preview says why the conversation did not.

## Fork lineage in the listing

Because `/fork` records which session a copy came from, every listing of
sessions — the `/resume` picker, `janetcode --sessions`, the web UI's
session list — draws the branches instead of a flat run of rows:

```
20250915-101500-ab12  the parser rewrite
└ 20250915-112000-cd34  the lexer branch
  └ 20250915-131000-ef56  the lexer branch, without the cache
20250915-090000-7788  a stray fork   (fork of 20250901-120000-9900, not listed here)
```

The rules are the same everywhere:

- Sessions that are nobody's fork keep the order the listing already
  used — most recent activity first, settled ones last. A fork follows
  the session it came from, and forks of one session keep that same
  order among themselves.
- The session you are in is marked `current`, a settled one `settled`.
- Indenting stops after three levels: a longer chain is still listed, at
  that depth, with a `…` instead of the branch mark.
- A fork whose parent is not in this listing — deleted, or made in
  another directory — is listed at top level and says where its parent
  went, rather than looking like a conversation that started on its own.
- Typing in the `/resume` picker filters the list and keeps a match's
  parents on screen, marked `parent of a match`, so a fork is never shown
  adrift from the conversation it branched off.

When the context grows past `compact_at` tokens (80k by default: far below
any modern window, because a smaller working context is cheaper, caches
better and is attended to better), or the model reports the prompt is too
long, the history is summarised into a handoff note and the conversation
continues in a new session file. A "too long" error that names the model's
real limit lowers the threshold for the rest of the session. `/compact`
does this on demand.

A summary is the last resort. Before paying for one, old tool results are
deduplicated and trimmed, and then condensed into one-line
placeholders in attention order (a command re-run later goes first, then
results nothing came back to, and a file the model kept editing last) that still say what the call was and how it went (`bash "go
test ./..." → exit 1; last line: FAIL pkg`, `read_file a.go: 400 lines
were shown`) with the archive id for the full text, until the history is
down to half the threshold. Most of the time that is enough and no model
call is made. When a summary call does fail (rate limit, outage), no
summary is attempted for ten minutes; pruning carries the session
meanwhile. Token estimates use character classes (letters, digits and
symbols, non-ASCII) rather than a flat bytes-per-token guess, which is
what keeps the threshold honest on code.

The summary itself is not a prose note. It is a typed working-memory
record: the harness fills in exactly what it observed in the messages
being condensed (files edited with a hash of their content now, files
read, commands run with their last exit code and run count, the last
verification result), and the model only maintains the parts that need
judgement: goal and constraints, decisions, approaches ruled out, facts
about the codebase, open questions, and next steps. The model gets the
previous record and updates it rather than summarising from scratch, so
the record does not drift from compaction to compaction, and a model
that answers in prose anyway has its text kept as notes. The eval
switch `working` restores the old prose handoff note for comparison.

Edits are refused when a file changed on disk since the agent last read
or wrote it (you edited it, a formatter ran, another session touched it):
the model is told to read it again. Files it has never read are not
checked; the edit coach handles those.

Tool results longer than 24 KB (12 KB for commands, 8 KB for listings and
greps) never reach the model whole: the full text
is archived under an id such as `ctx-12` (per project, 8 MiB, oldest
evicted) and the model sees the first and last part plus the id. The
`expand_output` tool pages through an archived result, greps it, or greps
every archived result, so a truncated build log or grep can always be
recovered instead of rerun.

An MCP server that exposes more than six tools has them registered
*deferred*: their schemas are not sent with every request. The model sees
their names in the `tool_search` description and loads the ones it needs
by keyword, after which they stay available. The `task` subagent tool takes
an optional `tools` list so a narrow job gets a narrow toolset.

## What if: replaying a session with one thing changed

`janetcode --what-if <session id or path> --from-turn <n> [--model <m>]
[--off <names>] [--deny <rule>] [--prompt "<text>"]` answers "what would
have happened if..." for a session that already ran, without redoing the
turns that already worked and without touching the real project tree.

The mechanism is the same recorded-start state `/eval keep` uses (see
[evals](evals.md#keeping-a-session-as-an-eval)): the session's first turn
recorded the commit `HEAD` pointed to and a snapshot of the work tree at
that moment, and `--what-if` checks that state out into a fresh, isolated
git worktree. Turns before `--from-turn` are then re-executed there
against the model's own recorded replies — the same cassette
`--replay` reads, so no tokens are spent and no API key is needed for
that part — with every tool call actually run for real against the
worktree, so the tree reaches exactly the state it had right before the
turn being changed. From `--from-turn` on, the session's remaining user
prompts run **live**, against whatever was asked to change:

- `--model <m>` — a different model (the session's own model otherwise);
  `--provider` selects the backend the same way it does for any run
- `--off <names>` — an ablation, disabling named harness behaviours for
  the live part only (the same names `--eval-off` and `JANETCODE_OFF`
  take, e.g. `--off coach,repomap`)
- `--deny <rule>` — an extra permission-deny rule for the live part
  (comma-separated, same syntax as the ordinary `--deny` flag); it wins
  even though the run auto-approves everything else
- `--prompt "<text>"` — replaces the recorded prompt of the turn named by
  `--from-turn`; every prompt after it is still the session's own

Every tool call in the worktree is auto-approved without asking — safe
because the whole run, replayed and live alike, takes place in a worktree
that is thrown away afterward (`--what-if-keep` keeps it instead, and
prints where).

At the end it prints a comparison between the session as it actually
happened and as it happened with the change applied: where the two first
diverge (the first tool call, at or after `--from-turn`, that uses a
different tool or acts on a different target — file, command, URL — or
that only one side made, with both shown; calls that did the same thing
with differently phrased arguments are counted, not reported as the
divergence), a verdict per turn on each side
(see [what a turn actually demonstrated](interface.md#what-a-turn-actually-demonstrated)), which files each
side touched and by how many lines, a run of the project's own
[verify commands](evals.md) in the resulting worktree, and tool calls,
tokens and cost — split into what the whole session would have cost
against what this run actually bought new, since the replayed turns spend
nothing. It exits 0 whether or not the two runs agree; `--json` writes the
same report as one JSON object instead of prose, for a script.

```sh
janetcode --what-if 20260911-153001-4f2a --from-turn 4 --model claude-opus-5
janetcode --what-if 20260911-153001-4f2a --from-turn 2 --off coach,repomap
janetcode --what-if 20260911-153001-4f2a --from-turn 3 --prompt "fix it without touching the public API" --json
```

`--what-if`'s live agent is the same minimal, real (not read-only) tool
set an eval run uses (core file and shell tools, search, `expand_output`,
a todo tool and code execution — see [evals](evals.md)) rather than a
real session's full tool set: no MCP, no hooks, no memory or skills, and
none of the job/terminal/fetch/subagent tools a session normally carries.
That keeps a comparison about the change under test rather than about
which of the product's own features happened to be wired up, the same
reasoning eval runs are built on; when the original session actually used
a tool outside that set, the report says so, since the live part cannot
behave the same way past that point for a reason that has nothing to do
with what was asked to change. Files changed is folded from `write_file`
and `edit_file` records only (the same ones `/undo` and `--proof` keep):
a file a bash command edited directly does not appear on either side, a
symmetric gap the report also notes.

`--what-if` refuses clearly, before spending anything, when: the session
has no recorded start state (it predates this feature, predates the
repository's first commit, or began outside a git repository — the same
condition `/eval keep` reports, with the same `--base`-shaped fix not
available here since there is no task to keep); `--from-turn` is out of
range, or the recording has fewer turns than it names; or no provider is
configured for the live part.
