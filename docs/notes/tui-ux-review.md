# TUI UX review: what four other harnesses do better

See also [qwen-code-review.md](qwen-code-review.md) for the agent core and
tools of qwen-code, reviewed the same way.

Four Opus agents read the terminal UIs of qwen-code (React/Ink, ~166k
lines), grok-build (ratatui, ~516k lines in the pager alone), pi (its
own `pi-tui`, ~17k lines plus a 6.6k-line interactive mode) and kimi-code
(pi-tui fork, ~64k lines) on 2026-09-11, against janetcode's ~9.5k-line
bubbletea TUI, with file references. opencode was not available. This
is the merged list, ranked by how many of the four do it and how much it
would change daily use. Where two or more agree, that is the signal.

## Adopt, ranked

1. **Per-block expand and collapse, not one global verbose flag** (all
   four). qwen-code registers each clipped block and offers `ctrl-s` only
   when something is actually clipped; grok folds one entry with `e`, all
   with `E`, all thoughts with `ctrl+e`, copies with `y`; kimi expands only
   the last three turns. janetcode's `ctrl+o` re-renders the whole
   transcript, and a folded thought can never be reopened.
2. **Keep the tail of command output, not the head** (grok, pi). grok
   shows first 2 / last 3 lines; pi shows the last 5 visual lines of bash.
   janetcode's `clip` keeps the head, which for a failed build is the
   compiler's preamble, not the error. Reads: first 5 / last 3.
3. **Name what the turn is waiting on** (grok, kimi, pi). grok's status
   has `WaitingReason` (model, subagent, task output, sleep) plus
   `Retrying 2/5 · reason · in 12s` and `AutoCompacting`; kimi and pi show
   retry countdowns. janetcode's retries and backoff are invisible; a
   stalled call looks like a slow one.
4. **Terminal integration** (grok, kimi, qwen-code): OSC 133 zones around
   messages so the terminal's own prev/next-prompt keys jump between
   turns; OSC 9/99/777 desktop notification when a prompt is waiting or a
   turn ends, only while unfocused (DEC 1004 focus tracking); a bell after
   20s unfocused; tab title from the session title; taskbar progress
   where supported. janetcode emits none.
5. **Paste chips** (qwen-code, kimi, pi, grok — all four). Over 10 lines
   or 1000 characters the paste becomes `[paste #1 +123 lines]`, one
   atomic grapheme for cursor and delete, re-inflated on submit, with a
   guard so a paste ending in a newline cannot submit itself. janetcode
   takes pastes verbatim into a 6-line box and a trailing newline sends.
6. **Persistent prompt history per working directory** (grok, kimi).
   JSONL with a bash flag so `↑` restores `!` mode; capped; `↑` on an
   empty prompt browses, `esc` restores the draft. janetcode's history
   dies with the process.
7. **Diffs with line numbers, elided gaps and word-level highlight**
   (all four). A gutter sized to the file, `… N unchanged lines` between
   hunks, 3 lines of context, and for a one-line change only the changed
   tokens highlighted. grok and pi highlight syntax too. janetcode
   colours by leading `+`/`-` only, and **truncates diff lines by byte
   slice (`renderPreview`, `l[:width-5]`), which corrupts UTF-8 on any
   non-ASCII line — fix regardless**.
8. **Searchable pickers for every list** (kimi, pi, qwen-code). janetcode
   has one (`/connect`); `/resume`, `/model` and `/permissions` are typed
   numbers. Reuse the connect machinery. pi's session picker draws forks
   as a tree; double-escape on an empty prompt rewinds to an earlier
   prompt by forking.
9. **Steer a running turn** (kimi `ctrl-s`, pi merges queued text back on
   interrupt). janetcode queues and can neither edit nor pull back a
   queued line; `esc` should never destroy typed text.
10. **Command availability model** (kimi): commands declare `idle-only`
    and resolving one mid-turn returns a *blocked* intent with a message,
    instead of a per-command `if m.running` check. Argument completion
    per command, fuzzy matching that splits on `/`.
11. **Height-aware budgets** (qwen-code, grok). Output caps scale with
    terminal height; grok drops optional rows below 16 lines and goes
    compact below 20 without persisting the choice. janetcode's caps are
    fixed line counts.
12. **Reserved rows** (grok): a status or menu row is reserved blank
    rather than appearing, so the prompt never jumps. janetcode already
    does this for the completion menu; generalise.
13. **Colour fallbacks** (grok, qwen-code): honour `NO_COLOR`, quantise to
    256/16 colours, an ANSI-16 pass so dark RGB does not collapse to
    black, a terminal-native palette using reset + dim. janetcode has one
    adaptive truecolor palette.
14. **Streaming diff before the tool runs** (pi): once an edit's arguments
    finish streaming, compute the diff against the real file and show it
    while the model is still writing the rest of the call.
15. **Tool-owned rendering** (pi): tools carry their own render hooks
    with a fallback, so MCP and custom tools look native instead of
    hitting a `switch` on the name.
16. **Small things worth copying verbatim**: a finished tool flashes
    bright for 400 ms (grok); toasts live ~3 s; `Thought for 12.3s` as the
    folded thought header (grok); cache-waste accounting that prints
    `cache miss after 12m idle: 45k tokens re-billed (~$0.14)` (pi);
    strip C0/C1 control characters from repo-supplied text shown in the
    trust prompt (kimi); resume replays only terminal tool states, not
    streaming intermediates (grok); error messages shaped
    `Headline (code): why. What to do.` (grok).

## Keep as is (the others do worse)

- The permission prompt. pi has none at all; grok's card shows no diff.
  janetcode's inline diff with y/a/r/n/esc/c and a reason back to the
  model is the best of the five. Take grok's scope editor (widen or
  narrow how many command words a saved rule covers, with a floor for
  `sudo`, `npx`, `docker`, `ssh`, `xargs`) without giving up the diff.
- Markdown rendering: janetcode's per-block cache with an adaptive
  throttle beats pi's re-parse-per-token; grok does the same as ours.
- Thought folding: three lines streaming, one line after, is what grok
  does and better than pi's all-or-nothing.
- The context gauge and the single status row with an explicit yield
  order; qwen-code's footer is a dozen pills.
- No code backgrounds (pi and glamour tint blocks); no emoji spinners
  (kimi's moon); one adaptive palette rather than fifteen themes.
- Size. These are 7x to 50x larger. Every item above is 20 to 200 lines.

## Not settled by reading code

Perceived latency of each renderer under a fast stream; whether
main-screen (non-alt-screen) modes really preserve scrollback in daily
use; how grok's replay behaves on sessions much larger than its window.
Only side-by-side runs answer those.
