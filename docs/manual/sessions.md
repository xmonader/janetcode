# Sessions

Part of the [janetcode manual](README.md); the [README](../../README.md) has the short version.

Every conversation is saved as append-only JSONL under
`~/.local/share/janetcode/sessions/`.

A session's first turn in a git repository also records the commit `HEAD`
pointed to and a snapshot of the work tree at that moment, so the session
can later be turned into a regression eval with `/eval keep` (see
[evals](evals.md)) that replays it against the state it actually started
from.

Every session gets a title after its first turn: the model is asked for a
few words (a headless run uses the first prompt instead), and `/title
<text>` renames one. `/fork [title]` branches the conversation: a new
session starts with a copy of the messages and pins, the original stays
as it was, and `/resume` lists both so you can try two approaches from the
same point. `/fork stay [title]` makes the same copy but stays on the original
session; later messages land only on the live one, and the copy is listed
with a parent link (`fork_of`). `/history <words>` searches every saved session in every
directory for the words and shows the best matching line with the session
id to `/resume`.

`janetcode --bg "prompt"` starts that prompt in a detached child, prints
the session id, and returns. Reopen it with `--resume <id>`. Armed `/loop`
jobs are stored on the session and restored when that session is resumed.

```sh
janetcode --sessions        # list sessions for this directory
janetcode --continue        # resume the newest one
janetcode --resume <id>
janetcode --bg "fix the tests"
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
