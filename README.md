# janetcode

A terminal coding agent in a single Go binary. It reads and edits code, runs
commands, and iterates on the result, with every mutating action shown to you
as a diff before it happens. Works with Anthropic's Messages API and any
OpenAI-compatible endpoint (OpenAI, xAI, Qwen, DeepSeek, OpenRouter, vLLM,
Ollama).

## Install

```sh
git clone https://github.com/xmonader/janetcode
cd janetcode
make install        # puts janetcode on your $GOPATH/bin
```

Requires Go 1.25+.

Linux and macOS are where janetcode is developed and tested. Windows
support is best effort: `GOOS=windows go build ./...` (`make cross`) is
kept green and the shell paths are switched by OS, so `bash`, hooks,
verification commands, `--watch`/`--until` checks and background jobs
run through PowerShell (`pwsh` if installed, else Windows PowerShell)
with the command passed as `-EncodedCommand` to avoid quoting rules, and
cancelled commands are killed with `taskkill /T`. None of that has been
exercised on a real Windows machine yet, and the command safety
classifier still reasons about POSIX shell syntax, so treat it as
untested. The cheapest way to use janetcode on Windows today is WSL.

## Quick start

```sh
export ANTHROPIC_API_KEY=sk-ant-...
cd your/project
janetcode
```

Or with an OpenAI-compatible backend. Tested live against Alibaba's coding
endpoint and OpenRouter:

```sh
# Alibaba Model Studio (Qwen coding plan)
export OPENAI_API_KEY=sk-...
export OPENAI_BASE_URL=https://coding-intl.dashscope.aliyuncs.com/v1
janetcode --model qwen3.7-plus

# OpenRouter
export OPENAI_API_KEY=sk-or-...
export OPENAI_BASE_URL=https://openrouter.ai/api/v1
janetcode --model deepseek/deepseek-v4-flash

# Ollama (no key needed for a local server)
export OPENAI_BASE_URL=http://localhost:11434/v1
janetcode --model qwen3-coder
```

Or skip the environment and run `/connect` inside the TUI: pick a preset
(Anthropic, OpenAI, OpenRouter, Alibaba Qwen, DeepSeek, Moonshot, Zhipu,
MiniMax, Gemini, xAI, Mistral, Groq, Cerebras, Together, Fireworks, Ollama,
LM Studio) or a custom OpenAI- or Anthropic-compatible URL, paste one or
more API keys, choose a model from the server's list, and the choice is
saved to the config file. Several keys for one provider are rotated
automatically when one is rate limited or rejected.

The provider is inferred from whichever key is set (`--provider` overrides
it, and accepts any configured or preset name). Put `model` and the keys in
the config file (see below), or export `JANETCODE_MODEL`, to skip the flag.
A prompt given as an argument starts the session with it:

```sh
janetcode "explain how requests flow through this service"
```

One-shot, scriptable mode:

```sh
janetcode -p "add a --verbose flag to cmd/server" --yolo
```

Assistant text goes to stdout, tool activity to stderr. Without `--yolo`,
mutating tools prompt on the terminal, or are denied when stdin is not a TTY.

`--until "<command>"` keeps the run going until a shell command exits 0:
after each turn the command runs in the working directory and its output
goes back to the model on failure, at most five rounds. The verdict is the
exit code, never the model's opinion.

```sh
janetcode -p "make the flaky TestSync deterministic" --yolo --until "go test -count=3 ./internal/sync/"
```

`--watch "<command>"` is the reverse: janetcode stays running, re-runs the
command whenever files under the directory change (you edit in your own
editor), and when it fails sends the output to the model to fix. A pass
prints `✓`; five failed fixes in a row make it wait for your next change.
`-p` adds standing instructions for the fixes; `--yolo` lets it edit
without prompts, which is what you want here:

```sh
janetcode --watch "go build ./... && go test ./..." --yolo -p "keep the tests green; never edit *_test.go"
```

`janetcode --doctor` checks the config file, every configured provider's
keys, a live call to the selected model, the sessions directory, git, local
tools, MCP servers, the project's hooks, verification commands and
permission rules (and whether they are trusted), skills, the memory store
and which language servers are installed. It exits non-zero when
something is broken; a missing optional feature is only a warning.
`/stats` in the TUI shows the session's turns, model calls and wait time,
tool calls per tool with failures, tokens, cache hit rate and cost.

## What it can do

| Tool | Purpose | Asks permission |
|------|---------|-----------------|
| `read_file` | Read a file with line numbers, windowed | no |
| `list_dir` | List a directory | no |
| `glob` | Find files by pattern (`**/*.go`) | no |
| `grep` | Search file contents with a regex | no |
| `edit_file` | Replace text in a file, located by exact string, line range or content anchor; several edits in one call | yes, shows a diff |
| `write_file` | Create or overwrite a file | yes, shows a diff |
| `bash` | Run a shell command with a timeout | yes |
| `fetch_url` | Fetch a web page or API response as text | yes |
| `repo_map` | Rank files for a query, or a symbol's blast radius (definers and users) | no |
| `web_search` | Search the web (DuckDuckGo, or Brave Search with a key) for titles, URLs and snippets | yes |
| `task` | Delegate a job to a subagent with its own context, optionally on another model; several run in parallel | no |
| `symbols` | Ask the language server where a name is defined, who uses it, its type, what a file declares | no |
| `expand_output` | Read back tool output and conversation that compaction archived, by search or by line | no |
| `execute_code` | Run a Python script that calls these tools, so a chain of calls costs one turn and their output stays out of the context | yes, shows the code |
| `todo` | Keep the plan for a multi-step task and mark steps off; triggered by a countable rule (three or more distinct steps) after [a subjective one was never reached for](docs/benchmarks/harnesses/session/README.md) | no |
| `inspect` | Look at the session's own state: context, archive, verdicts, config, servers | no |
| `diagnostics` | Compile and type errors from the language server, for a file or the project | no |
| `job` | List, read, wait on or kill a background job | no |
| `memory` | Remember a fact about the project, or search what is remembered | no |
| `skill` | Load a skill's instructions on demand | no |
| `goal_status` | Mark the session goal complete or blocked | no |
| `tool_search` | Find a tool by what it does, when many are loaded | no |

All paths are confined to the working directory, symlinks included.
Read-only tools run concurrently; mutating tools run one at a time, in order.

## The interface

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
ctrl+c           cancel; twice to force quit; quits when idle
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

`ctrl+c` cancels a running turn, then clears typed text, then quits.

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
arguments only. [The report](docs/benchmarks/script/REPORT.md) has the
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

## Editor integration (ACP)

`janetcode --acp` speaks the [Agent Client Protocol](https://agentclientprotocol.com)
on stdin and stdout, so any ACP editor can launch it as a subprocess and
drive it: `initialize`, `session/new`, `session/load` (which replays the
stored conversation), `session/prompt` streamed back as `session/update`,
and `session/cancel`. Tool calls stream as `tool_call` and
`tool_call_update` with the protocol's kinds, a title naming what the call
is about, and the file it touches; approvals go to the editor as
`session/request_permission`, so you answer where you are working.

Nothing but protocol frames reaches stdout — logs go to stderr. Point your
editor's ACP agent configuration at the binary with `--acp`.

It is driven in the tests by Zed's own client library
(`@zed-industries/agent-client-protocol`), with every frame checked
against their published schemas, and the stop reasons, permission kinds
and tool kinds are pinned to that schema so they cannot drift. Cancelling
mid-turn returns `cancelled` and leaves the session usable; a frame that
is not JSON is answered with a parse error and the connection carries on.

## Configuration

Precedence: command-line flags > environment > config file.

```sh
janetcode --init     # writes ~/.config/janetcode/config.json
```

```json
{
  "provider": "anthropic",
  "model": "claude-opus-5",
  "max_tokens": 16000,
  "effort": "high",
  "auto_approve": false,
  "max_iterations": 50,
  "compact_at": 80000,
  "memory": true,
  "theme": "auto",
  "providers": {
    "anthropic":  { "type": "anthropic", "api_key": "sk-ant-..." },
    "openrouter": { "type": "openai", "base_url": "https://openrouter.ai/api/v1",
                    "api_key": "sk-or-1", "api_keys": ["sk-or-2"], "model": "deepseek/deepseek-v4-flash" },
    "ollama":     { "type": "openai", "base_url": "http://localhost:11434/v1" }
  }
}
```

`web_search` scrapes DuckDuckGo's HTML endpoint by default, which needs no
key but can rate-limit heavy use. For the Brave Search API set
`"search": {"provider": "brave", "key": "${BRAVE_API_KEY}"}`; `url`
overrides either endpoint (a proxy or a mock).

`providers` holds any number of backends by name; `provider` selects one.
`type` is `anthropic` or `openai` (any OpenAI-compatible server). A preset
name needs no `base_url`. `api_keys` lists extra keys to rotate through on
429/401/403 responses, and each entry remembers its last `model`, so
`--provider openrouter` comes back with the model last used there. The older
`anthropic`/`openai` sections are still read.

Environment: `ANTHROPIC_API_KEY`, `ANTHROPIC_BASE_URL`, `OPENAI_API_KEY`,
`OPENAI_BASE_URL` (a key may be a comma-separated list), `JANETCODE_PROVIDER`,
`JANETCODE_MODEL`, `JANETCODE_EFFORT`, `JANETCODE_THEME`. Keys from the
environment are never copied into the config file.

`effort` (Anthropic only) is `low`, `medium`, `high`, `xhigh` or `max`.
Adaptive thinking is on for Claude 4.6+ models and off for Haiku; set
`"thinking": false` to override.

### Project instructions

If the working directory contains `JANETCODE.md`, `AGENTS.md` or `CLAUDE.md`
(first found wins), its contents are added to the system prompt.

## Sessions

Every conversation is saved as append-only JSONL under
`~/.local/share/janetcode/sessions/`.

Every session gets a title after its first turn: the model is asked for a
few words (a headless run uses the first prompt instead), and `/title
<text>` renames one. `/fork [title]` branches the conversation: a new
session starts with a copy of the messages and pins, the original stays
as it was, and `/resume` lists both so you can try two approaches from the
same point. `/history <words>` searches every saved session in every
directory for the words and shows the best matching line with the session
id to `/resume`.

```sh
janetcode --sessions        # list sessions for this directory
janetcode --continue        # resume the newest one
janetcode --resume <id>
```

Inside the TUI, `/resume` lists the sessions for the directory and
`/resume 2` reopens one.

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

## Evals

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

## Command-line reference

```
janetcode [flags] [prompt]        interactive session in the working directory
janetcode -p "prompt" [flags]     one prompt, headless, then exit
```

| flag | what it does |
|---|---|
| `-p "<prompt>"` | run one prompt headless; assistant text on stdout, tool activity on stderr |
| `--model`, `--provider` | override the configured model or backend for this run |
| `--effort` | reasoning effort where the provider supports it |
| `--yolo` | run mutating tools without asking |
| `--readonly` | read, search and answer, but change nothing |
| `--continue`, `--resume [id]` | carry on the last session here, or a named one |
| `--sessions` | list the sessions recorded for this directory |
| `--worktree <branch>` | run the session in a git worktree on that branch |
| `--until "<cmd>"` | keep going until the command exits 0, at most five rounds |
| `--watch "<cmd>"` | stay running, re-run the command when files change, fix failures |
| `--acp` | speak the Agent Client Protocol on stdio, for an editor |
| `--inspect <subject>` | print one part of the session state and exit; `list` names them |
| `--doctor` | check config, credentials, providers, tools and report what is wrong |
| `--init` | write a starter config file |
| `--allow`, `--deny` | add a permission rule for this run |
| `--mcp-auth <server>` | run the OAuth flow for an MCP server |
| `--version` | print the version |

Eval flags (`--eval` and the rest) are documented under [Evals](#evals).

## What has been measured

Numbers live in `docs/benchmarks/`, with the runs that produced them.

- **Against Claude Code**, on Claude models, all twelve cells passing: on
  haiku janetcode read 2.6M tokens to Claude Code's 4.2M and cost $0.54
  against $1.49; on sonnet the token counts are a dead heat (3.25M against
  3.23M) but Claude Code got there in 80 model calls to janetcode's 173.
  `docs/benchmarks/harnesses/claude/`.
- **Against other harnesses**, same model and endpoint: on three
  multi-prompt sessions every harness passed every task, and the context
  read to get there was janetcode 985k, pi 1.95M, grok 3.31M, qwencode
  16.4M. The gap is mostly fixed per-call overhead — janetcode's prompt
  and schemas come to about 1.8k tokens, against 18k, 23k and 70k.
- **Across eleven models**, five harnesses (janetcode, pi, hermes-agent,
  qwencode, openclaw), three runs each — 3,300 task runs on 2026-09-10:
  janetcode passed 651/660, the most, at $1.17 at list price for all
  eleven, the lowest cost per solved task (0.18¢ against hermes-agent
  0.25¢, pi 0.47¢, qwencode 1.19¢, openclaw 1.28¢), first or second on
  cost on every model. Its context read stays near 1M tokens per 60 tasks
  on every model while qwencode and openclaw read 7–12M.
  `docs/benchmarks/harnesses/eleven-models/`.
- **The hard set across harnesses** (tasks 21–26, qwen3.7-plus, three
  runs): 87/90 passed — janetcode, pi and openclaw 18/18 — so it does not
  separate harnesses on this model either. janetcode's fresh input is
  again lowest (qwencode 6x) and its output is twice qwencode's, spent
  writing and running its own tests on the INI parser with hidden tests,
  which it passed 3/3 where qwencode passed 1/3.
  `docs/benchmarks/harnesses/qwen-hard/`.
- **The session set across five harnesses** (three sessions of six or
  seven dependent prompts, qwen3.7-plus, two runs): every harness that
  continues a session passed all six; hermes-agent, which cannot, passed
  two. Context read for the six sessions: janetcode 2.0M, pi 3.9M,
  openclaw 6.1M, qwencode 34.2M — the widest gap of any set, and at list
  price $0.22 against $0.37, $0.55 and $3.64.
  `docs/benchmarks/harnesses/qwen-session/`.
- **Compaction** is safe on this set: forcing it on tasks that would not
  otherwise reach the threshold lost no task, 11/11 either way.
- **Recall after compaction**: 0/16 exact facts with no archive, 9/20 with
  it, every correct answer grounded in what was fetched.
- **What turns demonstrate**, replayed over 97 real turns: 42% verified,
  32% self-checked, 10% not demonstrated, 10% unverified, no false
  accusations.

One caution the benchmark makes plain: running the session set three times
gives 940k, 774k and 843k — a **21% spread between identical runs**, and
113% on the search-heavy task. Single-run comparisons on it are not
evidence, and anything reported from it should be `--eval-runs 3` or more.

## Development

```sh
make build     # bin/janetcode
make test
make race
make lint
```

Layout:

```
cmd/janetcode         flags and wiring
internal/llm          provider-neutral messages, tools, streaming events
internal/llm/anthropic  Messages API via the official SDK
internal/llm/openai   chat-completions client (hand-rolled, dialect-tolerant)
internal/tool         tool interface, registry, and the built-in tools
internal/agent        the model/tool loop, permissions, compaction
internal/session      JSONL persistence
internal/tui          bubbletea interface
internal/headless     one-shot runner
internal/diff         unified diffs for previews
```

All tests run against fake servers; no network or API key is needed.
