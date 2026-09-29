# The interface

Part of the [janetcode manual](README.md); the [README](../../README.md) has the short version.

`janetcode --web` opens the session in a browser at
`http://127.0.0.1:8787` (or `--web-addr`). The page follows the hosted-chat
layout: a slim left rail (brand, **New session**, search, the session list
grouped by day) and one centered conversation column with a hero composer.
Janet's extra surfaces slide over the column as drawers: the tabbed session
panel (Plan, Changed files, Background jobs, Subagents, PTC, This session)
from the header's panel button, and a web preview for the running app.
Click a subagent, job or PTC run to open its transcript, log tail or
script in the page. Chat, Trajectory and Terminal switch in the header:
Chat is the conversation, Trajectory is the session event ledger (turns, tools,
compaction, the critic) with a timing strip and a record inspector, Terminal
is a shell in the session's directory.
The header also carries the model picker, the session mode (ask, PLAN, AUTO,
YOLO, EDITS, READ-ONLY) and the effort tier.
Allow-edits / auto / allow-all / plan-first / read-only live on the Session
tab. You write in the composer; when a tool wants to change something, the
Allow once / Always this session / Remember / Deny card floats over the
composer with a scrim. When the model
asks a labeled question, the options (and Other) are answered on the
page. Ctrl+K still searches slash commands. The server binds loopback
unless you tell it otherwise. Bound to any other address it requires an
access token: the URL it prints carries one (`/?token=…`), opening it
sets a cookie, and a page opened without it asks for the token. Set
`JANETCODE_WEB_TOKEN` to keep the same token across restarts; scripts can
send it as `Authorization: Bearer <token>`. The token is sent in the
clear unless you put TLS in front.

The transcript streams the model's markdown as it arrives, shows each tool
call on one line with what it did and how long it took, and renders every
edit as a diff with line numbers: three lines of context around each
change, "… N unchanged lines" where a gap was skipped, and for a
single-line replacement the changed tokens picked out in inverse video.
A ```mermaid block is drawn rather than printed (see below).
The status line shows the session mode (ask, PLAN, AUTO, YOLO, EDITS),
whether bash is sandboxed or the workspace is remote, what the agent is
doing (including what it is waiting on: a retry and its countdown, a
compaction, a subagent), the elapsed time, and a gauge of how full the
context window is. While the model is thinking — not running a tool, not
waiting on you — that wait is a seeded flavor line that rotates every
couple of seconds: Claude Code present-participles ("Moonwalking…",
"Pondering…") mixed with Qwen Code loading phrases ("Reticulating splines…",
"Don't panic…", "Constructing additional pylons…"); `--web` uses the same list.
Tool names and retry countdowns stay literal. Mode changes, toggles and
one-line acknowledgements (titled, forked, copied, sandbox, a loop armed)
land here, not in the transcript. The composer grows with
what you type (up to nine lines) and the transcript above it holds its
scroll. While a turn runs the round send button becomes the stop button;
Enter queues behind the running turn, Ctrl+Enter steers it. A
permission prompt floats as a card over the composer with a scrim, so the
conversation behind it stays readable but inert until you answer.

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
shift+tab        cycle ask → plan → auto → yolo (approvals, plan-first, safe-auto, auto-approve)
ctrl+r           cycle the reasoning tier up the ladder and round again (/effort)
ctrl+t           open the overlay of continuable subagents (/agents)
```

## Colour and themes

A theme is a full named palette: every colour role the interface draws
with (accent, dim, faint, warn, err, ok, user, tool, the header
foreground and background, the two border colours, and the two diff
colours), plus the transcript's left gutter. The `default` theme is calm
system colours with janetcode's iris accent (aqua for tools and code), and is what you get if you never pick
one; `classic` is the palette janetcode drew before v0.3.

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
| `catppuccin-mocha` | dark | pastel blues and mauves on cocoa |
| `catppuccin-latte` | light | the same pastels on warm paper |
| `dracula` | dark | cyan, pink and purple |
| `tokyo-night` | dark | storm-blue and neon at midnight |
| `one-dark` | dark | atom one dark chalk pastels |
| `rose-pine` | dark | dusty rose and pine at dusk |
| `everforest` | dark | moss and bark, low-glare greens |
| `kanagawa` | dark | ink, gold and wave-blue |
| `matrix` | dark | phosphor rain: terminal green on black |
| `monokai` | dark | hot pink, lime and tangerine |
| `github-light` | light | github's blue links on white paper |

Every builtin clears a contrast ratio of about 3.5:1 for body text against
the background it assumes (borders may sit lower); a test holds each one to
that bar, so a shipped theme is always legible.

### Switching themes: `/theme`

`/theme` alone opens a searchable list of every theme with its
description. Moving the highlight restyles the whole interface as a
preview (colour chips under the list show the roles); enter keeps the
highlighted theme — the transcript recolours in place, no restart — and
saves the choice to the config file; esc puts back whatever was active
when the picker opened. `/theme <name>` switches directly, `/theme list`
prints them, and `--theme <name>` sets one for a single run without
touching the config.
Switching never re-asks the terminal for its background, so no keystroke is
lost; selecting a single-mode theme (a dark-only one, say) pins the light
or dark assumption to that theme.

### Room to breathe: the gutter

The transcript, the header, the status line and the input box share one
left margin — the theme's gutter, two columns by default — so the
top-level lines (the `✓` tool, `·` thought, `❯` prompt) step in from the
edge and the body indent under them steps in consistently from there. On a
terminal narrower than 88 columns the gutter collapses to zero, so diffs,
code and tables keep every column they can get, and it stays collapsed
until the terminal is a few columns wider than that (96), so resizing
around the threshold does not slam the margin on and off. A dense theme
can set its gutter to `0` to switch the margin off at every width. The
vertical rhythm — one blank line between blocks — is unchanged; this is
horizontal only.

### Your own status line: `status_line`

A command of yours can print a line or two under the input: the branch,
the spend, a clock, whatever a script can work out. It goes in the config
file (never a project's: it runs on every turn):

```json
"status_line": {
  "command": "input=$(cat); echo \"$(echo \"$input\" | jq -r .model.display_name) · $(echo \"$input\" | jq -r '.git.branch // empty')\"",
  "refresh_interval": 10
}
```

The command gets the session as JSON on stdin, in the shape Claude Code
and Qwen Code send theirs, so a script written for either works:

| field | what it is |
| --- | --- |
| `session_id`, `transcript_path` | the session and its log |
| `cwd`, `workspace.current_dir`, `workspace.project_dir` | where janetcode runs |
| `version` | janetcode's version |
| `permission_mode` | ask, auto, auto_edits, plan, yolo or read_only |
| `model.id`, `model.display_name` | the model |
| `git.branch` | the branch; `git` is absent outside a repository |
| `context_window.current_usage` | tokens the conversation takes now |
| `context_window.context_window_size`, `used_percentage`, `remaining_percentage` | only when the model's window is known |
| `context_window.total_input_tokens`, `total_output_tokens` | the session's totals |
| `cost.total_cost_usd` | only when the session's cost is known |

It runs again, checked twice a second, when any of that changes (the
conversation grows, a turn starts or ends, the mode or the model or the
branch changes), at most one run at a time, and every `refresh_interval` seconds as well if you set
one. Up to two lines of its output are shown, cut to the width. Its own
colours are kept; any other escape sequence is dropped, so it cannot move
the cursor or retitle the terminal. A command that fails or takes longer
than five seconds leaves the line blank and says why once.

The same object is read under `"statusLine"`, with `type: "command"`,
`refreshInterval` and `padding` (ignored), so one can be pasted from a
Claude Code or Qwen Code settings file. Qwen Code's preset status lines
(`type: "preset"`) are refused when the config loads. The status line is
drawn by the terminal interface; `--web` does not show it.

### Mermaid diagrams are drawn, not printed

When the model answers with a ```mermaid block, janetcode draws it with
box characters at the width of the transcript instead of showing the
source. Three kinds are drawn:

- `flowchart` / `graph`, top-down (`TD`, `TB`) and left-right (`LR`),
  with the usual shapes (`[ ]`, `( )`, `{ }`, `(( ))`), chains
  (`A --> B --> C`) and both spellings of an edge label (`A -->|yes| B`
  and `A -- yes --> B`). A cycle is fine: the edge that closes it is
  drawn with its arrow pointing back the way it came.
- `sequenceDiagram` with `participant`/`actor` lines and plain messages
  (`->>`, `-->>`, `->`, `-->`), drawn as lanes with a lifeline each; a
  dashed reply stays dashed.
- `stateDiagram` / `stateDiagram-v2`, drawn as a flowchart whose `[*]`
  ends become a **start** and an **end** box.

Everything else keeps today's behaviour and is shown as source: any other
kind (class, gantt, pie, ER, journey…), and anything inside a supported
kind that would have to be ignored to draw it — a `subgraph`, a `note`, a
`loop`/`alt`, a composite `state X { … }`, `classDef` and friends,
`BT`/`RL` directions, a self-loop, an `%%{init}%%` directive or YAML front
matter. Malformed source is shown as source too. A diagram is never drawn
from a partial understanding of it: the rule is that a wrong picture is
worse than the text.

It also falls back when it cannot be drawn honestly at this size: a
terminal under 40 columns, a diagram that would be wider than the
transcript or taller than 120 lines, and anything past the caps (40
nodes, 80 edges). On a terminal that reports only sixteen colours the
source is shown as well. An edge label that has no room is shortened, and
dropped when there is no room at all — the diagram is never widened past
the terminal to fit a label.

The source is always one keypress away: a message that drew a diagram is
expandable, so **ctrl+o** (or clicking the line, or `/verbose` for the
whole transcript) swaps every diagram in the last turn for the mermaid
source it was drawn from, and ctrl+o again draws them back. A faint line
under each diagram says so.

`--web` draws the same subset. The page posts the fence to the server,
which uses this package and returns the box drawing; anything the
terminal would leave as source stays a code block. No diagram library
is loaded in the browser.

On the web, two or more tools between a prompt and the answer fold into
one row (`Made 2 edits · Ran 1 command`). Click it to open the calls.
A turn that changed files shows a card with the line counts. A long
answer clips behind Continue. Enter while a turn is running queues the
next prompt; Ctrl+Enter steers the one in flight. `/` in the composer
opens the command menu, `@` mentions a file, and `+` attaches one.
The header shows the git branch and the session's `+N −M`. Settings
(the gear in the rail) sets Moon bright, Moon dark or System, the font
size, and a sound when a turn finishes in a background tab.

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
`r` save the rule shown on the key line, `←`/`→` (or `shift+tab`/`tab`)
widen and narrow that rule before saving it,
`n` deny and type why (the reason goes to the model, enter skips it), `esc`
deny silently, `c` cancel the whole turn. Destructive shell commands (recursive deletes, force pushes,
sudo, piping downloads into a shell) are flagged in the prompt.

An `edit_file` or `write_file` call to a file that already exists shows a
second line: the reach of the change, such as
`changes Parse (func) · used 14 times in 6 files · tests: parse_test.go, cli_test.go · checks: go test ./...`
(see [The reach of an edit](#the-reach-of-an-edit)).

Typing while the model is working queues the message; it is sent as soon as
the turn ends. `ctrl+s` instead hands what you typed to the turn that is
running — the model reads it before its next step. The prompt names both
while a turn is running (`enter queues · ctrl+s steers`), and the reserved
row above it says what is queued and how to steer it instead. `↑` with an
empty prompt takes the last queued message back to edit, and cancelling a
turn hands all queued text back to the input, joined by blank lines.

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
paste that ends in a newline can never send itself. A brief notice above
the input confirms the paste for a few seconds, then that row goes blank
again; it does not become a line in the transcript.

```
@path            attach a file, an image (png/jpg/gif/webp), or a directory listing; tab completes
ctrl+g           move the command running in the foreground to the background, as a job
ctrl+v           attach the image in the clipboard (wl-paste or xclip on Linux, pngpaste on macOS)
!command         run a shell command here; its output is shown and shared with the model
/help      /quit      /clear (new session)     /yolo (toggle auto-approve)   /auto (edits and known-safe reads)   /edits (edits go through, commands ask)
/plan <request> (plan mode: explore, write a plan to .janetcode/plan.md)
/plan review | comment <line> <text> | changes | approve  (read the plan, annotate a line, send the comments back, execute)
/accept <request> (write acceptance tests first; /accept approve locks them as a gate)
/readonly  (toggle read-only: the model can inspect but change nothing)
/sandbox [on|off|workspace|read-only|strict|backend|image]  (run local bash under a sandbox profile; file tools stay here)
/review [focus]  (two subagents read the current diff: bugs and tests)
/loop [duration] [prompt|off]  (repeat a prompt this session; restored on resume)
/agents [id|new <brief>|fork <brief>]  (overlay of subagents; new starts a general one, fork inherits this conversation)
/add-dir <path>  (grant file tools another local directory)
/btw <question>  (side question; not stored as a user turn)
/copy            (copy the last assistant reply)
/config [key=value]  (set a catalogued config key)
/doctor          (checkup; reports facts, changes nothing)
/cd [path]       (move this session's working directory; no argument says where it is)
/ssh [user@host[:/path]|off]  (file tools and bash on a remote host; off returns here)
/docker [container[:/path]|off]  (the same, in a running container)
/connect   /model [cheap|strong|advisor|vision [name|off]]  /compact (summarise the context to free the window)
/features [name [on|off]]  (the model slots, the cascade, the forced advisor, the stale-check nudge and the decision consumers)
/jev [on|off|model|provider|triage|intake|route|outcome [on|off]]  (the optional decision service)
/compact-fast (also /compress-fast: prune old results and thinking, no summary call)
/followup [on|off]  (ghost-text next prompt after a turn; tab accepts; off by default)
/curator [list | pin <name> | archive <name> | restore <name>]  (auto-generated skills)
/cua [on|off|doctor]  (start Cua Driver as an MCP desktop server)
/effort [tier|up|down]  (how hard the model thinks: low, medium, high, xhigh, max)
/usage     /stats (turns, model calls, tool calls per tool, tokens, cost, what the harness had to recover)
/perf      (where the wall clock went: the model, the tools, waiting on you, the harness itself)
/resume (searchable list of earlier sessions; /resume <n or id> reopens one directly)   /verbose   /settle (list it last)
/undo [preview | turn <n> | about <description> | apply]  /redo  /find X  /cost  /mcp [trust | auth <server> | logout <server>]
/budget [usd | off] (stop the session once it has spent that much; alone reports it)
/fork [stay] [title]  /title <text>  /history <words> (search every saved session)
/open path[:line] (view a file inline)   /diff [path | revert path:n] (hunk review)
/context   (what fills the window)   /pin <note or @file>   /unpin <n>
/export [file.md] (write the conversation as a Markdown transcript)
/rules  (path-scoped project rules: what brings each in, and which have been sent this session; see Configuration)
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

A session has more than one model slot, and `/model` fills any of them.
`/model` alone, or `/model <name>`, is the session's own model as it has
always been; `/model cheap`, `/model strong`, `/model advisor` and
`/model vision` open the very same list for the slot named, and
`/model <slot> off` gives a slot back. An unset slot reads
`cheap: unset (main: anthropic/claude-opus-5)`, because an empty slot is
the session's model and not a hole. What each slot is for, and which
feature has to be on before filling one changes anything, is in
[Configuration](configuration.md) under "The model slots".

`/features` is that second half: the cascade (`escalate`), the forced
advisor (`advisor`), the stale-check nudge (`stale-nudge`) and the four
decision consumers, each with what it is set to and what still stands in
its way — a filled strong slot with `escalate` off says so rather than
looking like a working cascade. `/features <name> on|off` changes one and
saves it to the config file, and it takes effect in the running session.

`/jev` is the decision service (see "Decisions" in Configuration): alone
it reports whether it is on, which endpoint and pinned model it asks,
which consumers are on and what the calls have cost this session.
`/jev on` and `/jev off` are the master switch, `/jev model` and
`/jev provider` open a list, and `/jev triage|intake|route|outcome on|off`
toggles one consumer. All of it is written to the config file and applied
to the running session — the client is rebuilt from the config on every
change, so nothing here waits for a restart. `JANETCODE_OFF=decisions`
still wins over every one of them, and `/jev` says so when it is set.

`/effort` sets how hard the model thinks, without changing models:
`low`, `medium`, `high`, `xhigh`, `max`, in that order, or `default` for
whatever the provider does on its own. Alone it reports the tier the
session is asking for, marks it on the ladder and says whether the
provider behind it takes one at all; `/effort up` and `/effort down` step
a rung, and `ctrl+r` cycles up and round. The tier rides on every request
from the next one onward, so it can be raised for one hard step and
lowered again afterwards, in the middle of a session and even while a
turn is running. It changes this session only — `/config effort=high`
is the spelling that also writes it to the config file for the next one.
When a model refuses the tier asked for, the one below it is used instead
and remembered, so the refusal is paid for once; asking for another tier
afterwards clears that and tries the new one honestly. Only Anthropic
models take a reasoning tier today: with another provider the setting is
remembered, `/effort` says it is not being sent, and nothing else changes.
A session above the default shows its tier next to the mode on the status
line, so `max` is never a surprise at the end of the month.

Safety rules that apply in every mode, including `--yolo`: commands that
would destroy the machine (deleting `/` or the home directory, formatting,
writing raw devices, piping a download into a shell, fork bombs, power-off)
are refused outright, seen through `sudo`, `env`, `timeout`, `sh -c` and
`eval` wrappers; commands that change files outside the working directory
(`rm ../x`, `cp a ~/b`, `> /etc/hosts`, `go build -o ~/bin/x`, `curl -o ../f`)
always ask. Everything else follows the permission mode.

Auto-approve means "do not stop me for what would be allowed", never "skip
the check". A guarded command is put in front of you under `--yolo`, an
allow rule and a `pre_tool` hook alike, and the only thing that lets it
through is an answer. A run with nobody to answer -- an eval, a headless
run with no approver -- refuses it and tells the model why, so the next
attempt changes the command rather than the machine.

Whatever a tool reads, credentials are taken out of its result before
anything sees it: a shell's whole environment, a `.env`, a 401 body
quoting the header it rejected. Vendor key shapes, a bare `sk-` key, a
value assigned to a variable whose name says key, token, secret or
password, and `Authorization: Bearer` / `x-api-key` headers become
`[redacted]`. The variable names and the surrounding lines stay, so the
output is still readable, and the same redaction covers the model's
context, what the interface shows, the session log on disk and an eval's
recorded run. `"off": ["scrub"]` turns it off for a session that needs
raw output.

Model-run commands also never inherit the credentials themselves. A
variable whose name contains `API_KEY`, `AUTH_TOKEN`, `API_TOKEN`,
`SECRET_KEY` or `ACCESS_KEY` as whole `_`-separated words is dropped from
the environment they run in, wherever in the name the phrase sits, so
`DEEPSEEK_API_KEY_PERSONAL` goes the same way as `DEEPSEEK_API_KEY`.
`GITHUB_TOKEN`, `NPM_TOKEN`, `CARGO_REGISTRY_TOKEN` and
`AWS_ACCESS_KEY_ID` are kept, because `gh`, `npm publish`, `cargo` and
`aws` need them; an MCP server's configured env is expanded after the
scrub and is unaffected.

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

#### Going back to before a turn: /undo turn n

Repeating `/undo` walks back a turn at a time. `/undo turn 4` goes
straight there: it puts the whole work tree back to the state turn 4
started from **and rewinds the conversation to that point**, out of the
same per-turn checkpoints `/undo` uses — no second checkpoint system, no
shadow repository. `/changes` marks the turns you can go back to
(`turn 4 [restorable]`), and tab completion offers them.

The conversation goes back with the files because the alternative is
worse: a model whose context still holds the turns that wrote the code
keeps describing that work as done, offers to build on files whose
contents it can still quote, and has to be argued out of it. That
confusion is the thing `/undo turn` exists to remove. `/undo turn 4
--files-only` restores the files and leaves the conversation as it is,
if that is what you want.

It is a preview, not an action. `/undo turn 4` lists every file it would
restore, recreate or remove, and says plainly what leaves the context —
`and rewind this conversation to that point, dropping 3 turns, 27
messages from the model's context` — and changes nothing; the same
`/undo apply` that carries out an `/undo about` selection carries this
out too, so there is one confirmation to learn. It refuses, with nothing
touched, when:

- there is no snapshot for that turn — the turn changed nothing, or it is
  older than the last 30 turns that did, so trimming dropped it. The
  refusal names the turns that can still be restored.
- the repository moved underneath it (a commit, checkout or reset since
  the snapshot was taken). The work tree recorded then cannot be put back
  without mixing two states, so it is refused outright rather than
  half-applied from the file copies.
- the turns moved between the preview and the apply (a turn ran, an
  `/undo` popped one): what was listed is no longer what would happen, so
  run `/undo turn n` again.

What it restores is recorded as changes of its own, exactly as `/undo n`
records a revert, so the restore appears in `/changes` and `/blame` and
`/undo <n>` can take it back. Outside a git repository there is no
work-tree snapshot, so the restore falls back to the copies the file
tools made — the same fallback `/undo` uses, covering file-tool changes
only.

A rewind drops, from turn n on: every message of those turns (prompts,
replies, tool calls and their results), the undo checkpoints of those
turns, and the archived tool outputs they produced, since the messages
naming their `ctx-…` ids are gone. It keeps everything from before turn
n untouched, the session file itself (see
[Sessions](sessions.md#rewinding-a-session), which is append-only and
keeps every dropped turn), your pins, the `/changes` record, and the
cumulative cost counters — the tokens were spent whether or not the work
survived. One harness note stands where the dropped turns were, saying
what went back and that the model is no longer the author of it; with
`--files-only` you get the ordinary `/undo apply` note instead.

The rewind is refused on its own, leaving the files to go back by
themselves, when the conversation has been compacted since turn n began:
the summary replaced the messages that marked where the turn started, so
that point can no longer be located and cutting at a guess would take
the wrong messages. The preview says so in as many words.

### Permission rules

`/permissions` (in the terminal or the browser) starts with a plain
summary of what all of this adds up to right now: what runs without
asking, what asks first, what never runs, and whether commands run in a
sandbox. It follows the mode, the rules, what you allowed for this
session and the protected paths, so it is the one place to check before
leaving a session to work:

```
ask: changing files, running commands and network access ask first

  runs         reading and searching
  asks first   file edits
               shell commands
               web fetches and searches
               MCP tools that change things
               protected paths (dotfiles, git hooks, keys), in every mode
  never runs   commands that would wreck the machine
  where        commands run on this machine, as you (no sandbox)
```

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

That rule is one rung of a short ladder, and `←` and `→` move along it
before `r` saves anything — narrower to the left, wider to the right, with
`2/3` on the key line saying where you stand. For `go test ./internal/...`
the rungs are `bash(go test ./internal/...)`, `bash(go test *)` and
`bash(go *)`; for `internal/agent/x.go` they are the file itself,
`edit_file(internal/agent/*)` and `edit_file(internal/*)`; for a URL, the
page, its section of the site, then the whole host. The prompt starts on
the rung it has always proposed, so pressing `r` straight away saves
exactly what it used to, and a call with only one rung shows no ladder at
all. The ends are a floor and a ceiling: the widest rung is never broader
than the tool's leading word, so `bash(*)` is not reachable, the tree rung
never climbs to the filesystem root, and a call `r` refuses to capture (see
below) has no ladder either. The ledger counts the rung you actually
saved, and the "approved N×" note beside it counts that same rule.

A rule is `tool`, `tool(pattern)` or `tool(param:pattern, ...)`; `*`
spans anything and `?` one character. A bare pattern is matched against
the call's subject: the command for `bash`, the path for file tools and
the URL for `fetch_url`. A compound bash line (`&&`, `||`, `;`, `|`,
`&`, newlines) is split into its commands: a deny rule refuses the line
when it matches any one of them, and an allow rule allows it only when
every command is covered by some allow rule or is a known-safe read (the
list AUTO mode trusts), so `bash(go test *)` allows `go test ./... 2>&1 |
tail` but asks about `go test ./... && rm -rf x`. A rule that spells out
a whole compound line with no wildcard still matches that exact line.
A command or process substitution (`$( )`, backticks, `<( )`, `>( )`)
runs a command line of its own, so it is judged the same way: a deny
rule matching any command inside it refuses the line, and an allow
rule's `*` covers `go test $(ls -d ./internal/*/)` but asks about
`go test $(curl ...)`, because the command inside must itself be
allowed by a rule or be a safe read. A here-document's body is the
command's input, not commands, so `bash(git commit *)` covers a commit
whose message comes from `<<'EOF'`; an unquoted one whose body holds a
`$( )` is judged line by line. `r` offers no rule for a compound command (`&&`, `;`, `|`, `$( )`) or one led
by `cd`, `sudo`, `env`, `sh -c` and similar wrappers. A leading
`cd <project> &&`, which changes nothing, is ignored when matching and
suggesting, so `go test` rules cover a model that prefixes every command
that way. Deny wins over allow, whichever order the two were written in and
whichever is more specific; a denied call is refused without a prompt and
the model is told which rule did it. Below both sits a tier no allow rule
reaches: see [Protected paths](#protected-paths).
Guarded commands (ones that reach outside the workspace) still ask.
A parameter clause names a field of the call instead of the subject, for
the things the subject cannot say:

```
task(model:opus)                   the task tool, but only on opus
task(agent:explore, model:haiku*)  both clauses must match
fetch_url(domain:*.example.com)    the host, not the whole URL
edit_file(path:internal/**)        the path field, cleaned, with / as the separator
```

Clauses are separated by commas and every one of them must match; a name
may be a dotted path into a nested object (`opts.model:opus`). The value
is matched with the same wildcards as any other pattern, against the
field as it reads: a number or a boolean as it is written, a list when
any of its elements matches, and nothing at all when the field is absent,
so `task(model:opus)` never matches a call that named no model.
`fetch_url(domain:…)` is the one field computed rather than read — the
host of the URL, with and without its port, because a host is what people
mean when they trust a site. A pattern with no `name:` prefix keeps its
old meaning exactly, so `bash(npm run *)`, `bash(git log --format=%h,%s)`
and `fetch_url(https://pkg.go.dev/*)` are all still subject patterns, and
no rule written before parameters existed changes behaviour. A malformed
clause (`task(model:)`, `task(agent:explore, model)`, the same name
twice) is refused when the rule is parsed rather than quietly matching
nothing.

The `r` key never proposes a parameter rule: the ladder is about breadth
— this path, its directory, the tree above it — and a field is not a
wider or narrower version of anything. Parameter rules are written by
hand, in the config file or with `/permissions allow <rule>`. The one
exception is the network, below.

#### Typed scopes

A handful of clause names are not fields but *scopes*: something the
call does, worked out from the call whatever tool it is, so one rule can
say what you mean without knowing where each tool keeps it.

```
bash(cmd:go test *)                      the command's words, one by one
*(read:docs/**)                          the paths a call reads
*(write:internal/**)                     the paths a call writes, bash included
fetch_url(host:*.go.dev)                 the hosts a call contacts
bash(cmd:curl *, host:api.github.com)    both: curl, and only to that host
*(touches:**/.env)                       any path a call names, read or written
*(arg:*BEGIN*PRIVATE KEY*)               any string anywhere in the input
*(secret:*)                              the input holds a credential
```

`cmd:` matches the command's argv word by word after the shell's quoting
is undone, so `go test './...'` is `go test ./...`, and a trailing `*` is
"any further words". As an allow rule it covers one command and nothing
it is chained with, never looks through a wrapper (`sudo go test` is not
`go test`), and refuses a command that writes outside the workspace:
`bash(cmd:go test *)` allows `go test ./... 2>&1` and asks about
`go test ./... && curl …`, `cd /etc && go test` and
`go test > ~/.profile`. As a deny or ask rule it catches the command
anywhere on the line — behind `sudo`, `env`, `timeout`, `xargs`, a path
(`/bin/rm`), inside `$( )`, `sh -c '…'`, or a here-document fed to
`bash`, `ssh` or `python`.

`host:` reads the host of a `fetch_url` URL, the hosts a bash command
names (every URL with a scheme, `git clone git@host:…`, the host of
`ssh`, `scp`, `rsync`, `curl`, `wget`, `nc` and the like), the search
provider behind `web_search`, and the server behind a remote MCP
server's tools. `*.example.com` also covers `example.com`; a pattern
without a port matches any port. A command that reaches the network
through something it does not show — `git push` to a configured remote,
`curl $URL` — has no host, and a host-scoped allow rule does not cover
it.

`read:`, `write:` and `touches:` compare paths in every spelling they
have — as written, relative to the workspace or absolute in it, and with
symlinks resolved. A leading `**/` also matches at the top, so
`**/.env` matches `.env`, and `dir/**` names `dir` itself too.

A scope with several values reads differently depending on the rule, and
it is on purpose: they fail in opposite directions. An **allow** rule
covers a call only when *every* value is in scope and there is at least
one — `*(write:docs/**)` does not cover `echo x > docs/a > main.go`, and a
path written inside `docs/` that resolves through a symlink to somewhere
else, or climbs out with `..`, is not in `docs/`. A **deny** or **ask**
rule catches a call when *any* value is in scope — `*(touches:**/.env)`
refuses `cat README.md .env`.

A tool whose own input has a field called `cmd` or `host` keeps that
meaning: the scopes apply where they can be worked out (`cmd:` for bash,
`host:` for bash, `fetch_url`, `web_search` and remote MCP tools).

For a network command, `r` offers the host and never more: approving
`curl -s https://api.github.com/repos/x` offers
`bash(cmd:curl *, host:api.github.com)` instead of `bash(curl *)`, which
would allow the same command against any host. A command that names two
hosts offers only itself.

#### Ask rules: the sensitive calls

The third list is `ask`. A call an ask rule matches is put to you in
every mode — `/edits`, `/auto` and `--yolo` included — even when an allow
rule covers it, even when the tool only reads, and the answer covers that
one call (`a` and `r` do not widen it). It is the list for what should
never happen unwatched: reading `.env`, sending a credential anywhere,
touching production.

```json
"permissions": {
  "allow": ["bash(cmd:go test *)", "fetch_url(host:*.go.dev)"],
  "deny":  ["bash(cmd:git push *)", "*(host:*.internal.example)"],
  "ask":   ["*(touches:**/.env)", "*(secret:*)", "bash(cmd:kubectl * --context=prod*)"]
}
```

The order of authority is: the hard-deny tier (commands that wreck the
machine), then deny rules, then ask rules, then allow rules. When a rule
that won outranked an allow rule that would have covered the call, the
refusal or the prompt says so — "Denied by permission rule
bash(cmd:rm *) (it outranks allow rule bash(*))", "asks because of rule
*(touches:**/.env) (it outranks allow rule read_file)" — and
`/permissions check <command>` (or `/permissions check read_file
{"path":".env"}`) answers the same question before anything runs.

A subagent inherits every deny and ask rule of the session that started
it, and none of its allow rules.

`/permissions` lists rules, `/permissions allow <rule>`, `/permissions
deny <rule>` and `/permissions ask <rule>` add one. A project can ship
`.janetcode/permissions.json` with the same shape: its deny and ask rules
always apply, since they only ever take something away; its allow rules
only after `/permissions trust`. Headless runs take `--allow`, `--deny`
and `--ask` with comma-separated rules (a comma inside a rule's
parentheses stays in the rule), and a `pre_tool` hook can answer
`{"decision":"allow"}` to approve a call itself. A here-document's body
is the command's input rather than commands for deny and ask rules as
well: `bash(rm *)` does not refuse a commit whose message mentions
`rm -rf`.

janetcode also remembers, per project and across sessions, how you
answered every permission prompt — a ledger under `"permissions"` in the
config directory, keyed the same way memory and hints are, never in the
project itself. It never saves a rule on its own: once a call's shape has
been approved three times running with no refusal, the prompt itself
notes it ("approved 4× before, never refused" next to the rule the `r`
key would save), a one-time hint may mention it, and `/permissions` lists
it under "approved often, not saved" with the `/permissions allow <rule>`
that would save it. Saving still takes the `r` key or that command.

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
  see `memory recalled: …`. Word overlap decides *whether* an entry is
  relevant; among the relevant ones, ranking follows use — how often and
  how recently an entry has been recalled (recalling an entry strengthens
  it), with the entries you wrote or corrected by hand favoured over
  automatic ones. `EVAL_OFF=memoryrank` (or `JANETCODE_OFF`) restores the
  flat word-overlap order. Recalled entries count as used; past 200
  entries per scope the least used, oldest *automatic* ones are trimmed —
  entries you wrote with `/remember` are never evicted by the cap.
- **The model has a `memory` tool**: `recall` by query, `remember` when
  you tell it something worth keeping (it picks the type; the entry is
  kept for this project), `forget`.
- **Automatic extraction.** After a turn that changed something, or one
  where you gave feedback ("never…", "always…", "prefer…"), a cheap extra
  call asks what the conversation *established*, as typed JSON entries
  that may also replace an entry that became wrong. Duplicates and
  near-duplicates merge instead of piling up. It records a preference only
  when you stated it as lasting, not an instruction for the task at hand. `"memory": false` turns it
  off; `/remember` and the tool still work.
- **A credential is never remembered.** An entry whose text holds
  something API-key-shaped — a vendor key, a private-key block, a JWT, a
  long opaque value assigned to something called a secret — is refused
  with a note saying why: memory is written to disk and read back into
  every later prompt, which is the last place a key should be. Where the
  value lives is worth remembering; the value is not.
- **Only you write global memory.** Automatic extraction and the model's
  `memory` tool write project-scoped entries only; a rule for every project
  takes an explicit `/remember -g`. When the model is asked to remember
  something for all projects, it keeps it for this one and tells you to
  use `/remember -g`. Phrase a global entry as a preference about how you
  work ("I prefer small commits"), not as one project's rule.
- **Global memory stays in its ecosystem.** A global entry that names a
  language or toolchain — `pytest`, `cargo`, `npm`, `gofmt`, `go test` —
  is tagged with it (`ecosystem:` in its frontmatter, and inferred from
  its text for entries written before tagging), and is neither listed in
  the prompt's index nor recalled in a project of another ecosystem: a
  Python project's rule does not follow you into a Go project. A global
  entry that names no ecosystem and was not written by you (one extracted
  automatically by an earlier version) is left out of the index and
  recalled only on a strong match — two words of its name or description,
  not one. `/memory` still lists every entry.
- **A correction closes, it does not overwrite.** When extraction or a
  consolidation marks an entry as replaced, the old entry is kept with
  `valid_until` and `superseded_by` in its frontmatter: it leaves the
  prompt index and recall, stays in `/memory` marked with what replaced
  it, and stays on disk. Nothing learned is silently deleted.
- **Entries say where they were learned and what they are about.**
  `evidence:` names the session and event an entry came from (a watermark
  for prose extraction, exact record lines for `/sleep` — which rejects
  any entry citing a line the record does not have). `concerns:` names
  the files or symbols an entry is about, and a turn that names them
  recalls the entry without any word of its text overlapping.
- `/sleep` records a gate that failed and later passed as one line of the
  record, and lists existing entries about the files the session changed
  under their own heading, so a correction says what it replaces.
- `/memory` lists entries, `/memory <words>` recalls, `/memory show <name>`
  prints one, `/memory about <path>` lists the entries whose concerns
  name that file, `/memory history <name>` follows what replaced what,
  `/memory current` lists what is still in force, `/remember [-g] <fact>` adds (`-g` global), `/forget
  <name|number>` removes. Files are plain markdown you can edit by hand.

Recall matches words by default. With `"embedding_model": "text-embedding-3-small"`
(or any model your provider serves at `/v1/embeddings`) it also ranks
entries by meaning, so "verify the build works" finds a note about running
`make test`. Vectors are cached next to the entries and recomputed only
when an entry changes; if the endpoint fails, recall silently falls back to
words and retries a few minutes later.

`/init` writes the project's starter instruction file (`JANETCODE.md`)
from what the repository shows — see
[Project instructions](configuration.md#project-instructions). It never
overwrites one that is already there; `/init force` does.

### Skills

A skill is a markdown file with instructions for one kind of task
(`.janetcode/skills/<name>/SKILL.md` or `.janetcode/skills/<name>.md` in
the project, `.agents/skills` the same way, `skills/` under the config
directory for all projects; `.claude/skills` is read too). A project
skill shadows a `.agents` skill of the same name, which shadows a global
one. A directory skill can ship scripts and
reference files next to `SKILL.md`; they are listed with absolute paths
when the skill is attached so the model can read or run them. Optional
frontmatter gives `name`, `description`, comma-separated `keywords` (or
`tags`), `paths` (globs; the skill is attached when a tool touches a
matching file, the same way project rules work), `disable-model-invocation: true` (only you can invoke it) and
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
`/<name> args` (`$ARGUMENTS` in the body is replaced). Skills are re-read
from disk every turn, so editing one takes effect immediately.

`/skills` prices what is installed rather than only naming it: per skill,
what attaching it costs in tokens (the same estimator `/context` uses),
what its one line in the system prompt's skill list costs in *every*
request, whether it is in the context right now, and how often it has
been sent this session — matched by the injector, brought in by a `paths`
glob, or read with the `skill` tool. The heaviest come first, and the
last line totals the index lines against the request prefix, which is
what a directory of a hundred unused skills actually costs you.

Every skill's files are scanned, never run, when it is loaded: SKILL.md,
its scripts and references, hidden files included. A skill that came
with the repository (`.janetcode/skills`, `.agents/skills`,
`.claude/skills`) and holds a critical finding is **blocked**: it is not
attached, not offered to the model and not runnable as `/name`, and
`/skills` and `--doctor` say which file and line blocked it. Critical is
a pipe from a bare IP address into a shell, decoded code evaluated, a
reverse shell, deleting `/` or `~`, a fork bomb, the cloud metadata
address, the environment or SSH keys sent over the network, a key added
to `authorized_keys`, a symlink out of the skill, and in markdown an
instruction to ignore earlier instructions or to hide something from you.
Everything else the scan notices is a warning: an installer piped from a
named host (the way uv and ollama install; bash still asks before it
runs), a command shape quoted in a reference document, a compiled binary
or archive shipped with the skill, a credential. Your own skills under
the config directory are never blocked, only warned about. To use a
blocked skill, read it and remove what was found, or remove the skill.

`/curator` manages auto-generated skills (`auto-skill-*`, or frontmatter
`source: auto-skill` / `learned`). `/curator` lists them with how often
they have fired; `pin` keeps one, `archive` moves it to
`.janetcode/archived-skills/`, `restore` puts it back. Usage is stored in
`.janetcode/skill-usage.json`. Hand-written skills are left alone.

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

### Protected paths

The same carve-out covers the files that are not janetcode's but are not
work either — the ones whose effect outlives the session:

- shell startup files and profiles (`~/.zshenv`, `~/.bashrc`,
  `~/.profile`, `~/.config/fish/**`, `/etc/profile`, `/etc/profile.d/**`)
- `.git/config` and `.git/hooks/**` in any repository, `~/.gitconfig`
- publishing and package credentials (`.npmrc`, `.pypirc`, `.netrc`,
  `~/.docker/config.json`, `~/.cargo/credentials`, `~/.config/gh/hosts.yml`)
- keys and cloud credentials (`**/.ssh/**`, `**/.aws/credentials`,
  `~/.gnupg/**`, `~/.kube/config`)
- `.janetcode/permissions.json`, the trust store, and janetcode's own
  binary when it sits inside the workspace
- what runs at login or on a timer (LaunchAgents, user systemd units,
  `/etc/cron.d/**`, autostart)

Writing one of these always asks — under `/edits`, `/auto`, a saved allow
rule and `--yolo` alike — and the prompt says why on the line it already
uses for warnings: `protected path: ~/.zshenv`. The web UI shows the same
text, because it is the same field. Through the shell it is what the
command would *write* that decides, not how it is spelled: a `>` or `>>`
redirect, `cp`, `mv`, `tee`, `sed -i` and the rest, through `sudo`,
`env`, `timeout`, `sh -c` and `eval`. Reading any of them is unaffected.

`--yolo` asking is deliberate. Yolo means the session does not stop for
its own work, and this set is outside anyone's work: a session doing what
it was asked never meets it, and the one that does is the one you would
want to see. Nothing else about `--yolo` changes. The set is data —
`protected_paths` in the config file is appended to it (see
[Configuration](configuration.md#the-protected_paths-key)) and can only
add.

Below the rules entirely sits the hard deny: a command that destroys the
machine (`rm -rf /`, `mkfs`, `dd of=/dev/…`, a download piped into a
shell, a fork bomb) is refused before the rules are consulted at all. An
allow rule, `--yolo` and a `pre_tool` hook all pass it by without being
asked, and the model is told it was refused and not to retry it in
another form.

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
lists jobs, shows output, waits with a timeout, kills one, or writes a
line to a running job's stdin (`job` action `stdin`). Listing, output
and waiting never prompt; `stdin` and `kill` are asked about like any
command, since approving a background shell once does not approve what
the model later types into it. `/jobs` opens a
live overlay of them (empty if none); enter (or `/jobs <n>`) tails the
log, `k` kills the highlighted job, esc closes. `/jobs kill <n>` and
`/jobs stdin <n> <text>` still work typed. A one-shot `bash` call still
gets EOF on stdin; only a background job has a pipe. Every job is
stopped when janetcode exits.

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

`.janetcode/agents/<name>.md` (or `.agents/<name>.md`,
`.agents/agents/<name>.md`, `agents/` under the config directory, or
`.claude/agents`) defines a subagent the model can pick with
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

A running child can be steered without waiting for it to finish:
`task {"steer":"task-2","prompt":"also check the tests"}` or
`/agents steer task-2 also check the tests`. That is the existing Steer
path (ctrl+s in the parent turn) pointed at the child.

`/agents new <brief>` starts a built-in `general` subagent immediately
(read-only, like `task` without `edit:true`) and opens the overlay so you
can watch it; `/agents fork <brief>` starts a `task` fork that inherits
this conversation. `ctrl+t`
opens the overlay from the keyboard. `k` in the overlay, `/agents cancel
<id>`, or the parent calling `task {"cancel":"<id>"}` stops that child
without ending the session. A blocking `task` still dies with the parent
turn; a fork no longer has to wait for quit.

A subagent that finished is not gone: `/agents` opens a live overlay of
the continuable subagents this session started with the `task` tool.
Each row is the child's id, whether it is running or done, and what it
last did. Enter (or `/agents <id>`) opens that child's own transcript:
the brief it was given, every tool call with its output, thoughts, and
the answer it has written so far, following the tail while it still
runs. Esc goes back to the list; esc on the list restores the prompt.
With only one child, `/agents` opens the transcript directly. With none
yet, the overlay says so rather than a one-line notice. When one has
found something and the answer it wrote back is not the whole of it,
carry on by asking ("tell task-2 to also check …") or with
`task continue=<id>`, rather than starting again from an empty context.
Tell the parent to cancel a runaway child and it should call
`task cancel=<id>`.

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
a time. `/changes` lists the changes under one header per turn, grouped
by intent beneath it, with line counts and paths relative to the project;
`/changes 4` shows that change's hunks (in the web UI too). `/blame path`
attributes each line of a file to the change that wrote it (lines from
before the session are unmarked) and says so when the file was edited
outside the session since. `/undo 4` reverts one change on its own,
leaving the later changes to the same file in place: each hunk is
located in the file as it is now and put back, and a hunk whose lines
were edited since is refused rather than guessed. Reverts are recorded
as changes themselves. Turn-level `/undo`, `/undo turn n` (which
rewinds the conversation too) and `/redo` still restore whole snapshots. Shell commands are not tracked at hunk level; a git
snapshot covers them for turn-level undo.

### Undo by description

`/undo about <description>` finds which of this session's changes match a
plain-language description and previews only those, e.g. `/undo about the
logging changes but keep the rename`: everything considered is listed,
numbered, and marked `undo` or `keep`. Nothing is touched by the preview.
With a provider configured it is one side model call — not added to the
conversation, the same discipline `/btw` uses — given the description and
a compact numbered list of the candidates (path, stated reason, line
counts, a few of the changed lines); without a provider, or if the reply
cannot be used, it falls back to ranking candidates by keyword overlap
with the description and says so, since keyword overlap is a much weaker
signal than a model reading the description and the diff together.
One logical change can span several recorded changes — a rename touching
both a definition and its call sites, say, possibly in different files —
and the model is told to keep such a group on one side rather than
splitting it across `undo` and `keep`. When a single recorded change
itself mixes edits that match the description with edits that do not (a
rename and an unrelated logging tweak made in the same call, for
instance), the model is told not to guess which half to keep: it lists
that change as `mixed`, with why, and the preview shows it separately
from `undo` and `keep` rather than silently choosing a side for it.

A change whose own edit can no longer be located — the file was edited, or
reverted outside the session (e.g. `git checkout`), since the change was
recorded — is left out of the preview entirely, under its own
`unavailable` heading with why: neither undoing nor keeping it is a real
choice, so it is never offered to the model or the keyword fallback as
one.

Independently of what the model says, a deterministic guard checks every
preview for a coupling the model might have missed, by simulation rather
than by trying to recognize a rename, a deleted helper or a dropped import
as a specific shape: it reverts the `undo` side in memory and asks, for
every meaningful identifier a `keep` change relies on (appears on its own
added lines) that some `undo` change also introduced (appears on its added
lines too), whether that identifier still occurs anywhere in the touched
files besides the kept change's own added lines once the revert has
happened. If not, keeping that change while undoing the other would very
likely leave it referring to something that no longer exists. The preview
shows this as a warning naming both changes, e.g. `#3 uses calculate,
which undoing #2 would remove from main.go; the result would likely not
build — undo both, or keep both`. Because it simulates the actual revert
instead of pattern-matching a hunk, it catches a rename even when it lands
in the very same hunk as an unrelated edit (adding logging next to it,
say) — the kind of hunk a per-hunk "one name dropped, one name added"
heuristic cannot pick apart.

`/undo apply` reverts exactly the selection just previewed, newest change
first, and stops at the first change that refuses (edited since, most
often) rather than guessing past it. It refuses outright, without
touching anything, when the preview has a `mixed` change or a coupling
warning: `/undo apply --force` overrides that and proceeds anyway. Asking
twice in a row does not ask the model twice: the preview is remembered
until the next `/undo about`, and `/undo apply` refuses if the session's
changes moved since — something else was undone or added — rather than
acting on a selection that no longer describes what is there (a refusal
for `mixed` changes or a coupling warning does not consume the preview
either, so a plain `/undo apply --force` right after it acts on the same
selection without asking the model again).

Whatever was reverted, `/undo apply` always says how (or whether) the
result was checked afterwards, rather than reverting silently: if the
project's verify gate is configured and trusted, it re-runs the same
checks the gate runs (see "Verification gates", below) and reports
pass or fail with a short summary; if verify is defined in
`.janetcode/verify.json` but not yet trusted, it says so instead of
running anything (`/verify trust` trusts it); if nothing is configured at
all, it says `no checks configured (/verify set <cmd>)`. In the latter
two cases — nothing actually ran — it falls back to the language server's
diagnostics for every file any of the preview's candidates touched, kept
ones included, not only the ones actually reverted (a kept file can stop
building once something an undone one defined disappears from under it),
naming each file and either what the language server found or that the
file could not be checked; with no language server either, it says
plainly that nothing verified the result.

### Proof in the commit

Change provenance lives in the session log, which is why `/blame` only
speaks for a file this session touched. `/proof [commit]` (default
`HEAD`) makes the same evidence outlive the session: which session, turn
and stated intent wrote each of a commit's files, and what the harness
actually observed demonstrating the work — a turn's `TurnOutcome` (see
"What a turn actually demonstrated"), not the model's own claim. When the
project runs mutation testing (`/verify mutate`, below) the turn's line
also shows the score, e.g. `mutations 7/9 caught`.

When a bash call that looks like `git commit` succeeds and HEAD actually
moves, janetcode attaches this evidence automatically as a git note under
`refs/notes/janetcode`, and says so (`proof attached to <sha> · /proof
shows it`). `/proof` does the same on request: if the commit already
carries a note it is printed; if not, and this session made a tracked
change to one of its files, one is attached first. Two sessions
contributing changes to the same commit both show, rather than the
second overwriting the first — the note is merged, not replaced.

`/blame path` still answers from the session's own record first; when
this session never changed the path, it falls back to the same evidence
read from git blame plus whatever commit along the way carries a note,
showing each line's commit, its intent and its verdict.

Notes are local to the clone that made them, like any other git ref: they
are not pushed or fetched by `git push`/`git pull` on their own.
`/proof push [remote]` shares them (default remote `origin`); `/proof
fetch [remote]` pulls them, and also adds the notes refspec to that
remote's fetch config so a plain `git fetch` brings them from then on
too. Both work the same way from the command line, as `janetcode --proof
push|fetch`.

```
/proof push            # share this clone's proof notes with origin
/proof fetch upstream   # pull upstream's notes, and configure future fetches to bring them
```

A note is only useful once the commit it describes is somewhere someone
else can see it, so `/proof push` checks first: if any noted commit is not
yet reachable from the remote's own refs, it still pushes the notes (they
are harmless sitting there even so, and other commits' notes may well
already be on the remote) but warns —
`N noted commit(s) are not on <remote> yet; push your branch too`.

<sub>Under the hood, `/proof push` runs `git push <remote>
refs/notes/janetcode`; `/proof fetch` runs `git fetch <remote>
refs/notes/janetcode:refs/notes/janetcode` and adds
`+refs/notes/janetcode:refs/notes/janetcode` to `remote.<remote>.fetch`.
`git log --notes=janetcode` reads them inline once fetched.</sub>

`"proof_notes": false` (or `/config proof_notes=false`) turns off the
automatic attach; see [Configuration](configuration.md#proof-notes). The
setting takes effect immediately in a running session, the same as any
other live `/config` key.

#### Amend and rebase keep their notes

A `git commit --amend` or a `git rebase` builds a new commit object and
throws the old one away, and by default git does not carry a note across
that — the note stays attached to the abandoned commit and looks, from
`/proof`, like it was simply dropped. The first time a note is attached
(automatically or via `/proof`, and also when `/proof fetch` first
configures a remote) janetcode sets three git config values so this
does not happen:

```
notes.rewriteRef = refs/notes/janetcode   # (added, not replaced, if the key already has values)
notes.rewrite.amend = true                # (only if the key had no value at all)
notes.rewrite.rebase = true                # (only if the key had no value at all)
```

With these set, git itself copies a commit's note onto its replacement
during `--amend` or a rebase, no further action needed. Because the two
`notes.rewrite.*` keys are only ever set when they were previously unset,
an explicit `false` you (or another tool) put there on purpose is never
overridden.

### Recipes: replay a turn for free

A turn that read the right files, ran the right commands and edited the
right lines cost a model call for every one of those steps. Once it has
worked once, `/recipe save <name>` crystallises it into a small JSON file
under `.janetcode/recipes/<name>.json`: the turn's tool calls, in order,
with their inputs — dropped are any call that failed and any tool whose
effect is only on the conversation (`task`, `workflow`, `execute_code`,
`run_code`, `ask`, `cron`, `todo`, `expand_output` and the like). Saved
files are meant to be committed, the same way `.janetcode/commands` and
`.janetcode/evals` are.

```
/recipe save deploy-staging --turn 4 env=staging
```

names the recipe, picks a turn (default: the latest one that made a tool
call), and parameterises: every place the literal text `staging` appears
inside a step's JSON input becomes `{{env}}`, and the recipe records
`staging` as its default. Two params whose values overlap are substituted
longest value first, so one being a substring of the other (`v1` inside
`v1.2`) does not eat part of the longer one; this is a value-level rule,
not a token-aware one, so a very short param value can still land inside
an unrelated word — keep params to values distinctive enough that this
does not matter (a version number, a hostname), not single letters or
common short words. `--desc` sets the saved description; without it, the
turn's own first line is used. `/recipe show deploy-staging` prints the
recipe's parameters and its steps with the defaults filled in; an
`edit_file` step shows as a compact `path: "old" → "new"` instead of raw
JSON, and a step with a precondition (see below) names it at the end.

**What replay verifies before it changes anything.** A recipe never
replays a position and hopes the file still matches — a saved step is one
of:

- an `edit_file` **text-form** call (`old_string`/`new_string`, the shape
  the model writes on its own): replayed as is. `old_string` must still
  match the current file exactly once; replay fails, unchanged, on zero
  matches or on more than one (`edit_file` itself refuses an ambiguous
  match, the same as it would for a model-driven call).
- a **positional** edit — `edit_file`'s line-range form
  (`from_line`/`to_line`/anchors), or a `write_file` that overwrote a
  file which already existed — recorded at save time, not at replay
  time. `/recipe save` converts each one into an anchored `edit_file`
  text-form step using the session's own record of what the file looked
  like before and after the call: the changed lines plus just enough
  surrounding unchanged text, grown only as far as needed to be unique in
  the file as it was recorded. This is why the params above matter: give
  the value that changed a name (`/recipe save bump version=1.1.0
  from=1.0.0`) and the anchor becomes reusable — `/recipe run bump
  from=1.1.0 version=1.2.0` on a file that has since moved from `1.0.0`
  to `1.1.0` finds `## 1.1.0` and inserts correctly; running it again
  with the stale `from=1.0.0` fails cleanly whenever that text is no
  longer in the file at all. The mechanism only ever verifies text, not
  version ordering, though: if `1.0.0` still occurs somewhere else in the
  file (older history further down, say) a stale `from` can still find
  and match *that* occurrence rather than refuse — pick param values
  distinctive enough that this does not come up in practice, and check
  `/recipe run --dry-run` or the diff-stat a real run reports.
- a `write_file` that **created a new file** cannot be anchored — there
  was no "before" to diff against — so it is kept as `write_file` but
  gains a precondition: replay refuses to run it if the path exists now
  with content different from what it is about to write. A path that is
  still absent, or that already holds byte-for-byte what this step would
  write anyway (a harmless no-op), proceeds.
- `bash` steps replay exactly as recorded.

Whenever a step cannot be verified this way, **it fails and does not
touch anything** — this is the whole point: a replay must never change
content it has not verified is what the recording expected.

`/recipe run deploy-staging env=prod` runs the recipe's steps one by one,
each through the exact pipeline a model-driven call goes through —
authorize, permission rules, the approval prompt, checkpoints, the event
log — so undo, `/changes` and a commit's proof note all see it as they
would any other turn. No model is called and no tokens are spent unless a
step fails partway through, or every step succeeds but the project's own
[verification gate](#verification-gates) fails afterwards — the run stops
either way and hands the failure to the model as an ordinary turn, the
same as a queued prompt or a goal round starting its next one: which step
failed and its output, or, when the steps all worked but the checks
didn't, that outcome and the checks' output; either way the recipe's own
description goes along as the goal to finish. The one exception is a
failure that was the harness itself refusing the call — a permission
rule, a "no" from you, a failed precondition above — rather than the tool
running and failing on its own: that is reported directly instead, never
handed to the model, since the model's own retry of the very same call
would be refused the very same way. A recipe that mutated nothing skips
the verify gate entirely, the same as a read-only turn does. A run that
finishes clean reports a compact per-file `+added -removed` line count of
everything it actually changed, so success is inspectable rather than
taken on faith. `/recipe run deploy-staging --dry-run` prints the
expanded steps without running any of them, and `/recipe list` shows what
is saved.

The same replay runs with no session open at all:

```sh
janetcode --recipe deploy-staging env=prod
```

runs headless and exits; a failure — a step, or the verify gate — hands
over to the configured model the way `-p` running under `--until` does,
unless it was a permission denial (reported directly, as above) or no
provider is configured (also reported directly). Before starting
anything, this checks whether stdin can actually answer a permission
prompt: without a terminal and without `--yolo`, a mutating step that no
permission rule or auto mode already allows would only ever be
auto-denied one at a time, so the run refuses to start at all, lists the
steps that would need approval, and suggests `--yolo` or adding a
permission rule from an interactive session first — rather than spend a
model turn learning what this already knows. It builds the agent through
the same path `-p` does, so a configured, trusted MCP server's tools are
available to a step that names one; servers are only actually started
when a step's tool name says the recipe needs one, so a recipe of plain
file and shell steps — the common case — starts none and stays fast.

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

`run_code` is the same idea without Python, and a session offers it only
where `execute_code` cannot run (no Python on the PATH, or
`JANETCODE_OFF=script`): `code` is a JSON array of
`{name, input}` steps, each dispatched through the real tool pipeline
(approvals, guards, the event log). Nested `run_code`, `execute_code`,
`workflow`, `task` and `ask` are refused. `workflow` is a short
orchestration script: a JSON array of `{description, prompt, agent?}`
steps, each of which starts a real `task` child and returns the reports.

The model also has a persistent `terminal` tool: `action=open` starts a
shell that keeps cwd and environment across `send`s, `read` returns
recent output, and `close` / `signal` stop it. One-shot `bash` still
ends with EOF and is the right tool for a single command.

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

`/help` alone is a one-screen sheet of the everyday keys and commands,
and `/help all` the full one. `/help <topic>` is a
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
and can be asked as often as it is useful. `/trajectory` has the records
themselves.

### /trajectory

The session log is a list of events; `/trajectory` is the list of things
that happened, which is not the same list. A tool call and its result are
two events and one thing — bash ran, it took 23 seconds, it printed this
— and a view that prints them as two lines makes you do the join every
time you read it. A step is four or five events and one step.

So the ledger folds first, then prints. Each line is a record: its
number, what kind it is, what it did, what came back after an arrow, and
its tokens and duration right-aligned in their own column. Records are
grouped under the turn and step they belong to, and each step header
carries its span and the tools it ran (`Step 2 · 3.6s · write_file,
bash`), so the shape of a turn is legible before a word of it is read.

```
turn 1 · 3 steps · 3 tool calls · 31.2s · ↑690 ⧉57k ↓280
#1    USER    make the tests pass and tidy the tree
  Step 1 · 26.4s · bash
#2    MODEL   (tool calls only)                          ↑420 ⧉18k ↓96 3.4s
#3    TOOL    bash · go test ./... ✗ --- FAIL: TestParse…            23.0s
```

A tool call shows its arguments summarised, not dumped: raw JSON spends
the line on punctuation, so every row began with the same `{"command":"`
and the part that differed was what got clipped off the end. A file's
contents are reported by size rather than spelled out, so a short file
and a long one look like the same tool. A call that never got a result —
a cancelled turn, a call still running — stays on the ledger marked `→
…`, because a cancelled turn should look cancelled rather than simply
shorter.

| | |
|---|---|
| `/trajectory` | the most recent 60 records |
| `/trajectory all` | every record, however long the session |
| `/trajectory <n>` | one record in full |
| `/trajectory turns` | one line per turn |
| `/trajectory turn <n>` | one turn's records |
| `/trajectory slow [n]` | what took longest, longest first |
| `/trajectory grep <words>` | records matching every word |
| `/trajectory raw` | the unfolded event log, as it is on disk |

`/trajectory <n>` opens one record whole: its arguments, its full output,
and for a model call the timing split that a single duration cannot give
you — how long the first token took, how long the rest took, and the
resulting tokens per second. A slow first token is the provider queueing;
a slow decode is a long answer; the two want different responses and look
identical in a transcript. It also shows the five token buckets with the
cached share separated out, and the session's running total as of that
record.

`/trajectory grep` searches the whole record — arguments, output, the
model's answer, the tool's name, the turn — and not the line that
happened to be rendered. A ledger you can only search by what fitted on
screen is a ledger you cannot search. Terms are ANDed.

`/trajectory slow` answers the other question a trajectory is opened for:
not what happened, but what took so long. Model calls and tool calls are
ranked together, because they compete for the same wall clock. For the
session-wide version of that question, see `/perf`.

`/trajectory raw` is still there, unchanged: one line per event as it is
on disk, with the surface classification that says whether the model can
still see it. It is what to read when the ledger itself is what looks
wrong.

### The first session in a new directory

The first run in a directory janetcode has never seen is the one moment a
user is willing to be told about setup, and "you can configure a
verification command" is not worth their attention. So janetcode looks at
what the directory holds — `go.mod`, `package.json`, `pyproject.toml`,
`requirements.txt`, `Cargo.toml`, a `Makefile` with a `test` or `check`
target — and prints one line:

```
tip · Go project · /verify set go test ./... runs it after every change and hands
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

The newer features have theirs too:

- `/recipe save <name>` when a turn repeats an earlier turn's file changes
  (the same tools on the same files, in the same order, at least two of them)
- `/undo about <description>` after a whole-turn `/undo` that reverted
  changes to two or more files, or with two or more intents
- `/accept <request>` when a prompt asks for tests to be written ("write
  tests", "with tests", "test-first", "TDD") or opens a long feature request
- `/eval keep <name>` when a turn ends verified: a failing check now passes
- `/proof` and `/proof push` the first time the session's own commit gets
  a proof note
- `janetcode --what-if <session> --from-turn 1 --model <m>` after switching
  model with `/model` once the session has had a turn

A hint that teaches a subcommand is suppressed by the subcommand, not the
command: having typed `/undo` does not silence the `/undo about` hint.

Each fires at most once per project, ever. One at most per moment. Never
while a permission prompt or a picker is open. Never for a feature you
already use. `/hints off`, or `"hints": false` in the config file, silences
the lot. The rules are evaluated on events — a turn ending, a tool result, a
denial, a paste — never on a render and never on a keystroke, which is
tested.

`/perf` is the other half of `/stats`. `/stats` counts what happened;
`/perf` divides the wall clock between the four things that can consume
it, and a transcript cannot tell them apart — a provider that takes forty
seconds a call and a harness that takes forty milliseconds look identical
on screen. The report opens with how long the session has been up and how
much of that was spent inside turns; the rest is you reading and typing,
and it is not counted against the harness. Then one line each for:

- **model** — waiting for the provider to answer, with how much of that
  was spent before the first token arrived. A provider that takes a
  minute to start and one that takes a minute to finish are different
  problems with the same total: the first is the queue, the second is
  the answer being long, and they want different responses. Averages
  hide the shape
  of provider latency too, so each slice also carries its slowest single
  call: ten fast calls and one that took two minutes average out to
  "fine", and the two-minute one is the thing you felt.
- **tools** — running what the model asked for. The time you spent
  deciding on a permission prompt is *not* in here: the prompt blocks
  inside the tool call, so it is measured and deducted, and a minute
  spent thinking about a write is not reported as the write taking a
  minute.
- **you** — permission prompts and `ask` questions, waiting on an answer.
- **harness** — the remainder: the loop itself, context assembly, hooks,
  checkpoints, logging. It is computed as what is left over rather than
  measured, so anything nobody instrumented shows up here instead of
  disappearing.

Under that, the same division by model and by tool, slowest first. When
tool calls ran in parallel their times can add up to more than the turn's
wall clock; the harness row says "overlapped" rather than printing a
negative.

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

`/compact-fast` (alias `/compress-fast`) is the no-LLM form: it prunes old
tool results the way automatic compaction does first, and drops thinking
blocks from earlier turns, then stops. There is no summary call and no
handoff note. Use it when the window is full and you would rather keep
working than pay for a rewrite.

When a goal is active and `advisor_model` is set, each round gets a short
planner (first round) or skeptic (later rounds) note from that model.
This does not replace `/goal` or `goal_status`.

Compaction is two-pass on the existing machinery: at 85% of `compact_at`
it prunes (`CompactFast`); at 100% it summarises (`Compact`). It does not
replace the archive or NEED recovery.

`monitor` starts a background job (the existing jobs table) and Steers
the session when output matches a pattern. `/loop` remains the scheduler.

`/dream` consolidates overlapping auto-memory. It never edits what you
`/remember`ed. Extract still captures new facts.

`/sleep` reads the session's structured record — the goal and its
outcome, the harness's interventions (a failed verify gate, a critic
warning, a refused call), your requests, the files changed — and stores
what a future session needs, each entry citing the record lines it stands
on. It runs on its own, in the background, when a `/goal` finishes;
`/sleep` asks for it now. `EVAL_OFF=sleep` turns the automatic run off.

`read_file` extracts text from PDF (pdftotext, else a crude stream) and
PPTX, and describes images instead of refusing them as binary.

A follow-up suggestion is off by default. `/followup on` (or
`"followup": true` in the config) asks a cheap side query after a turn —
not the session system prompt, not the tool schemas — and puts 2–12 words
in the input box as ghost text. Tab accepts it. `/followup off` silences
it for the session.

`read_many_files` concatenates several text files (or a glob) in one
call. It stays behind `tool_search` so it does not sit on every request.

`advisor` is the same kind of extra: a consult the executor calls when
stuck. The harness forwards the recent transcript (no session system
prompt, no tool schemas). At most three uses per turn. It is never on
the always-on schema list unless `"advisor_model"` is set, in which case
the executor sees `advisor` without a hunt. `"advisor_model"` is who it
asks (for example `z-ai/glm-5.3` beside an `inception/mercury-2.5`
executor); otherwise the session model. It is routed like any other
named model, so it can live on a different provider.

A tool a profile or the parent session has removed is **withheld**, not
unknown: the model is told the policy reason and not to hunt for a
workaround.

File reads share an I/O cache with subagents, so a child that looks at a
file the parent already read does not hit disk again. Writes drop the
path.

Edits that look like they faked a green (always-true `__eq__`, a call
counter as the answer, a rewritten test expectation) are **reported** on
the verdict line. They never change the verdict and never block.

### Cua Driver

Desktop automation that is not pixel-guessing: [Cua Driver](https://cua.ai/docs/how-to-guides/driver/install)
(`cua-driver` on PATH) or `npm i -g @qwen-code/open-computer-use`. janetcode
does not ship the native driver.

`/cua on` (or `"computer_use": true` in the config) starts it as the MCP
server `cua-driver`. Its tools (`list_apps`, `list_windows`,
`get_window_state`, `click` with `element_token`, …) are deferred behind
`tool_search` as `mcp__cua-driver__*`. Screenshots the driver returns are
attached as images so a vision model can see them. `/cua off` stops the
server. `/cua doctor` runs the driver's own checks.

The built-in `computer` tool remains a last-resort pixel fallback
(screenshot / click / type / key via xdotool or osascript). The
`computer-use` skill prefers Cua Driver when it is connected.

`/mcp add cua-driver cua-driver mcp` is the same registration if you would
rather list it under `mcp_servers` yourself.

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
| **tests written** | an `/accept` writing-phase turn wrote only test files and they fail, as they should until the implementation exists |

Each check command counts by its latest run in the turn, including the
runs of the turn-end verify and `/accept` gates, so a red run fixed and
re-run green is not a failure; a leading `cd <project> &&` does not make
it a different command. A call that was refused — by a rule, a mode, a
hook, or you — never ran and is not counted as a check. A proof note
attached to a commit the turn made is brought up to date once the turn's
gates have run.

When the answer claims something the turn does not show — and only then —
the model is told once, with what was observed, and asked to run the check
or say plainly that it did not. Nothing blocks: a gate that refuses to let
a turn finish wedges more work than it saves. `inspect verdicts` shows the
history with each claim beside its evidence, and `--eval-off verdict`
turns it off.

Calibrated by replaying 97 real turns (commit `3ee2021`, "fix(verdict):
calibrate it against real sessions"): the first version called nothing
verified and raised 13 false accusations; measured again it recognises
42% of turns as verified, with none. The commit history has what the
first version got wrong and how it was fixed.

### Plan mode and read-only mode

`/plan add rate limiting to the API` puts the session in plan mode: the
model only gets read-only tools, explores the code and writes a numbered
plan under `## Plan` (files, order, verification, risks).

That plan is written to `.janetcode/plan.md`, next to the project's other
janetcode files, and the file is the plan: nothing else holds a copy of
it, so what you review is what the model wrote. A later turn that revises
the plan rewrites the file and keeps the version it replaced in
`.janetcode/plan.md.prev`, which is one `diff` away from telling you what
changed. A reply with no `## Plan` heading — a question on the way to a
plan — leaves the file alone.

`/plan review` shows the file with line numbers, the way `/open` shows any
file. `/plan comment 12 this hits the database twice` attaches a remark to
line 12; the line can also be written the way review prints it
(`plan.md:12`). Comments collect, show up under their lines on the next
`/plan review`, and go nowhere until you act on them:

- `/plan changes` sends every collected comment to the model as one
  message — each comment with the plan line it is about — and stays in
  plan mode, so the model rewrites the plan instead of starting work. The
  collection is emptied. If a turn is still running the comments steer it
  rather than starting another.
- `/plan approve` leaves plan mode and tells the model to execute the
  plan step by step, verifying each step. Any comment you collected and
  never sent is dropped, and the note says the plan file was approved.

Every step of the execution is a normal turn, so `/undo` walks it back.
`/plan off` leaves without executing. Questions and changes can still be
typed as ordinary messages; the comments are for the parts of the plan
that are easier to point at than to describe. `shift+tab` cycles the four session modes
without typing a command: **ask** (edits and commands ask first) →
**plan** → **auto** (`/auto`, in-workspace edits and known-safe reads
run; other commands still ask) → **yolo** (`/yolo`, tools run without
asking) → ask. The current mode stays on the status line (and as a
header prefix); cycling it does not print into the transcript. `/edits`
and `/readonly` stay their own toggles.

`/sandbox workspace` runs local bash under a named profile (see *The
sandbox* below); `/sandbox on` is the same as `/sandbox workspace`. File
tools, `/ssh` and `/docker` are unchanged: only the shell is isolated,
and it is off by default. `/review` starts two read-only
subagents over `git diff HEAD` (bugs and tests). `/loop 5m <prompt>`
repeats that prompt for this session; the timers are written on the
session record and restored on `-c`/`-r` of that session (`/loop off`
cancels them). `/fork [title]` copies the conversation and continues in
the copy; `/fork stay [title]` lists the copy and stays here. `/add-dir <path>` grants file tools
another local root for this session. `/btw` asks a side question that is
not stored as a user turn; `/copy` copies the last reply; `/config key=value`
sets a catalogued setting; `/doctor` prints the same checkup as
`janetcode --doctor` without changing files — including the `would fix:`
lines, which say what `janetcode --doctor --fix` would repair, and the
`needs you:` lines it never will (trusting a project's `verify.json`). Repairing
is the command line's job, so a running session never changes files
behind you.

### The sandbox

The sandbox is off by default. When it is on, local bash — and any
background job started from it — runs under a named **profile**; file
tools, `/ssh` and `/docker` are untouched, so only the shell is confined.

| profile | writes | reads | network | never writable |
|---|---|---|---|---|
| `workspace` | the project root, the directories `/add-dir` granted, and the temp dir | anything | allowed | `.janetcode/**`, `**/id_rsa`, `**/id_ed25519`, `**/.ssh/**`, `**/.aws/credentials` |
| `read-only` | nothing | anything | allowed | — (nothing is writable) |
| `strict` | the project root, the `/add-dir` directories, and the temp dir | anything | **none** | `.git/**` and everything `workspace` denies |

`/sandbox on` means `workspace`. `/sandbox off` turns it off again.
`/sandbox` alone reports the active profile, the backend, and whatever
that backend cannot enforce. `/sandbox <image>` names the docker image
(default `alpine:3.20`) and `/sandbox docker|seatbelt|landlock|auto`
forces a backend instead of the one picked for the machine. A project
can start every session sandboxed with the `sandbox` config key (see
*Configuration*).

`strict` denies writes to `.git/**`, which means git cannot commit
inside it. That is the point of the profile; use `workspace` when the
model is expected to commit.

**Backends.** macOS uses `sandbox-exec` (seatbelt). Linux uses docker
when it is installed, and **Landlock** otherwise — the kernel LSM, no
container and no image. `/sandbox landlock` chooses it even where docker
exists.

What each backend can and cannot do:

- **docker** mounts every write root read-write and everything else the
  command can see read-only, gives a profile with no network
  `--network=none`, and re-mounts a deny glob that names a directory
  (like `.git/**`) read-only on top. It cannot mount a *wildcard* deny
  glob such as `**/id_rsa`. The command also runs inside the image, not
  against your machine's toolchain.
- **seatbelt** writes a policy with a `subpath` rule per write root, a
  `deny` rule per deny glob (a `subpath` for a directory, a regex for a
  wildcard glob) and `deny network*` for `strict`. Reads stay broad by
  design.
- **Landlock** confines writes to the profile's roots in the kernel.
  It has allow rules only, so it cannot carve a deny glob out of a root
  it made writable. It also cannot take a network away before ABI 4
  (Linux 6.7), so `strict` **refuses to run** on such a kernel rather
  than pretending — install docker, or use `workspace`. From ABI 4 it
  denies TCP bind and connect, but not UDP (DNS included) or unix
  sockets; docker's `--network=none` is the stronger no.

Whatever the backend, a deny glob is also enforced before the command
runs: janetcode reads the command line with the same static analysis the
acceptance-test lock uses, and refuses `rm -rf .git` under `strict` with
a message the model can act on. That analysis sees the write targets of
a command line — the path arguments of `mv`, `cp`, `rm`, `tee`, `sed -i`
and friends, and redirect targets — not what a program decides to write
once it is running; the backend is what stops the rest.

Two Landlock details worth knowing: `/dev/null`, the terminal and
`/dev/fd` stay writable under every profile (a shell that cannot write
them cannot report anything), and the sandboxed command is started by
re-executing janetcode itself, which applies the policy and then execs
your shell.

### Acceptance tests

`/accept <request>` puts the session in a test-first mode: the model's
only job is to write acceptance tests for the request — tests that fail
against the code as it stands and would pass once the request is done.
Every other mutating tool is refused while this lasts — except read-only
shell commands and running the tests, which still ask as usual; a `write_file` or
`edit_file` on anything but a test file is turned down the same way
read-only mode turns down an edit, and the model is told to touch only
test files. This restriction holds through both the writing phase and
the "proposed" phase that follows it — from the moment the model states
its verify command until you actually run `/accept approve`, only test
files may still be written, whatever the model tries next; approval is
the only thing that hands the implementation tools back. When it is
done writing tests it ends its final message with one line of the fixed
form `ACCEPT-VERIFY: <command>`, naming how to run the tests it just
wrote (revising the tests after that sends it back to state the command
again, so a dropped test does not stay in it) — one literal shell command on that line, with no wrapping needed
around it (a backtick or quote pair wrapping the whole command is
stripped automatically, but content spanning more than one line is
rejected rather than run literally); the harness nudges it once for a
missing or unusable command before falling back to a guess (`go test
./<pkg>/...`, `pytest <files>`, `npm test`, `cargo test`, or a matching
command already in `.janetcode/verify.json`).

Review the tests with `/diff <path>` for each file `/accept` lists (they
are new, untracked files, so `/diff` shows them in full), then run
`/accept approve`, which prints the locked files and the command that
will gate the session for you to confirm. From that point the files are **locked**: any
`edit_file`/`write_file` call on one, or a bash command that would write
to one (`echo > path`, `sed -i`/`perl -i`, `mv`, `cp`, `rm`, and the
like — the same path-escape analysis the workspace guard uses), is
refused up front with a message telling the model the test is locked and
to fix the code instead. That pre-execution check cannot cover every way
a command could reach the file, so it is backed by a content guarantee:
each locked file's full content is captured at approve/lock time
alongside its hash, and after *every* mutating tool call while locked —
whatever tool ran, including one made from inside a script or run_code
step — the file's hash is checked again; if it moved, it is restored
from the captured content immediately, and the tool call that caused it
is told plainly that its change was reverted and to fix the code
instead, with a matching notice for you. Their run command also becomes
an extra verification gate for this session only — not written to
`.janetcode/verify.json`, and not applied to any other session — so a
turn is not done until it passes, alongside whatever the project's own
checks already require. If the gate command itself cannot run at all
(exit 126 or 127 — not found, not executable), the harness gives up
after that one attempt instead of spending retries on a command that no
code change can fix, and says to correct it with `/accept command
<cmd>` rather than editing code. A turn's verdict (see above) never
marks a locked test as self-checked, because the model could not have
edited it: an edit the lock refused never lands as a successful write,
so it never enters the turn's edited-files count.

`/accept unlock` lifts the lock when a test itself turns out wrong (its
hash and stored content are refreshed the moment the fix is saved);
`/accept lock` reapplies it. `/accept command <cmd>` replaces the
proposed or locked verify command directly, for when the stated or
inferred one is wrong. `/accept` alone reports the request, the locked
files, the exact command in effect (quoted, so any stray wrapping is
visible rather than swallowed), whether their hashes are intact, and the
last gate result; `/accept off` ends the mode and unlocks everything.
The state is kept on the session record, so `-c` or `-r` on a locked
session comes back locked — the restriction on non-test writes, the
lock itself, and the content-restore guarantee all still apply after a
resume, the same as in the session that set them up.

A `task` with `fork: true` inherits this conversation's history and
tools, runs in the background, and can be continued by id. It cannot
spawn another fork, and cannot be combined with `edit: true`.

`/readonly` (or `--readonly` on the command line) is the same restriction
without the plan: useful for reviews and for letting the model answer
questions about a codebase you do not want touched. In both modes a
mutating call the model tries anyway fails with a message rather than
asking you.

A long task the model is working through keeps a plan of its own, in the
`todo` tool rather than in prose. While that plan exists it stays under
the header as a sticky strip: a tally, the step in progress, and what
is next, rewritten on the frame that marks a step off. A short window
collapses it to one line; a very short one hides it so the transcript
keeps the rows. `/todos` opens a live overlay of every step and its
status (pending, in_progress, completed); it re-reads when the model
rewrites the plan, and esc closes it. With none yet the overlay says
so rather than a one-line dump.

The model can ask you a labeled question with the `ask` tool when a
choice would change what it does next. Several questions in one call
are tabs: left/right or tab switches without dismissing the prompt,
1–9 or enter picks an option, and Other lets you type something else.
The chosen label (and any Other text) is what the model receives.

`task` with `agent: explore` runs a built-in read-only subagent: it can
read, list, glob and grep, but cannot edit files or run commands, and
it needs no profile file in `.janetcode/agents`. `agent: general` is the
same kind of built-in subagent for a job that is not specifically
exploration. Other names still load from those files.

### Moving the working directory

`/cd <path>` moves the session to another directory without restarting it.
It is the local form of the retarget below: the same shared workspace
pointer is swapped, so the file tools and `bash` follow, and everything
else the session roots at the project follows with them — the repository
map, `.janetcode/`'s hooks, verification and permission files, skills,
rules, project memory, the plan file, the language servers, the checkout
root editing subagents branch from, the snapshots `/undo` and `/proof`
take, and the working directory the system prompt states. Relative paths
and `~` resolve against where the session is *now*, not where janetcode
was started, so `/cd ../sibling` works after an earlier `/cd`. A path
that does not exist, is not a directory, or cannot be opened is refused
and nothing moves. A directory `/add-dir` granted is a perfectly good
target. `/cd` with no argument says where the session is rooted, what
else the file tools may read, and that `/ssh` and `/docker` are the
remote forms.

The conversation is not reset — this is a move, not a new session. The
model is told once, as a harness note, that the directory changed and
that relative paths it gathered before no longer mean what they did. The
session's metadata records the new directory, so `--continue` from there
finds this session afterwards.

Three things cannot follow, and `/cd` says so rather than leaving them
pointed at the tree you left. A running language server is stopped and a
fresh one starts in the new tree on the next edit. Background jobs,
open terminals and subagents that are still working keep the directory
they were started in — they are processes and checkouts, not settings.
The archive of full tool outputs stays where it was opened, because the
results in it belong to *this* conversation and `expand_output` still has
to be able to read them back. Trust does not travel either: the new
directory's `.janetcode/hooks.json`, `verify.json` and `permissions.json`
are re-read and re-checked against the trust store, so a tree you have
never approved starts unapproved.

`/cd` costs almost nothing in prompt cache. The working directory lives
below the `# Environment` heading of the system prompt, which is the part
deliberately left outside the cached prefix (it carries the date too, so
it changes daily anyway); the cached prefix above it and the whole
conversation are untouched. What a move does cost is the next turn's
repository map, which is rebuilt for the new tree and attached to that
turn like any other — the same price the first turn in a project pays.

### Remote workspace

`/ssh user@host` (or `user@host:/path`) retargets the file tools and
`bash` onto that machine. One shared workspace pointer is swapped, so
every tool sees the new root without rebuilding the agent. `/ssh off`
returns to this directory. `/docker <container>` (or
`container:/path`) does the same through `docker exec`. The session,
memory, snapshots, language servers and `/undo` stay on the local
project; only what the model reads, writes and runs moves. That is the
difference from `/cd`, which moves all of them: `/ssh` changes *where the
tools run*, `/cd` changes *where the session is*. For the same reason
`/cd` is refused while the workspace is remote — bring it back with
`/ssh off` first, or move it with `/ssh user@host:/path`. `ssh` uses
`BatchMode` so it will not prompt for a password: keys or an ssh-agent
have to already work.

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
`"mutate": "go test ./..."` in the file (or `/verify mutate go test
./...`), once the commands pass janetcode takes the lines the turn
changed, flips one operator at a time (`==`/`!=`, `<`/`<=`, `&&`/`||`,
`+`/`-`, `true`/`false`), runs the test command against each mutant in
a throwaway git worktree of the current tree, and treats a mutant the
tests let through as a failure: the model is told which line's behaviour
is not pinned down and asked to add a test (or say why the mutation is
equivalent). At most twelve mutants run per turn (`max_mutants`), spread
across the changed lines, so the cost is bounded by twelve test runs.
Test files and comments are never mutated. Every run's score is logged
against the turn it tested, so `/proof` shows it too (see "Proof in the
commit", above).

`/verify` runs the checks now, `/verify set make test` replaces the
file's commands with that one, `/verify add go vet ./...` appends another,
`/verify clear` removes them all. The command is taken as typed, without
quotes: one pair of quotes or backticks wrapped around the whole command
(`/verify set "go test ./..."`) is taken off, since the shell would
otherwise look for a program named `go test ./...`. Like hooks, a verify file that
came with a clone needs a one-time `/verify trust` before it runs, because
it executes commands on your machine; files you write through `/verify
set` or `/verify add` are trusted as you write them. In headless mode `--until` remains the
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

#### The reach of an edit

Before you approve an `edit_file` or `write_file` call to a file that
already exists, the prompt names what it reaches: the symbols whose
definitions the change touches, how many places reference each one, which
test files already cover them, and which verify commands will run —
`changes Parse (func) · used 14 times in 6 files · tests: parse_test.go,
cli_test.go · checks: go test ./...`. Only the lines the edit actually
inserts, removes or replaces count, and a definition the edit adds is named
as `adds parseAll (func)`, with no refs — nothing uses it yet — rather than
as a change to the functions on either side of it. There is nothing to show for a new
file (there is no "reach" for code that does not exist yet) or a non-code
file, and the line is silent then.

The answer comes from the language server for that file when one is on
PATH — the same resolved view `symbols` queries — and from the repository
map otherwise: its regex/AST-based index finds a file's definitions and
counts identifier occurrences without a running server, at the cost of a
coarser, unresolved count. An answer built the second way, or one cut
short by the time budget below, says `may miss some uses`.

This never delays the prompt beyond a fixed, short budget (well under a
second): whatever is not known by then is left out rather than waited for,
so a slow or absent language server never makes approving a change feel
slower.

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

| event | when | `match` is tested on | what its answer can do |
| --- | --- | --- | --- |
| `pre_tool` | before a tool call is approved | the tool name | deny, allow, ask, rewrite the input, add a note |
| `permission_request` | as a call is about to be put to you | the tool name | answer in your place: allow or deny |
| `post_tool` | after a tool call | the tool name | add a note to the result |
| `post_tool_failure` | after a tool call that failed | the tool name | add a note to the result |
| `user_prompt` | before your message is sent | — | refuse it (nothing is sent or recorded), add context |
| `session_start` | at a session's first turn | `source`: startup, resume, clear | add context |
| `session_end` | when a session that ran a turn ends | `reason`: exit, clear, resume | — |
| `stop` | when the model means to end its turn | — | send it back to work |
| `stop_failure` | when a turn ends on an error | `error_type` | — |
| `pre_compact` | before the conversation is compacted | `trigger`: auto, manual | add to the handoff note |
| `post_compact` | after it was | `trigger` | — |
| `notification` | when janetcode waits on you | `notification_type`: permission_prompt, idle_prompt | — |
| `subagent_start` | as a subagent starts | its kind: a profile's name, general, editor, fork | add to its brief |
| `subagent_stop` | when it finishes | its kind | — |

Events marked — only observe: `session_end`, `stop_failure`,
`notification` and `subagent_stop` run in the background (at most 30
seconds; `session_end` is waited for, since janetcode may be exiting).
Added context reaches the model as a block marked as a hook's.

Each hook gets the event as JSON on stdin: `event`, `cwd`,
`session_id`, `transcript_path`, `permission_mode` (ask, auto,
auto_edits, plan, yolo, read_only), and what the event is about — `tool`
and `input`, for `post_tool` the `output`/`is_error`, for `stop` the
model's `text`, `prompt`, `source`, `reason`, `trigger`, `tokens`,
`summary`, `notification_type` and `message`, `error_type` and `error`
(`loop_detected`, `max_turns`, `rate_limit`, `authentication_failed`,
`billing_error`, `server_error`, `invalid_request`, `unknown`), `agent`
and `agent_id`. `JANETCODE_EVENT`, `JANETCODE_TOOL` and `JANETCODE_CWD`
are in the environment. `match` is a regexp, anchored at neither end, on
what the table says; `""`, `*` and `.*` match everything. What the hook
prints decides:

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

#### Hooks written for Claude Code or Qwen Code

A `"hooks"` object copied from a Claude Code or Qwen Code settings file
runs as it is. Their event names (`PreToolUse`, `PostToolUse`,
`PostToolUseFailure`, `PermissionRequest`, `UserPromptSubmit`,
`SessionStart`, `SessionEnd`, `Stop`, `StopFailure`, `PreCompact`,
`PostCompact`, `Notification`, `SubagentStart`, `SubagentStop`) are read
as the events above, and their `{"matcher": …, "hooks": [{"type":
"command", …}]}` groups as hooks. A matcher may name their tools (`Bash`,
`Read`, `Write`, `Edit|MultiEdit`, `WebFetch`, `Task`,
`run_shell_command`, …), matched against the whole name. Only command
hooks run; a `prompt` or `agent` hook is refused when the config loads.

The stdin carries their field names too (`hook_event_name` in their
spelling, `tool_name`, `tool_input`, `tool_response`,
`last_assistant_message`, `compact_summary`, `agent_type`), though
`tool_name` and `tool_input` hold janetcode's own tools and inputs
(`bash` with `{"command": …}`, `write_file` with `path`): a script that
compares `tool_name` to `Bash` or reads `file_path` needs changing,
where a matcher does not. Their answers are understood: `hookSpecificOutput.permissionDecision`
(allow, deny, ask) with its reason, `updatedInput`, `additionalContext`,
`decision.behavior` for `permission_request`, `continue: false` with
`stopReason`, and `systemMessage`, which is shown to you rather than the
model. `permission_request` hooks cannot rewrite the input; use
`pre_tool` for that. Events those tools have and janetcode does not
(`PreToolBatch`, `InstructionsLoaded`, `ConfigChange`, …) are refused by
name when the config loads, rather than never running.

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

#### Adding a server

`/mcp add` walks through it: a name, the transport (a command here, or a
remote HTTP/SSE URL), the command or URL, any environment variables or
request headers, and a confirmation. On confirm the entry is written into
the project's `.mcp.json` — created if absent, and otherwise left as it
was, other keys, server order and indentation included — marked trusted
(you wrote it yourself), started, and its tools listed.

`/mcp search <words>` looks servers up in the official MCP registry
(registry.modelcontextprotocol.io; `JANETCODE_MCP_REGISTRY` points it at
another server with the same API) and prints, for each, the `/mcp add`
line that would add it, with `<placeholders>` for keys and arguments you
fill in. It installs nothing: running the line is the step that writes
and starts the server, so you see the exact command first. A local
package (npm, PyPI, a container image) is preferred to a hosted URL.
Registry entries are published by their authors and not reviewed.

`/mcp add <name> <command or url>` does the same without the dialog, for
scripting and for the web UI:

```
/mcp add fs npx -y @modelcontextprotocol/server-filesystem .
/mcp add docs --env API_TOKEN=… -- npx -y docs-mcp
/mcp add remote https://example.com/mcp --header 'Authorization: Bearer …'
```

Options come before the command, because everything after it belongs to
the command: the `-y` in `npx -y pkg` is npx's. `--env K=V` sets the
command's environment, `--header 'Name: value'` a remote server's request
headers, and `--transport stdio|http|sse` pins the transport rather than
letting the URL decide. `--` hands every remaining word to the command.

`/mcp add --from <git url or directory>` installs what a repository
declares for itself. The contract is the smallest one that already
exists: a file at the repository's root named `.mcp.json` or `mcp.json`,
holding the same `{"mcpServers": {…}}` object as the project file, and
nothing else — no registry, no install script. The repository is cloned
shallow (or read in place when it is a directory) into
`mcp-servers/<repo>` next to the config file, and every entry that runs a
command gets its `cwd` set to that checkout, so a relative `node
server.js` works. Nothing is written until you confirm what it found; in
the web UI, where there is no dialog, the first call prints what it would
add and `--yes` performs it.

`/mcp remove <name>` stops the server and drops it from `.mcp.json`,
after a confirmation (`--yes` in the web UI). A server that comes from
the config file rather than the project is left alone and says so.

Server tools appear as `mcp__<server>__<tool>`. They ask for permission
unless the server marks them read-only. Servers that publish resources get
`mcp__<server>__list_resources` and `read_resource` tools, and every prompt
a server offers becomes a slash command: `/mcp__<server>__<prompt> args`.
`/mcp` shows status, tools and prompts. The web UI answers `/mcp`,
`/mcp add` and `/mcp remove` in their non-interactive forms; `/mcp auth`
and `/mcp trust` stay in the terminal.

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
