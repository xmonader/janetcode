# Session protocol: several clients, one session

Part of the [janetcode manual](README.md). [Sessions](sessions.md) covers
the files; this page is the contract between the clients of one session and
the process that runs it. The same session can be open in a terminal, in
browser pages, in an editor over ACP, in a `--bg` run and in a script, and
none of them may produce a duplicate turn, a lost approval or a setting
changed under a running turn. The protocol is **version 1**
(`sessionproto.Version`), implemented in `internal/sessionproto` (which
imports nothing from janetcode) and spoken by `--web`, `--acp` and
`--follow`.

## Two levels of ownership

| Level | Who holds it | How it ends |
|---|---|---|
| Process lease | the one janetcode process that runs turns in the session: a terminal, `--web`, `--acp`, a `--bg` run | the kernel drops the OS lock on `<id>.lock` when the process exits, however it exits, so a crash never leaves a session locked |
| Control lease | the one client of that process that may change the session | released, taken over at a turn boundary, or lapsed after 15 seconds without being heard from |

The process lease is documented in [sessions](sessions.md#one-process-per-session).
A second process is refused (`*session.LeasedError`) and told who has the
session; it can continue in a copy (`--fork`), or watch it read-only
(`--follow`, below). The rest of this page is the control lease inside the
process that serves several clients: today the web server.

## The terminal interface is not a lease client

The TUI is one user at one keyboard in one process, so it has no control
lease and no observers: it needs no arbitration inside its own process.
What protects its session is the process lease alone: while the terminal
holds a session, `--web`, `--acp`, `--resume` and a second terminal are
refused with the holder named, and `--follow` can watch it read-only.
`--web` is the process that serves several clients and so is the one that
runs this page's control lease. Its own prompt queue keeps the same
first-in first-out order as the others.

## Roles

- **controller**: may change the session. At most one at a time.
- **observer**: attached read-only. Any number. Receives every event the
  controller's page does and can change nothing until it takes control.
- A client that is neither is **undecided**: the first change it asks for
  makes it the controller when nobody is, so a plain client that never
  heard of leases works until a second client appears.

A *change* is every `POST`, `PUT`, `PATCH` or `DELETE` under `/api/` except
`/api/mermaid` (it only renders a diagram) and `/api/control` (it checks for
itself): a prompt, steering, cancel, a permission or question or secret
answer, a slash command, connecting a provider, a new, loaded or forked
session, file upload, rewind, workspace changes. Every `GET` is open to
every client, observers included. `/guest/<token>/...` is a separate
credential and not a client of this protocol.

## Naming a client

The server reads, in this order:

1. the `X-Janet-Client` request header;
2. the `client` query parameter (an `EventSource` cannot set headers);
3. the `janet_client` cookie the page `/` hands each browser, so a browser's
   tabs are one client and another browser or device is another;
4. otherwise the client is `anonymous`: every unnamed script is one client.

A client id is up to 64 characters of `A-Za-z0-9._:-`; anything else counts as
no id. `X-Janet-Client-Name` (or `name`) is a label shown to the others
("the phone"), at most 64 characters.

## Headers

| Header | Direction | Meaning |
|---|---|---|
| `X-Janet-Protocol` | both | The version. A client may send it; a server answers every `/api/` and `/events` response with the version it speaks. A client that names another version gets `400` with code `bad_version`. Omitted means "whatever you speak". |
| `X-Janet-Epoch` | request | The lease epoch the client believes it acts under. Optional. A mismatch is `stale_lease`. |
| `X-Janet-Client`, `X-Janet-Client-Name` | request | Identity, above. |

## The control state

Every client can read it: `GET /api/control`, the `control` field of
`GET /api/state`, the `control` field of the `hello` event, and `control`
events. It is a *snapshot*:

```json
{
  "protocol": 1,
  "epoch": 7,
  "holder": {"client": "web-1a2b", "name": "the phone", "since": "...", "expires": "..."},
  "observers": 2,
  "you": "observer",
  "busy": true,
  "queued": 3
}
```

`epoch` counts grants of the control lease and only goes up; `you` is the
asking client's own role (`controller`, `observer` or `none`); `busy` says a
turn runs; `queued` is how many prompts and settings wait behind it.
`holder` is absent when nobody controls the session.

## Changing who controls

`POST /api/control` with `{"action": "..."}`:

| Action | Succeeds when | Otherwise |
|---|---|---|
| `acquire` | nobody holds the lease, it lapsed, or the caller holds it | `409 lease_held` |
| `take` | as `acquire`, or the holder is idle: **a handoff happens at a turn boundary** | `409 turn_running` while a turn runs |
| `release` | always; it releases only if the caller holds the lease | |

Success returns the snapshot. Taking control ends an observer's read-only
status.

## Conflicts

A refused request gets `409` (`400` for `bad_version`) with
`Content-Type: application/json`:

```json
{"error": "this session is controlled by the phone",
 "conflict": {"code": "lease_held", "message": "this session is controlled by the phone", "state": { ...snapshot... }}}
```

`state` is the snapshot as the refused client sees it: what it should
re-read before acting again. Codes:

| Code | Meaning |
|---|---|
| `lease_held` | another client controls the session and its lease is live |
| `stale_lease` | the client acted under an epoch that is over: its lease lapsed, was taken over, or the session changed |
| `observer_readonly` | the client attached as an observer |
| `turn_running` | a handoff was asked for mid-turn |
| `bad_version` | the client speaks another protocol version |

## The lease lapses; it is never permanent

The holder's lease runs 15 seconds (`Options.ControlTTL`) and is renewed by
any change it makes and, every 500 ms, by its open `/events` stream. So:

- a controller whose tab is open keeps the session, idle or not; another
  client takes it with `take` once the turn ends;
- a controller that disconnects, crashes or loses the network lapses 15
  seconds later, and the next client to ask becomes the controller;
- a controller that comes back after that is a stranger: its next change is
  `lease_held` (someone else holds it) or, with its old epoch, `stale_lease`.

A turn the departed controller started keeps running to its end, and the
queue behind it runs; only *new* changes need a new holder. The process
dying frees the process lease through the kernel, as above.

## Events

`GET /events` is a server-sent-event stream of JSON envelopes
`{"seq": n, "type": "...", ...}`. `?role=observer` attaches it read-only;
`?client=<id>` names the client; `?since=<seq>` skips the replay of events
the client already has (the default replays the whole log, up to the last
1500 to 2000 events, which is how a reconnecting page catches up). The
stream sends:

- `hello` first, with the session's identity and the `control` snapshot;
- the logged events after it, in order (`entry`, `status`, `permission`,
  `question`, `secret`, `notice`, `error`, ...);
- `status` every 500 ms with the running phase and queue length;
- `control`, whenever the snapshot as *this client* sees it changes (a
  handoff, a lapse, an observer arriving, a turn starting or ending);
- `bye` when the server shuts down. A client should not retry into it.

Observers see everything the controller's page does, including permission
requests (they cannot answer them).

## Ordering

- **Prompts** queue first-in first-out behind the running turn. A turn
  that fails drops what was queued behind it, since it was queued behind
  work that did not happen. The web page, the terminal's own queue and ACP
  (below) keep the same order; each is tested.
- **Settings** that change how later turns run (`/effort`, `/routing`,
  `/context-window`, `/config`, `/add-dir`, `/sandbox`, `/dryrun`) sent
  during a turn wait in the same queue and apply when the turn ends, in the
  order they came, between the prompts around them. Commands that change
  the model or provider, the directory, or the session itself (`/model`,
  `/connect`, `/cd`, `/resume`, `/fork`, `/clear`, `/undo`, ...) are refused
  while a turn runs. The permission switches (`/yolo`, `/auto`, `/edits`,
  `/readonly`, `/afk`) take effect at once, because stopping or loosening a
  running turn is what they are for.
- **Cancel, approvals, questions and secrets** act on the session's single
  running turn and are changes, so only the controller makes them.
- **A new, loaded, resumed or forked session** ends the control lease: the
  epoch advances and every client's old lease is stale. An approval, an
  undo, a cancel or a prompt sent under the old epoch is refused with
  `stale_lease` and reaches nothing of the new session.
- **Subagents belong to the session that started them.** Moving to another
  session cancels the old session's background subagents (their attempt
  records say `cancelled`, in the old session) and forgets its continuable
  children. A subagent that still ends afterwards, because its model call
  could not be interrupted, reports to the old session's log only: never to
  the new session's history, event stream or page. Over ACP each session
  has its own agent, and every update a turn makes carries its own session's
  id, however many sessions the connection has opened since.

## ACP

`janetcode --acp` is one process serving one editor, so the editor is the
controller. It adds:

- `initialize` answers with `_meta.janetcode.sessionProtocol` (1);
- prompts for one session run in the order they were read, one at a time;
  `session/cancel` ends every prompt read before it, running or queued;
- `session/new` and `session/load` of a session another janetcode runs fail
  with JSON-RPC code `-32010` and
  `data: {"protocol":1,"code":"session_leased","session":"<id>","holder":{"pid":...,"host":"...","holder":"the terminal interface","since":"..."},"options":["fork","follow"]}`,
  so an editor can offer a copy or `--follow` without parsing the message.

## Following a session from another process: `--follow`

```
janetcode --follow <session-id> [--output-format stream-json]
```

An observer for a session another janetcode runs, whatever it is (a
terminal, `--web`, `--acp`, a `--bg` run). It never takes the process
lease: it reads the session file the owner appends to, and the `--bg`
status file beside it. It prints who holds the session, then each event as
it is appended, and ends when the holder is gone, whether it finished or
was killed (the kernel releases the lease with the process). A session
nobody holds is printed and the command ends. Ctrl+C stops it at any time.

With `--output-format stream-json` each line is one object, always with
`"protocol": 1` and a `kind`:

| `kind` | Fields |
|---|---|
| `lease` | `session`, `held`, `role` (`observer`), `holder` (`pid`, `host`, `holder`, `since`) when held |
| `event` | `event`: a session event as in the session file (`seq`, `t`, `turn`, `message`, `name`, `arguments`, `output`, ...). A `follow/reset` event means the owner rewrote the file (a rewind or compaction); the whole log follows it again |
| `bg` | `status`: the run's `.bg.json` (`state`, `ask`, `question`, ...) each time it changes |
| `end` | `reason`: `owner_gone`, `idle` or `interrupted` |

## Versioning

The version is an integer. Within a version, fields and codes are only
added: a client ignores keys it does not know, and a server never changes
the meaning of one it sent. A change that could break a client (a removed
field, a changed code, a changed rule about who may change what) is a new
version, and the server then refuses clients that name the old one with
`bad_version` instead of guessing.
