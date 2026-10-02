# Durable turns: continuing a turn the session died in

Idea from pi 1.0's `pi-durable` (intent committed before a tool runs; on
reopen only replay-safe tools rerun, the rest get an "interrupted" result).
This note is what janetcode does, written before the code.

## What was there

Every event is appended to the session log as it happens, and the fold
(`session.FoldSurface`) keeps the history provider-valid: a `tool_use` with
no answer gets a synthesised "the session ended while this call was
running" result. Three gaps:

1. A turn's tool results reach the model-visible history together, in one
   message, after the last of them. A crash after three of four calls had
   finished threw away the three real results — they were on the log as
   log-only `tool/result` events — and called all four "unknown".
2. A turn that ended on an error, esc or the iteration cap logged no
   `turn/end`, so the log could not tell "stopped" from "died".
3. Resuming never continued the turn: the user had to say what they
   wanted again.

## What is persisted before what is shown

Unchanged, and already enough: `tool/call` is appended after the call is
approved and before it runs; `tool/result` (log-only, with the call id)
after it returns, before the results message; the assistant message after
its model call completes. A model call that was streaming has logged
nothing. New: `turn/start` carries `{"logs_end":true}` in Extra, and every
exit from a turn that is not a crash appends `turn/end` (reasons
`cancelled`, `max-iterations`, `error: …`). A turn whose start has the
marker and that has no end died. Sessions written before the marker are
never reported as interrupted.

## What resume does

For the interrupted turn, by where it died:

| died while | log shows | on continue |
|---|---|---|
| (a) a model call was streaming | user message (prompt or tool results) last | the same model call is made again; nothing else |
| tool calls of a step were running | assistant message with calls, no results message | each call is answered as below, the answers logged as the step's results message, then the next model call |

Each call of that step:

- **finished** (`tool/result` logged): its real output, unchanged.
- **(b) read-only, started, unfinished:** run again now, through the normal
  approval and logging path — reading twice is harmless.
- **(c) mutating, started, unfinished:** never run again. The model gets
  "this call was running when the session ended; it may or may not have
  taken effect", plus the evidence: for `write_file` whether the file now
  holds exactly that content; for `edit_file` whether the new text is
  there and the old text gone (done), the old text still there (not
  done), or neither (unknown); for anything else, `git status --short`
  when the directory is a repository.
- **(d) never started** (no `tool/call`: it was waiting for approval or
  queued behind a serial call): "most likely did not run, but that is
  unknown; check before reissuing anything that changes state". Not a
  flat "not run": a `tool/call` line lost to the crash would make that
  invite a second run of a change. A reissue goes through approval again.

The interrupted turn is closed with `turn/end` reason `interrupted`, and
the continuation runs as the next turn (`turn/start` reason
`resume:<n>`), with no new user message: the model sees exactly the
history it would have seen, plus the recovered results.

Gap 1 is fixed for every resume, continued or not: the fold uses a
finished call's logged result instead of "unknown".

## How it is asked for

Continuing spends a model call and may run tools, so it is never
automatic: `/recover` in the terminal and the web after opening an
interrupted session (the opening notice says so), `--recover` with
`--resume`/`-c` for `-p` and `--bg`.

## Not covered

- The undo checkpoint of the interrupted turn is written when a turn
  ends, so a crash loses that turn's manifest; the shadow/git snapshot
  taken before each mutating call is still there, but `/undo` cannot name
  the turn. Recovering it needs the manifest written per call.
- A mutating MCP tool or a background job started in the dead turn is
  reported as "may or may not have happened" with no evidence beyond
  `git status`.
- Partial streamed text of the dead model call is not kept (pi keeps
  100 ms windows of it); the call is simply made again.
