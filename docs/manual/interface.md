# The interface

Part of the [janetcode manual](README.md); the [README](../../README.md) has the short version.

The transcript streams the model's markdown as it arrives, shows each tool
call on one line with what it did and how long it took, and renders every
edit as a coloured diff. The status line shows what the agent is doing, the
elapsed time, and a gauge of how full the context window is. The input box
grows with your text.

```
enter            send        ctrl+j / alt+enter   newline
up / down        recall earlier prompts          tab   complete a /command
esc              cancel the running turn (or clear the input)
ctrl+o           expand tool output and diffs    ctrl+l   redraw
pgup / pgdn      scroll (mouse wheel works)
ctrl+c           cancel the turn; when idle, press twice to quit
```

At a permission prompt: `y` allow once, `a` allow that tool for the session,
`n` deny and type why (the reason goes to the model, enter skips it), `esc`
deny silently. Destructive shell commands (recursive deletes, force pushes,
sudo, piping downloads into a shell) are flagged in the prompt.

Typing while the model is working queues the message; it is sent as soon as
the turn ends. Cancelling a turn hands queued text back to the input.

```
@path            attach a file, an image (png/jpg/gif/webp), or a directory listing; tab completes
ctrl+v           attach the image in the clipboard (wl-paste or xclip on Linux, pngpaste on macOS)
!command         run a shell command here; its output is shown and shared with the model
/help      /quit      /clear (new session)     /yolo (toggle auto-approve)
/plan <request> (plan mode: explore, write a plan; /plan approve executes it)
/readonly  (toggle read-only: the model can inspect but change nothing)
/connect   /model     /compact (summarise the context to free the window)
/usage     /stats (turns, model calls, tool calls per tool, tokens, cost)
/resume    /verbose   /resume <n or id> (reopen an earlier session from this directory)
/undo [preview]  /redo  /find X    /cost      /mcp [trust | auth <server> | logout <server>]
/fork [title]  /title <text>  /history <words> (search every saved session)
/open path[:line] (view a file inline)   /diff [path | revert path:n] (hunk review)
/context   (what fills the window)   /pin <note or @file>   /unpin <n>
/export [file.md] (write the conversation as a Markdown transcript)
```

`ctrl+c` cancels a running turn, or clears typed text; when idle it asks
for a second press before quitting, so a stray one never closes a session.

`/connect` walks through provider → base URL → API keys → model with
arrow-key lists that filter as you type; `/model` alone opens just the model
list for the current provider (`/model name` sets one directly). Both write
the choice to the config file. When the provider's model list carries a
context length and prices (OpenRouter does), the list shows them
(`128k ctx · $0.15/$0.6 per M`) and the choice is saved under `"models"`
in the config, so cost estimates work without a `"prices"` entry and
compaction runs at three quarters of the model's window (capped at 200k
tokens) instead of the 80k default. Explicit `"prices"` and `compact_at`
still win; Claude and GPT windows are known without a listing.

Safety rules that apply in every mode, including `--yolo`: commands that
would destroy the machine (deleting `/` or the home directory, formatting,
writing raw devices, piping a download into a shell, fork bombs, power-off)
are refused outright, seen through `sudo`, `env`, `timeout`, `sh -c` and
`eval` wrappers; commands that change files outside the working directory
(`rm ../x`, `cp a ~/b`, `> /etc/hosts`) always ask. Everything else follows
the permission mode.

`/undo` puts the work tree back to how it was before the last turn that
changed anything, and tells the model. Inside a git repository the whole
tree is snapshotted before the first mutating tool of a turn (a hidden
commit under `refs/janetcode/cp/`, taken through a temporary index so
HEAD, your index and your branches are never touched), so changes made by
shell commands, generated files and deletions all revert. Outside git the
files changed through `edit_file`/`write_file` are restored from copies.
Repeat it to go back further (up to 30 turns); it still works after
`--continue` or `/resume`. If you committed or checked out in between, the
git snapshot is not applied and only file-tool changes are reverted.
`/undo preview` lists what an undo would restore, recreate or remove
without touching anything. `/redo` reapplies what the last `/undo`
reverted; it survives a restart but is discarded as soon as the next turn
changes something, because the two histories would no longer line up.

### Permission rules

Approval is normally per call (`y`), or per tool for the session (`a`).
Press `r` at a prompt to save a rule instead, so matching calls never ask
again in any session; the prompt shows the rule it would save, such as
`bash(go test *)`, `edit_file(internal/agent/*)` or
`fetch_url(https://pkg.go.dev/*)`. Rules live under `"permissions"` in the
config file:

```json
"permissions": {
  "allow": ["read_file", "bash(go test *)", "bash(make *)", "edit_file(internal/*)"],
  "deny":  ["bash(git push*)", "bash(rm -rf *)"]
}
```

A rule is `tool` or `tool(pattern)`; `*` spans anything and `?` one
character, matched against the command for `bash`, the path for file
tools and the URL for `fetch_url`. Deny wins over allow; a denied call is
refused without a prompt and the model is told which rule did it.
Guarded commands (ones that reach outside the workspace) still ask.
`/permissions` lists rules, `/permissions allow <rule>` and `/permissions
deny <rule>` add one. A project can ship `.janetcode/permissions.json`
with the same shape: its deny rules always apply, its allow rules only
after `/permissions trust`. Headless runs take `--allow` and `--deny`
with comma-separated rules, and a `pre_tool` hook can answer
`{"decision":"allow"}` to approve a call itself.

### Memory

janetcode remembers across sessions. Each entry is a small markdown file
with frontmatter (`name`, `description`, `type`, `source`, `uses`, dates)
under `~/.local/share/janetcode/memory/`: a project scope keyed by project
identity (`.janetcode/project` if present, else the git origin URL, else
the path) and a global scope (`_global/`) for things about you that hold
everywhere. Types: `user`, `feedback`, `project`, `decision`, `reference`.

- **Index in the prompt.** Names and one-line descriptions of every
  entry go into the system prompt (8 KB budget). Bodies do not, so the
  prompt stays small no matter how much is remembered.
- **Recall per turn.** Entries whose names, descriptions or bodies overlap
  the request clearly are attached to that turn (up to four, 6 KB); you
  see `memory recalled: …`. Recalled entries count as used, and the least
  used, oldest entries are the ones trimmed past 200 per scope.
- **The model has a `memory` tool**: `recall` by query, `remember` when
  you tell it something worth keeping (it picks type and scope), `forget`.
- **Automatic extraction.** After a turn that changed something, or one
  where you gave feedback ("never…", "always…", "prefer…"), a cheap extra
  call asks what the conversation *established*, as typed JSON entries
  that may also replace an entry that became wrong. Duplicates and
  near-duplicates merge instead of piling up. `"memory": false` turns it
  off; `/remember` and the tool still work.
- `/memory` lists entries, `/memory <words>` recalls, `/memory show <name>`
  prints one, `/remember [-g] <fact>` adds (`-g` global), `/forget
  <name|number>` removes. Files are plain markdown you can edit by hand.

Recall matches words by default. With `"embedding_model": "text-embedding-3-small"`
(or any model your provider serves at `/v1/embeddings`) it also ranks
entries by meaning, so "verify the build works" finds a note about running
`make test`. Vectors are cached next to the entries and recomputed only
when an entry changes; if the endpoint fails, recall silently falls back to
words and retries a few minutes later.

### Skills

A skill is a markdown file with instructions for one kind of task
(`.janetcode/skills/<name>/SKILL.md` or `.janetcode/skills/<name>.md` in
the project, `skills/` under the config directory for all projects;
`.claude/skills` is read too). A directory skill can ship scripts and
reference files next to `SKILL.md`; they are listed with absolute paths
when the skill is attached so the model can read or run them. Optional
frontmatter gives `name`, `description`, comma-separated `keywords` (or
`tags`), `disable-model-invocation: true` (only you can invoke it) and
`user-invocable: false` (only the model can):

```markdown
---
description: Ship a release to production
keywords: release, ship
---
1. `make test` must pass ...
```

Every model-invocable skill is listed by name and description in the
system prompt. When a request clearly matches one (its name or a keyword
in the text, or several words of its description and headings), the file
is attached to that turn (up to five, not re-attached while still fresh
in the context) and you see `skills attached: …`. For anything else the
model can read a skill with the `skill` tool. You can run one yourself as
`/<name> args` (`$ARGUMENTS` in the body is replaced), and `/skills`
lists what is installed. Skills are re-read from disk every turn, so
editing one takes effect immediately.

### Background jobs

`bash` with `background: true` starts a command detached (its own
process group, output to a log file) and returns a job id at once, so a
dev server or a long build does not block the turn. When the job ends,
the model is told before its next call and you see `⚙ job 1 finished with
exit 0` in the transcript, with the last lines of output. The `job` tool
lists jobs, shows output, waits with a timeout, or kills one; `/jobs` and
`/jobs kill <n>` do the same for you. Every job is stopped when janetcode
exits.

### Editing subagents in a worktree

`task` with `edit: true` gives the subagent the full tool set inside a
throwaway `git worktree` of the project (uncommitted changes included, via
a snapshot). It edits and runs commands there without asking, and the
main agent gets back its report plus the diff: a `--stat` summary, the
patch inline, and the patch saved to a file so it can be applied with
`git apply` after review. The real tree is untouched until then, and
your deny rules still apply inside the worktree. This needs a git
repository.

### A whole session in a worktree

`--worktree <branch>` runs the session in a `git worktree` checked out
under `.git`, where nothing scanning the project will find it. The tree
you are sitting in is never touched, two sessions can work on one
repository at once, and what comes out is an ordinary branch to review
and merge rather than a patch to apply. Naming the same branch again
continues in the same checkout, and the session ends by saying what the
branch holds and how to merge it.

```sh
janetcode --worktree try/new-parser
# worktree: created /repo/.git/janetcode-worktrees/try-new-parser on branch try/new-parser
# … work …
# worktree: branch try/new-parser has 3 commits
# merge it with: git merge try/new-parser
```

### Sending a job to another model

`task` takes a `model`, so a job can go to a cheaper or faster model for
wide reading, or a stronger one for the part that has to be right, while
the session stays where it is. A name resolves to whichever configured
provider serves it: an explicit `routes` entry in the config first, then
a provider that lists the model, then the family the name belongs to
(`glm-*` is Zhipu, `claude-*` is Anthropic, `deepseek-*`, `kimi-*`,
`grok-*`, `qwen*`) when that provider is configured, and the session's
provider otherwise. Ask in words — "explore this codebase with glm-5" —
and the model passes the name through.

A name nothing serves is refused rather than sent to an endpoint that has
never heard of it, and `/model` follows a name to another provider
instead of setting it on the wrong one. `inspect providers` lists what is
reachable.

```json
{
  "routes": { "glm-5": "zhipu", "kimi": "moonshot" },
  "providers": {
    "zhipu": { "type": "openai", "base_url": "https://open.bigmodel.cn/api/paas/v4", "models": ["glm-5"] }
  }
}
```

### Custom agents

`.janetcode/agents/<name>.md` (or `agents/` under the config directory,
or `.claude/agents`) defines a subagent the model can pick with
`task {"agent": "<name>", ...}`: the body is added to the subagent's system
prompt, and the frontmatter may set `description`, a `tools` list
(read-only tools only) and a `model`. The `task` tool's description lists
them, so the model knows they exist.

```markdown
---
description: Reviews a change for correctness and missing tests
tools: read_file, grep, glob
---
You are a strict reviewer. Report concrete defects with file:line ...
```

### Prompt caching

The request prefix is laid out to stay byte-identical between calls, so
providers that cache prompts (Anthropic explicitly, OpenRouter and others
automatically) serve most of the history from cache: the system prompt is
fixed for the session, the memory and skill indexes inside it are evaluated
once and refreshed only at compaction (or with `/reload`), tool definitions
are static and sorted, and skills and recalled memory are attached as user
blocks rather than by editing the prompt. Pins, plan mode and read-only
mode do change the prompt, and each change costs one cache miss. The
footer shows the session's cache hit rate (`cache 87%`) whenever the
provider reports cached tokens.

### Asking for a shape

Where a reply is parsed rather than read, the request carries a JSON
schema and the provider is asked to conform to it — the compaction record
is the case that matters, since a reply that is nearly JSON loses the
whole structure. It is safe everywhere: a provider that does not know
`response_format` never sees the field, one that rejects it is learned
from on the 400 and the call retried without, and the reply is parsed the
same way regardless. The constraint raises the odds; nothing depends on it.

### File viewer and hunk review

`/open internal/x/x.go:120` shows the file inline with line numbers,
centred on the line. `/diff` lists what differs from HEAD (files with
hunk counts, new and deleted files); `/diff path` shows that file's
hunks, numbered; `/diff revert path:2` puts one hunk back the way HEAD
has it, leaving the rest of the file's changes in place. `/undo` still
reverts a whole turn at once.

### Repository map

Every request starts with a map of the repository ranked for it: which
files matter and the symbols they define, ordered by how often other
files use them. Files are nodes, "A uses a symbol B defines" is an edge
(Go is parsed with the real parser, other languages by patterns), and a
PageRank walk gives the structural weight. The request then pulls files
up: a path or file name it mentions (×4), a directory (×3), a symbol or
its stem (×2.5, so "compaction" finds `Compact`), and files that differ
from HEAD (×2.5). The map is about 1500 tokens, attached to the user
turn so every model call of the turn shares it as cached prefix, and
each line says why a file was pulled up; the turn note says how many
files the request pulled up. The
`repo_map` tool re-ranks for another query or shows a symbol's blast
radius: what defines it, every file that uses it, and with
`transitive` the users of those. The index rebuilds when the tree
changes (git status or mtimes) and caps at 5000 files.

#### Real grammars, optionally

The map reads Go with the standard library's parser and every other
language with regular expressions, which cannot tell a declaration from
the same words in a comment or a string. `make build-treesitter` compiles
in tree-sitter grammars for Python, JavaScript, TypeScript, TSX, Rust and
C: declarations come from the syntax tree, and so do the identifiers
counted as references, so a name inside a docstring no longer links two
unrelated files.

It is opt-in because the grammars are C and need cgo. The default build
stays pure Go and cross-compiles to Windows and macOS; Go keeps its own
parser either way, which is more accurate than a grammar for Go.

### Change provenance: /changes, /blame, /undo n

Every file change is recorded per tool call with the model's stated
reason (the text it wrote just before the call), so the session's work
can be read and unpicked one decision at a time rather than one turn at
a time. `/changes` lists the changes grouped by turn and intent with
line counts; `/changes 4` shows that change's hunks. `/blame path`
attributes each line of a file to the change that wrote it (lines from
before the session are unmarked) and says so when the file was edited
outside the session since. `/undo 4` reverts one change on its own,
leaving the later changes to the same file in place: each hunk is
located in the file as it is now and put back, and a hunk whose lines
were edited since is refused rather than guessed. Reverts are recorded
as changes themselves. Turn-level `/undo` and `/redo` still restore
whole snapshots. Shell commands are not tracked at hunk level; a git
snapshot covers them for turn-level undo.

### Trajectory critic

The harness watches the shape of a turn, not just its last call. An
identical tool call repeated three times gets a note and five ends the
turn; an identical failing call is called out on the second try. A
successful edit resets those counts, because re-running a test after a
change is progress. Longer loops are caught as cycles: the calls of a
turn are reduced to signatures (tool, what it was about, failed or
not), and a repeating period of two to four steps that contains a
failing step (edit a file, run the failing test, edit it again, run it
again) is named in a note after three rounds and ends the turn after
six. A loop that converges never has a failing last step and is never
flagged.

Plans are checked against the repository before they are acted on.
When the model writes text and then calls tools, the names in that text
written in backticks are looked up in the repository map: a path that
does not exist, or a symbol that is neither defined nor used anywhere,
is reported in a note after the tool results (and in the footer), unless
the sentence says the name is to be added, created or written. Symbols
are matched by exact name, by bare method name and by dotted suffix, so
`http.Client` passes when the code already uses it and `Handler.Flush`
is flagged when nothing defines or calls a Flush. The eval switch is
`plan`.

The critic also learns. Every eval run records the trajectory features
of the turn (tool calls and errors, reads, edits, files edited, test
runs and failures, edits since the last test, repeats, cycle rounds,
tokens, compactions, minutes) next to its pass/fail outcome, and
`make eval-train FILES=evals` fits a logistic model to them, reporting
the cross-validated accuracy against the majority baseline; it refuses
when every run passed or every run failed, since there is nothing to
separate. Once `critic.json` exists in the config directory, the live
turn is scored every five tool calls, and the first time the chance of
success falls under 35% the model gets one note naming the features
pulling it down (`tool_errors=8, tests=0`) and is asked to step back.
The eval switch is `critic`. A critic that scores no better than
guessing the majority outcome is not saved. The bundled set has six
harder tasks tagged `hard` (a data race, an interface refactor with a
hidden test, an INI parser against hidden edge cases, a rename across
code, docs, Makefile and script, pagination bugs, a log-summary script)
that a small model fails some of the time; 35 live runs with 3 failures
were not enough to beat the baseline, so more failing runs are needed
before the critic earns its place.

### Browsing

janetcode has no browser tool of its own and does not need one: it speaks
MCP, and Chrome ships a devtools MCP server. Add it to the config —

```json
"mcp_servers": {
  "browser": {
    "command": "npx",
    "args": ["-y", "chrome-devtools-mcp@latest", "--headless", "--isolated"]
  }
}
```

— and the session gains `navigate_page`, `take_snapshot`, `click`, `fill`,
`press_key`, `wait_for`, `list_console_messages`, `list_network_requests`
and the rest, addressing elements by the uid an accessibility snapshot
gives them rather than by pixel coordinates.

Two things make this cheap rather than ruinous. The server contributes 29
tools, which is over the deferral threshold, so their schemas stay out of
every request until `tool_search` is asked for them — a browsing session
pays for them, a coding session does not. And janetcode is text-only by
construction: `Tool.Run` returns a string, and the MCP client replaces
image content with a placeholder, so a screenshot cannot quietly put a
megabyte of base64 into the context. The model works from the accessibility
tree, which is what the harnesses that measured this concluded is both
cheaper and more reliable than pixels.

It is off by default because it launches a browser.

### Calling tools from a script

Some work cannot be batched. Reading twenty files and keeping the two that
matter, looping over search hits, retrying something flaky, making the same
change across a set of files — you cannot know the next call until you have
seen the last one, so it costs one round trip per step, and every round trip
re-sends the whole conversation.

`execute_code` collapses it. The model writes a Python script; the script
imports the session's own tools and calls them; only what it prints comes
back:

```python
from janetcode_tools import glob, read_file, plain
hits = []
for f in glob(pattern="**/*.go").splitlines():
    if "LegacyTimeout" in plain(read_file(path=f)):
        hits.append(f)
print("\n".join(hits))
```

The file bodies that went through that loop never entered the context.

Measured over three fan-out tasks × five runs each, with Sonnet 4.5, and
compared against the same tasks with the tool switched off: **cost per
solved task x0.88 [0.75, 0.99]** — about a tenth cheaper — with the pass
rate unchanged at 15/15 and context within 3%. The model reached for it in
9 of 15 runs, and 164 of its 252 file reads happened inside a script.

That is a smaller claim than it started as. The first version of this tool
*lost* 26% of context in the same measurement, because a quarter of the
scripts died on `read_file("path")` — the generated stubs took keyword
arguments only. [The report](../benchmarks/script/REPORT.md) has the
diagnosis, the fix, and the caveat that a bootstrap over three tasks is
coarse.

The calls a script makes are real calls. They go through the same hooks,
the same permission rules, the same approval prompt with the same diff, and
onto the same event log — a script saves round trips, it does not get
around the user. Read-only mode refuses a script's edits exactly as it
refuses the model's. A script gets five minutes, fifty tool calls and 40 KB
of output, and what it read is copied to the archive, so `expand_output`
can still find it afterwards. `inspect scripts` shows every script that
ran, the calls it made and what it printed.

The one sharp edge is that a script parses tool output as text, and a wrong
guess about the format becomes a confident wrong answer rather than an
error. `read_file` and `grep` number their lines; `plain()` strips that,
and the tool's description says so.

`JANETCODE_OFF=script` turns it off for a session, along with the other
`-eval-off` names.

### Inspecting the session

A session should not have to be guessed at. `/inspect <subject>` in the
terminal, `--inspect <subject>` on the command line, and an `inspect` tool
the model can call on itself all report the same thing:

| subject | what it answers |
|---|---|
| `context` | where the window goes, by part, with the largest results still in it |
| `messages` | the conversation **as the model will receive it** — pruned results, injected notes and compaction summaries included |
| `prompt` | the system prompt verbatim, with project context and pins |
| `events` | the append-only log; `events #12` opens one whole |
| `archive` | the full tool outputs kept for retrieval; `archive ctx-4` reads one |
| `retrieval` | every archive lookup and whether it found anything |
| `compaction` | when the history was summarised and what the summaries cost |
| `verdicts` | what each turn demonstrated, against what it claimed |
| `todos`, `goals` | the plan and the session goals |
| `scripts` | every `execute_code` run: the code, the calls it made, what it printed |
| `memory` | the working record carried across compactions |
| `tools`, `stats`, `trajectory` | what is loaded, what has run, what the critic scores |
| `config`, `providers`, `permissions` | how it resolved and what may run unasked |
| `lsp`, `mcp`, `jobs`, `subagents`, `skills`, `notes` | the subsystems |
| `repomap [query]` | the map and how files rank for a request |
| `undo` | file changes recorded this session |
| `all` | every subject at once |

Thirty-one subjects in all; `inspect list` names them. They report the live
state, and each is reachable the same way from the terminal, the command
line and the model, so "why did it do that" is one question rather than
three different ones.

`/context` remains a shortcut for the first. `/pin <note>` or `/pin @path`
keeps a note (or a file's content, up to 16 KB) in the system prompt for the
rest of the session; pins survive compaction and session resume, `/pin`
lists them and `/unpin <n>` drops one.

### Getting back what compaction dropped

Compaction summarises the old part of a conversation and drops the rest,
which is where the tokens go — and a summary keeps *that* a file was read,
not what was in it. So nothing is actually thrown away: the full text of
every long tool result, and of the conversation compaction replaces, goes
to an archive on disk, and `expand_output` reads it back.

- `query` searches it in words — "the compile error in the router" — and
  returns the best passages ranked, with their neighbours.
- A query naming a file and a line, like `line 36 of ledger/parse.py`,
  looks that line up exactly; numbered tool output carries the file's own
  line numbers into the archive.
- `grep` takes a regexp, and an `id` with `offset`/`limit` reads a slice.
- Entries are labelled with what the call was about, so the listing says
  `ctx-12 read_file ledger/parse.py` rather than just `read_file`.

Every compaction note ends by saying it is a summary and that exact values
live in the archive, because a model that takes a summary for the whole
truth reconstructs paths and error strings plausibly and wrongly. A model
without tools to hand can write `NEED: <query>` instead and gets the
excerpts back.

`inspect retrieval` lists every lookup and whether it found anything —
which is how the recall numbers below were arrived at rather than guessed.

Measured on sessions with compaction forced: **0 of 16** exact facts
recalled with no archive to read, **9 of 20 (45%)** with it, and every
correct answer appears verbatim in what the model fetched rather than
coming from memory. See `docs/benchmarks/compaction/FACTS.md`.

### What a turn actually demonstrated

A model that edits three files and reports "done, tests pass" is making a
claim. The harness watched the turn, so it decides for itself and says so
at the end of every turn that changed code:

The TUI shows the verdict as a line under each turn, with its mark, and
leads the status bar with it; headless mode prints it as `[verdict]` on
stderr.

| verdict | what it means |
|---|---|
| **verified** | tests ran and passed afterwards (and it says so when a failing check flipped to green) |
| **self-checked** | tests passed, but the tests themselves were edited in the same turn |
| **not demonstrated** | a check passed without exercising anything, or the code ran with nothing asserting the result |
| **unverified** | code was edited and nothing was run against it |
| **check failed** | something ran and came back red |
| **vacuous** | a check exited 0 having found no tests to run |

When the answer claims something the turn does not show — and only then —
the model is told once, with what was observed, and asked to run the check
or say plainly that it did not. Nothing blocks: a gate that refuses to let
a turn finish wedges more work than it saves. `inspect verdicts` shows the
history with each claim beside its evidence, and `--eval-off verdict`
turns it off.

Calibrated by replaying 97 real turns: see
`docs/benchmarks/compaction/FACTS.md` and the commit history for what the
first version got wrong and how it was fixed.

### Plan mode and read-only mode

`/plan add rate limiting to the API` puts the session in plan mode: the
model only gets read-only tools, explores the code and writes a numbered
plan under `## Plan` (files, order, verification, risks). Ask questions or
request changes to the plan as normal messages; `/plan approve` leaves the
mode and tells the model to execute the plan step by step, verifying each
step. Every step is a normal turn, so `/undo` walks it back. `/plan off`
leaves without executing.

`/readonly` (or `--readonly` on the command line) is the same restriction
without the plan: useful for reviews and for letting the model answer
questions about a codebase you do not want touched. In both modes a
mutating call the model tries anyway fails with a message rather than
asking you.

### Verification gates

A project can define what "done" means. `.janetcode/verify.json` lists
commands that must pass after any turn that changed something:

```json
{"commands": ["go build ./...", "go test ./..."], "timeout": 600}
```

When the model ends a turn after editing or running something, the
commands run in order. A failure is shown to you and fed back to the model
as a message with the exit code and output, and it keeps working; after
three failed rounds it stops and tells you. A pass shows as
`verified: …`. Read-only turns skip the checks, and the checks do not
rerun until something changes again.

Passing tests prove little when the change is not covered. With
`"mutate": "go test ./..."` in the file (or `/verify mutate "go test
./..."`), once the commands pass janetcode takes the lines the turn
changed, flips one operator at a time (`==`/`!=`, `<`/`<=`, `&&`/`||`,
`+`/`-`, `true`/`false`), runs the test command against each mutant in
a throwaway git worktree of the current tree, and treats a mutant the
tests let through as a failure: the model is told which line's behaviour
is not pinned down and asked to add a test (or say why the mutation is
equivalent). At most twelve mutants run per turn (`max_mutants`), spread
across the changed lines, so the cost is bounded by twelve test runs.
Test files and comments are never mutated.

`/verify` runs the checks now, `/verify set "make test"` adds a command to
the file, `/verify clear` removes them all. Like hooks, a verify file that
came with a clone needs a one-time `/verify trust` before it runs, because
it executes commands on your machine; files you write through `/verify
set` are trusted as you write them. In headless mode `--until` remains the
one-off equivalent.

### Diagnostics from language servers

When a language server for the file is on PATH, every `write_file` or
`edit_file` result carries its diagnostics, so the model sees a type error
in the same step that caused it rather than at the next build:

```
[diagnostics]
internal/x/x.go:14:9: error: cannot use "no" (untyped string constant) as int value in return statement (compile)
```

gopls, pyright-langserver, typescript-language-server, rust-analyzer and
clangd are used by extension; each starts on the first edit of a matching
file and is remembered as unavailable when missing. The `diagnostics` tool
re-checks a file or lists every known problem, and `/lsp` shows server
status. Configure or disable under `"lsp"` in the config file:

```json
"lsp": {
  "timeout": 8,
  "servers": {
    "pyright": {"command": ["basedpyright-langserver", "--stdio"], "extensions": [".py"]},
    "clangd": {"command": []}
  }
}
```

An empty command removes a default; `"disabled": true` turns diagnostics
off.

#### Asking the server about the code

Diagnostics say what is wrong; `symbols` asks what is *there*. It answers
where a name is declared, every use of it, its type and documentation,
what a file declares, and a name search across the project. The server has
the resolved program, so it tells a declaration from a mention and one
package's `Config` from another's, and it costs one call instead of a read
of every textual hit.

```
symbols {"action":"references","path":"cfg/cfg.go","symbol":"Config"}
symbols {"action":"definition","path":"main.go","symbol":"Load"}
symbols {"action":"outline","path":"cfg/cfg.go"}
```

Name the symbol rather than pointing at a line; occurrences are ranked so
a declaration wins over the doc comment above it, which is where the first
textual match usually lands.

Before a project-wide question the other files of that language are
opened, because a server reports uses only in files it knows about and a
cold project would otherwise answer "used nowhere". A document is sent
when it changes and not otherwise. An empty answer from a server that is
still indexing is retried before it is believed, `ContentModified` is
treated as the protocol's "ask again" rather than as a failure, and a
server that dies is restarted rather than being handed to every later
query.

The query surface is tested against the real binaries of all five
supported servers — gopls, pyright, typescript-language-server,
rust-analyzer and clangd — for definition, references across files, hover,
outline and workspace search.

### Hooks

Hooks are shell commands run at fixed points of the loop. They live under
`"hooks"` in the config file (every project) or in
`<project>/.janetcode/hooks.json` (needs a one-time `/hooks trust`, like
`.mcp.json`, because a cloned repository must not run commands on your
machine by itself):

```json
"hooks": {
  "pre_tool":    [{"match": "bash", "command": "scripts/policy.sh"}],
  "post_tool":   [{"match": "write_file|edit_file", "command": "gofmt -l . | sed 's/^/unformatted: /'"}],
  "pre_compact": [{"command": "echo 'Open PR: #42'"}],
  "stop":        [{"command": "go test ./... >/dev/null || { echo 'tests fail' >&2; exit 2; }"}]
}
```

Each hook gets the event as JSON on stdin (`event`, `cwd`, `tool`, `input`,
and for `post_tool` the `output`/`is_error`, for `stop` the model's `text`,
for `pre_compact` the estimated `tokens`) plus `JANETCODE_EVENT`,
`JANETCODE_TOOL` and `JANETCODE_CWD` in the environment. `match` is a
regexp on the tool name. What the hook prints decides:

- exit code 2 blocks: a `pre_tool` call is denied with stderr as the
  reason; a `stop` sends the model back to work with stderr as the message
  (at most three times per turn).
- stdout JSON `{"decision":"deny","reason":"…"}` blocks the same way;
  `{"input":{…}}` rewrites the tool input (the rewritten input is what is
  previewed, approved and run); `{"note":"…"}` is appended to the tool
  result (or to the handoff note for `pre_compact`);
  `{"continue":"…"}` on `stop` sends the model back with that text.
- plain text on stdout is a note.
- any other exit code, a crash or a timeout (10 s by default, `timeout`
  in seconds) is logged and ignored: hooks fail open, so a broken hook
  cannot wedge a session.

`/hooks` lists what is in effect.

### MCP servers

janetcode is an MCP client over stdio. Servers listed under `mcp_servers` in
the config file start with every session. Servers listed in a project's
`.mcp.json` (the same format Claude Code uses) are shown at startup but only
start after you run `/mcp trust` once for that exact file; editing the file
asks again, because the file can run any command on your machine.

```json
{"mcpServers": {"fs": {"command": "npx", "args": ["-y", "@modelcontextprotocol/server-filesystem", "."]}}}
```

Remote servers use `"url"` instead of `"command"`; janetcode speaks
Streamable HTTP and falls back to the older SSE transport on its own.
`"headers"` adds request headers and expands `${ENV_VARS}`, so a token
never has to be written into the file:

```json
{"mcpServers": {"docs": {"url": "https://example.com/mcp", "headers": {"Authorization": "Bearer ${DOCS_TOKEN}"}}}}
```

Servers that answer 401 want OAuth. `/mcp auth <server>` (or
`janetcode --mcp-auth <server>` outside the TUI) discovers the
authorization server, registers janetcode as a client dynamically, opens
the browser for the authorization-code flow with PKCE, receives the
redirect on a loopback port and stores the token, with the refresh token,
in `mcp-auth.json` next to the config file (mode 0600). Expired tokens
are refreshed before requests; a 401 with a stored token triggers one
refresh and retry. Servers without dynamic registration take
`"client_id"`, `"client_secret"` (env references expand) and `"scope"` in
their config. `/mcp logout <server>` forgets the token.

Server tools appear as `mcp__<server>__<tool>`. They ask for permission
unless the server marks them read-only. Servers that publish resources get
`mcp__<server>__list_resources` and `read_resource` tools, and every prompt
a server offers becomes a slash command: `/mcp__<server>__<prompt> args`.
`/mcp` shows status, tools and prompts.

### Custom commands

Drop a prompt template at `.janetcode/commands/<name>.md` in the project (or
`~/.config/janetcode/commands/<name>.md` for every project) and `/<name> args`
submits it with `$ARGUMENTS` replaced. The first line is the help text shown
in tab completion.
