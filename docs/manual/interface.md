# The interface

Part of the [janetcode manual](README.md); the [README](../../README.md) has the short version.

The transcript streams the model's markdown as it arrives, shows each tool
call on one line with what it did and how long it took, and renders every
edit as a diff with line numbers: three lines of context around each
change, "… N unchanged lines" where a gap was skipped, and for a
single-line replacement the changed tokens picked out in inverse video.
The status line shows what the agent is doing (including what it is
waiting on: a retry and its countdown, a compaction, a subagent), the
elapsed time, and a gauge of how full the context window is. The input box
grows with your text.

```
enter            send        ctrl+j / alt+enter   newline
up / down        recall earlier prompts          tab   complete a /command
                 or its argument (/resume, /model, /forget, /undo, /open, /diff);
                 a name matches on its letters in order, so /rsm finds /resume
esc              cancel the running turn (or clear the input)
ctrl+o           expand the last turn's output   ctrl+l   redraw
                 (again folds it; click a tool
                 line or thought to fold one;
                 /verbose does every turn)
pgup / pgdn      scroll (mouse wheel works)
ctrl+c           cancel the turn; when idle, press twice to quit
```

## Colour and themes

A theme is a full named palette: every colour role the interface draws
with (accent, dim, faint, warn, err, ok, user, tool, the header
foreground and background, the two border colours, and the two diff
colours), plus the transcript's left gutter. The `default` theme is the
palette janetcode has always drawn and is what you get if you never pick
one, so an upgrade changes nothing on screen.

The terminal's background is asked for once at start. `theme: auto` (the
default) uses that answer to draw the default palette light or dark;
`theme: dark` or `theme: light` pins it; `theme: <name>` selects a builtin
or a theme you defined. `NO_COLOR` in the environment renders everything
plain whatever the theme. A terminal that reports only the sixteen ANSI
colours keeps the ANSI-named default palette rather than truecolor hex
quantised down — several of the dark foregrounds would otherwise land on
black, which is the background; the `mono` theme is built on ANSI indices
for exactly that terminal.

The builtin themes (`--inspect themes` lists them with their mode and
description):

| theme | mode | character |
|---|---|---|
| `default` | adapts | the historical palette; light or dark to match the terminal |
| `solarized-dark` | dark | Ethan Schoonover's solarized, warm and low-glare |
| `solarized-light` | light | solarized on paper |
| `high-contrast` | dark | accessible: bright, saturated, well past the contrast floor |
| `muted` | adapts | low-saturation greys with just enough tint to tell roles apart |
| `mono` | adapts | near colourless; greys plus the terminal's own red/green/blue |
| `gruvbox` | dark | warm retro amber and olive |
| `nord` | dark | cool arctic blues |

Every builtin clears a contrast ratio of about 3.5:1 for body text against
the background it assumes (borders may sit lower); a test holds each one to
that bar, so a shipped theme is always legible.

### Switching themes: `/theme`

`/theme` alone opens a searchable list of every theme with its
description; enter applies the highlighted one live — the whole transcript
recolours in place, no restart — and saves the choice to the config file.
`/theme <name>` switches directly, `/theme list` prints them, and
`--theme <name>` sets one for a single run without touching the config.
Switching never re-asks the terminal for its background, so no keystroke is
lost; selecting a single-mode theme (a dark-only one, say) pins the light
or dark assumption to that theme.

### Room to breathe: the gutter

The transcript, the header, the status line and the input box share one
left margin — the theme's gutter, two columns by default — so the
top-level lines (the `✓` tool, `·` thought, `❯` prompt) step in from the
edge and the body indent under them steps in consistently from there. On a
terminal narrower than 88 columns the gutter collapses to zero, so diffs,
code and tables keep every column they can get. A dense theme can set its
gutter to `0` to switch the margin off at every width. The vertical rhythm
— one blank line between blocks — is unchanged; this is horizontal only.

## The terminal takes part

janetcode tells the terminal what is happening, so the terminal can help:

- **OSC 133 zones** mark where each turn starts, where the model's answer
  begins and where the turn ended, with its exit code. Terminals that
  understand them give you prev/next-prompt keys that jump between turns.
- **A desktop notification** when a permission prompt appears or a turn
  ends *while the window is unfocused* — never while you are looking at
  it. OSC 9, with the kitty (OSC 99) and rxvt (OSC 777) dialects, and a
  bell as the fallback. Focus is tracked with DEC 1004 focus reports; a
  terminal that never sends them is treated as focused and stays quiet.
  `JANETCODE_NOTIFY=0` turns notifications and the bell off.
- **The window and tab title** is the session title once one has been
  generated, and the working directory before that.

At a permission prompt: `y` allow once, `a` allow that tool for the session,
`n` deny and type why (the reason goes to the model, enter skips it), `esc`
deny silently, `c` cancel the whole turn. Destructive shell commands (recursive deletes, force pushes,
sudo, piping downloads into a shell) are flagged in the prompt.

Typing while the model is working queues the message; it is sent as soon as
the turn ends. `ctrl+s` instead hands what you typed to the turn that is
running — the model reads it before its next step. `↑` with an empty prompt
takes the last queued message back to edit, and cancelling a turn hands all
queued text back to the input, joined by blank lines.

In a short terminal the interface gives way rather than crowding out the
transcript: how much of a tool result is shown is a third of the visible
area (never fewer than four lines, never more than forty); below twenty
rows the input box and the completion menu shrink; below sixteen the
welcome banner and the cache rate go. None of it is remembered — growing
the window puts everything back.

`↑` and `↓` walk the prompts you have sent in this directory. The history
outlives the process: it is a JSONL file keyed by the working directory,
beside the sessions, capped at a thousand lines. A line typed with a `!`
prefix comes back with the prefix. `esc` while browsing puts back whatever
you were typing before `↑`.

Pasting more than ten lines (or a thousand characters) puts a chip in the
prompt — `[paste #1 +123 lines]` — instead of the text itself. The chip
deletes as one unit and is replaced by the full text when you send, so a
paste that ends in a newline can never send itself.

```
@path            attach a file, an image (png/jpg/gif/webp), or a directory listing; tab completes
ctrl+g           move the command running in the foreground to the background, as a job
ctrl+v           attach the image in the clipboard (wl-paste or xclip on Linux, pngpaste on macOS)
!command         run a shell command here; its output is shown and shared with the model
/help      /quit      /clear (new session)     /yolo (toggle auto-approve)   /edits (edits go through, commands ask)
/plan <request> (plan mode: explore, write a plan; /plan approve executes it)
/readonly  (toggle read-only: the model can inspect but change nothing)
/connect   /model     /compact (summarise the context to free the window)
/usage     /stats (turns, model calls, tool calls per tool, tokens, cost, what the harness had to recover)
/resume (searchable list of earlier sessions; /resume <n or id> reopens one directly)   /verbose   /settle (list it last)
/undo [preview]  /redo  /find X    /cost      /mcp [trust | auth <server> | logout <server>]
/budget [usd | off] (stop the session once it has spent that much; alone reports it)
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
changed anything, and takes that turn's messages out of the model's context
as well, leaving one note in their place: a transcript that still describes
edits which are no longer on disk is how a model comes to assert stale file
contents. The session log keeps the turn shadowed, not deleted. Inside a git repository the whole
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
- **A credential is never remembered.** An entry whose text holds
  something API-key-shaped — a vendor key, a private-key block, a JWT, a
  long opaque value assigned to something called a secret — is refused
  with a note saying why: memory is written to disk and read back into
  every later prompt, which is the last place a key should be. Where the
  value lives is worth remembering; the value is not.
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

### Changing janetcode's own settings always asks

Everything that decides what janetcode may do without asking is a file:
the config file, `.mcp.json`, everything under `.janetcode/` (hooks,
permission rules, verification, rules, skills, commands) and the memory
store. A model that can write files can therefore widen its own
permissions and then use them — and `--yolo`, auto-edit mode and a saved
allow rule are exactly the settings under which that would happen
unobserved.

So those paths are carved out. A `write_file`, an `edit_file` or a `bash`
command that touches one asks for permission whatever the mode, and the
prompt says which surface is being changed. Through the shell the obvious
forms are recognised: a `>` or `>>` redirect, and `cp`, `mv`, `rm`, `tee`,
`sed -i`, `patch`, `install`, `truncate` and the rest with one of those
paths as an argument. Reading them is not affected.

This is defence in depth, not a boundary: a determined model has a
hundred ways to write a file, and this catches the obvious ones. What it
buys is that the single move which turns a limited session into an
unlimited one is the one move always shown to a human.

### Encodings, byte-order marks and line endings

The model works in UTF-8 text with LF line endings; files on disk are
bytes. `read_file` detects a UTF-8 or UTF-16 byte-order mark and CRLF line
endings, shows the file as UTF-8 with LF, and says what it found; a file
that is neither UTF-8 nor marked is decoded as Latin-1, which round-trips
every byte, and the guess is named in the result. `write_file` and
`edit_file` put the form back: the mark, the line endings and the charset
the file had. A new file is plain UTF-8 with LF — nothing is invented.
Without this, the first edit in a Windows repository rewrites every line
of the file it touched, and the first edit to a Latin-1 file turns the
rest of it into mojibake.

A file with a NUL byte in its first 4 KB is binary and is refused as text
by `read_file` and `edit_file`. UTF-16 is full of NUL bytes and is text,
so a file that announces itself with a UTF-16 mark is exempt.

### When an edit's quoted text nearly matches

`edit_file` locates a change by quoting the text it replaces, and a model
quoting text it read a moment ago still gets it wrong in ways that have
nothing to do with the code: an em dash where the file has a hyphen, a
curly quote for a straight one, a non-breaking space inside an indent, a
trailing space the file does not have. After an exact match misses, the
search runs again over a folded view of both sides (the dash, quote and
space variants each stand for one character), and then again ignoring
trailing whitespace on every line. Leading whitespace is never relaxed —
in Python and YAML it is the program — and an ambiguous match on a rung
is refused exactly as an ambiguous exact match is. When a rung below
exact matching was needed, the result says so, so you can see the file
did not hold what the model thought it held.

When nothing matches at all, the failure shows the closest region of the
file as a diff against what was asked for, so the next attempt is aimed
at the text that is actually there instead of guessing again.

### Files that are not what you read

Before a write or an edit lands, janetcode checks that the file is still
the one the agent looked at, and says precisely what is wrong when it is
not: it changed on disk, it disappeared, the path is a directory, it went
binary, or — for `write_file` on a file that already exists — it was never
read at all. The instruction differs by verb, because the two tools need
different things: an edit names the text it replaces, so re-reading the
part being changed is enough; a write replaces everything, so it asks for
the whole file. An edit of a file that was never read is still allowed —
it cannot silently destroy text it did not name — and the coach supplies
the content when such an edit misses.

### When exit 1 is an answer

`grep`, `rg`, `diff`, `cmp` and `test` use exit code 1 to say "I looked
and found nothing" — no matching line, two files that differ, a false
condition. That is an answer, not a failure, so the result says so:
`[exit code 1] no lines matched; for this command that is an answer, not
a failure`, and the call is not flagged as an error. Only exit 1 is read
this way; grep's exit 2 (a bad pattern, an unreadable file) and every
other non-zero code are reported unchanged. The command whose status the
line carries is worked out the way a shell works it out — the last stage
of the pipeline — and a line joined with `&&`, where either side could
have produced the 1, stays an ordinary failure.

### Background jobs

`bash` with `background: true` starts a command detached (its own
process group, output to a log file) and returns a job id at once, so a
dev server or a long build does not block the turn. When the job ends,
the model is told before its next call and you see `⚙ job 1 finished with
exit 0` in the transcript, with the last lines of output. The `job` tool
lists jobs, shows output, waits with a timeout, or kills one; `/jobs` and
`/jobs kill <n>` do the same for you. Every job is stopped when janetcode
exits.

A command already running in the foreground can be moved to the
background instead of being waited out or killed: press `ctrl+g`. It keeps
running, the output collected so far is written to the job log from the
beginning, and it becomes a job like any other — the transcript says
`moved to background as job 1`, and the model is told when it finishes,
as for any job. The status line offers the key only while there is a
command to promote. The tool schema is unchanged, because this is the
user's decision about a command that is already running, not the model's.

`sed -i` is an edit, so it is shown as one: a `bash` call containing it is
run once without `-i` and the diff it would make appears in the permission
prompt, per file, the way an `edit_file` diff does. The preview never
touches the file.

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

`disallowed_tools` is applied after `tools`, so a tool the profile refuses
cannot be let back in by naming it in the allowlist or by the caller;
`max_turns` caps the subagent's model calls and `max_minutes` bounds its
wall time (the parent's turn is held open while it runs, so a subagent
that will not finish is finished for it, and what it found so far comes
back as its report). `approval: bubble` changes what happens when an
editing subagent needs a confirmation: instead of approving its own calls
inside the throwaway checkout, the prompt is queued to the parent's
approver — you — saying which subagent is asking.

```markdown
---
description: Fixes a failing test in its own checkout
disallowed_tools: fetch_url, web_search
max_turns: 20
max_minutes: 10
approval: bubble
---
Make the failing test pass. Do not weaken the test.
```

A subagent that finished is not gone: `/agents` lists the continuable
subagents this session started with the `task` tool, so when one has
found something and the answer it wrote back is not the whole of it, the
conversation with that subagent can be carried on rather than started
again from an empty context.

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

### What fetch_url will not do

A fetch is approved for one URL, so it stays on that URL. A redirect to a
different host, or a redirect from https down to http, is handed back to
the model instead of followed — the permission was for the host that was
asked about, and a redirect is another host's chance to be fetched under
it. A same-host redirect is followed as usual.

A GitHub blob URL is rewritten to `raw.githubusercontent.com` before the
permission check, so the host approved, and any rule saved, is the host
actually contacted — and what comes back is the file rather than the
several hundred kilobytes of page around it.

A Content-Type is the server's claim; the bytes are not. A body whose
leading bytes are a PNG, a PDF, a zip, gzip or an ELF binary is named and
not read, whatever it was served as.

A URL pointing at the local machine or network — loopback, a private
range, a link-local address (on a cloud instance, the credentials
endpoint), `localhost`, `*.local`, `*.internal` — is fetched only when the
user named that host themselves in their own message. The agent reaching
one on its own initiative is reading something nobody offered it. Every
message you send counts, not only the first: a prompt, a note steered
into a running turn with `ctrl+s`, and a `!command` you ran yourself all
add the hosts they name.

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

The first `tool_search` that reveals anything reveals the rest of the
deferred set too, as far as a twentieth of the context window will hold
their schemas. Revealing a tool changes the tool list, and the tool list
is part of the cached request prefix, so each reveal makes the model pay
for the whole prefix again: one at a time, a browsing session pays that
several times over. The batch happens once — a second would be a second
break — and a deferred set too large for the budget stays deferred and is
revealed by name as it is asked for.

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

### Asking janetcode about janetcode

`/help` alone is still the keys-and-commands sheet. `/help <topic>` is a
search: it ranks janetcode's whole feature catalogue — every slash
command, key, command-line flag, tool, configuration key, and everything
the harness does on its own — against the words you typed, and then
quotes whatever the manual has to add. The manual is compiled into the
binary, so this works with no network and no model call.

```
/help the context is full
/help remember things across sessions
/help my build takes forever
```

The catalogue is generated from the tables the program runs on: the
command table, the flag set, the tool registry and the `Config` struct.
It cannot describe a janetcode that does not exist, and tests fail when a
command or a flag is added without a line saying when it helps.
`--inspect features` prints the whole thing; `--inspect "features <query>"`
searches it from a script.

The model has the same catalogue through an `introspect` tool, so it can
tell you the command for something instead of inventing one. That tool is
*deferred*: its definition is not in any request until the model asks for
it, so a session that never wonders about the harness pays nothing for it.

The model also carries a short section of the system prompt — measured at
312 estimated tokens, with a test that fails above 350 — listing what the
harness does on its own: checkpoints, the verification gate, memory,
compaction and `expand_output`, background jobs, permission rules,
sessions, `/goal`. It is there because both failures it prevents were
paid for on every session without it. The model used to redo the
harness's work (copying a file before editing it, re-reading a file to
recover output that was one `expand_output` away) and, asked what
janetcode could do about something, it used to invent a command that does
not exist. That section sits above the cache breakpoint, so on a provider
with prompt caching it is read from the cache after the first request.

### Why did it do that? `/why`

The harness intervenes. It summarises a conversation that got long, stops
a turn going in circles, sends the model back to fix a failing test, falls
back to another model when a quota runs out, kills a stream that stopped
producing, refuses a tool call, contradicts an answer that claimed more
than the turn showed. Each of those is surprising the first time it
happens, and each leaves a record in the session ledger.

`/why` reads that ledger and explains the most recent one in a paragraph:
what kind of intervention it was, what the harness said at the time, and
why it does that. `/why 2` is the one before, and so on back. There is no
model call behind it — asking the model why the harness did something is
asking the wrong party, and paying for the answer — so it costs nothing
and can be asked as often as it is useful. `/trajectory` still has the raw
records.

### The first session in a new directory

The first run in a directory janetcode has never seen is the one moment a
user is willing to be told about setup, and "you can configure a
verification command" is not worth their attention. So janetcode looks at
what the directory holds — `go.mod`, `package.json`, `pyproject.toml`,
`requirements.txt`, `Cargo.toml`, a `Makefile` with a `test` or `check`
target — and prints one line:

```
tip · Go project · /verify set "go test ./..." runs it after every change and hands
      failures back to the model · gopls is not on PATH (go install
      golang.org/x/tools/gopls@latest) for diagnostics after edits
```

It runs nothing and writes nothing: proposing a command is not the same as
running it, and a directory nobody has asked janetcode to manage does not
get a `.janetcode` written into it. A `package.json` with no test script,
or a Makefile with no check target, gets no command proposed rather than
one that will fail the first time it is run. A directory that already has
a `.janetcode`, or a resumed session, gets nothing.

### One-time hints

janetcode has features nobody finds. A user who has approved the same
command nine times does not know that pressing `r` would have stopped it
asking; a user watching a test suite hold a turn open for two minutes does
not know about `ctrl+g`. A manual does not help, because nobody reads one
at the moment they need it.

So a small rule engine watches what the session is already watching and
puts one faint line in the transcript at the moment the problem is in front
of you: the verification gate on the third changing turn (or the second time
you run the tests yourself with `!`), the `r` key on the third refusal of
the same shape of call, `ctrl+g` after a command holds a turn for twenty
seconds, `/context` and `/compact` at 60% of the window, `@path` after a
large paste or a stack trace, `/usage` at $0.50, the trust command for a
project file that is present but not enabled, the `prices` key for a model
nothing can price, and `/context` on a session resumed after a compaction.

Each fires at most once per project, ever. One at most per moment. Never
while a permission prompt or a picker is open. Never for a feature you
already use. `/hints off`, or `"hints": false` in the config file, silences
the lot. The rules are evaluated on events — a turn ending, a tool result, a
denial, a paste — never on a render and never on a keystroke, which is
tested.

`/stats` ends with a "not yet tried" line naming up to five catalogue
features you have never reached for. It comes from a count kept in the
config directory — feature name to number, written once when a session
ends — which never leaves the machine and which nothing reads but the
hint rules and that line.

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
| `features` | the feature catalogue; `features <query>` searches it and the manual |

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

The stub that replaces a long result names the archive id and carries a
short sha256 of the output before it was cut down. The id is unique per
call, so without the digest two runs of the same command that produced
the same megabyte leave stubs that look different; with it, "the same
output again" is visible without fetching either one back.

A preview is untrusted text standing where a whole result was, so the
harness's own tags — `<system-reminder>`, `<harness>` and the rest — are
escaped in it. A file, a log or a fetched page that contains one still
reads the same; it no longer parses as the harness speaking.

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

A long task the model is working through keeps a plan of its own, in the
`todo` tool rather than in prose. `/todos` shows it: what is done, what
is in progress and what is left, so a turn that has been running for
minutes can be read without interrupting it.

### Rating an answer

`/feedback up` and `/feedback down` mark the last assistant message, with
an optional note (`/feedback down the diff was backwards`). Nothing is
sent to the model and nothing changes about the session: the rating goes
into the session log, where a later review — or an eval comparing two
prompts — can find it. It is a record for you, not a correction for the
model; to correct the model, say so.

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
- any other exit code, a crash or a timeout (`timeout` in seconds on the
  hook, else `hooks.limits.timeout`) is logged and ignored: hooks fail
  open, so a broken hook cannot wedge a session.

Every hook runs on the critical path of a tool call, and what it prints
is pasted into the conversation, so what hooks may cost is bounded. The
bounds live in the same object, under `limits`:

```json
"hooks": {
  "post_tool": [{"command": "gofmt -l ."}],
  "limits": {"timeout": 10, "project_timeout": 60, "output": 65536, "per_event": 20}
}
```

| key | default | what it bounds |
| --- | --- | --- |
| `timeout` | 10 | seconds a hook that names no `timeout` of its own may run |
| `project_timeout` | 60 | the most a hook from the project file may ask for |
| `output` | 65536 | bytes kept of one hook's stdout and stderr; the rest is dropped and said to be |
| `per_event` | 20 | hooks run for one event; the rest are skipped and logged |

Zero, or the key left out, means the default. A negative figure is refused
on load (exit 52).

A project's `.janetcode/hooks.json` is held to less than the user's own
config, because trusting it is a decision about this checkout rather than
a decision to hand the repository every power the user has: a project hook
may not reach the network (`curl`, `wget`, `ssh`, `scp`, a bare URL), and
its timeout is capped at `project_timeout`. A `limits` object in the
project file is ignored and said to be — a repository that could raise its
own ceiling would not have one. Each hook dropped or trimmed is named,
with what to do about it — copy it into your own config, which is a
deliberate act. The user's own hooks are unrestricted.

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

Resource content is written by whoever runs the server, so it is framed
with a delimiter it cannot close: `<mcp-resource <nonce> uri=…>` with a
fresh random nonce per call, and a line after it saying the text inside is
data and any instruction in it is the server's, not the user's. A fixed
delimiter would be one the content could simply contain.

A tool call whose connection failed before a reply arrived is retried only
when the server marked the tool read-only or idempotent. Otherwise the
model is told that the call may or may not have taken effect and that it
was not repeated: a lost reply says nothing about whether the server
created the issue or sent the message.

### Custom commands

Drop a prompt template at `.janetcode/commands/<name>.md` in the project (or
`~/.config/janetcode/commands/<name>.md` for every project) and `/<name> args`
submits it with `$ARGUMENTS` replaced. The first line is the help text shown
in tab completion.
