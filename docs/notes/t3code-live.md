# The t3code features, run against real models (2026-11-16)

The t3code round (render_html, request_secret, delegates, notification
deep links) was unit and UI tested only. Each was run for real with a
scratch `XDG_CONFIG_HOME`/`XDG_DATA_HOME`. Model for janetcode itself:
`deepseek/deepseek-v4-flash` through OpenRouter (`--provider openai`
with `OPENAI_BASE_URL=https://openrouter.ai/api/v1`), plus `glm-5`
(`zai-coding-plan`) for the first diagnosis. No keys are in this file.

## Bug found: a tool loaded with tool_search was not callable in the same turn

`render_html` is a deferred tool, so the model has to load it with
`tool_search`. Observed with deepseek-v4-flash and glm-5, `-p`:

    [tool] tool_search {"query":"render_html"}
    [tool ok] Loaded 1 tools; they are callable from now on: ## render_html ...
    [tool] tool_search {"query":"render_html"}
    [tool ok] render_html: already loaded and callable; its schema is in your tool list.
    ...
    [notice] turn stopped: the model repeated the same tool call 5 times

A logging proxy in front of the provider showed the cause: the request
carried `['bash','edit_file','read_file','tool_search','write_file']` on
every call of the turn. `Agent.turnTools` froze the list at the turn's
start, so in `-p` (one turn) the loaded tool was never sent, and the tool
result's "its schema is in your tool list" was false. deepseek wrote the
chart to a script file instead. Fix: the list is rebuilt when the
registry's activation count changes (`Registry.Activations`); MCP tools
arriving mid-turn still wait for the next turn. Regression test:
`TestToolSearchLoadReachesTheNextRequestOfTheTurn` (fails without the fix).

## Minor bug: `-p` did not print the page card

The manual says `-p` prints `▣ title · path`. It printed the raw result
clipped to 300 characters. Fixed in `headless.printEvent`; test
`TestRenderHTMLResultPrintsTheCardInHeadlessRuns`.

## render_html

- `-p`, after the fix: `[tool] render_html {"title": "Quarterly Sales", ...}`,
  result `Page saved at .../sessions/20261116-135714-e3d24c.page-1.html (page 1), titled "Quarterly Sales"`;
  the file and `.page-1.json` sit beside the session `.jsonl`.
- `--web` (token `tok123`, loopback), prompt via `POST /api/prompt`: the
  model called `render_html` directly; `/api/state` has a `page` entry
  (`{"sid":"20261116-135816-d75ce3","n":1,"title":"Quarterly Sales"}`).
  `GET /page/<sid>/1?theme=dark` with the cookie: `200`, `Content-Type:
  text/html; charset=utf-8`, `Content-Security-Policy: sandbox allow-scripts
  allow-popups allow-popups-to-escape-sandbox; default-src 'none';
  script-src 'unsafe-inline' https:; ... connect-src 'none'; form-action
  'none'; base-uri 'none'; ...`. Without the cookie: `401`.

## request_secret (TUI under a pty, pyte frames, OSC 11 and DSR answered)

Prompt asked for secret `X`, then `printf %s "$JANETCODE_SECRET_X" | wc -c`
and `echo "value=$JANETCODE_SECRET_X"`. A 21 character dummy was typed.

- Masked frame: `│ › •••••••••••••••••••••` (21 bullets); the typed text
  appears in no frame and not in the raw pty bytes.
- `bash printf %s "$JANETCODE_SECRET_X" | wc -c` -> `21`: the command saw it.
- `bash echo "value=$JANETCODE_SECRET_X"` -> `value=[redacted]`.
- `grep -rl <value>` over the scratch data dir, config dir and HOME: no hits
  (session `.jsonl`, audit, undo, history all clean).

## delegate (web, default mode, real `claude` 2.x and `codex` CLIs)

`delegates.enabled` in the scratch config; HOME is the real one (the CLIs
sign in from it; with a scratch HOME claude answered `Not logged in`).
The permission prompt appeared for each call
(`APPROVAL PROMPT: delegate | {"agent":"claude","prompt":"How many .go files are in internal/delegate? ...`).

- claude: `2` then `[delegate claude · 8.8s · cost $0.1727 · 62959 in / 134 out tokens]`.
  The cost is the CLI's own prompt overhead, not the task.
- codex: `2` then `[delegate codex · 9.3s · 35183 in / 57 out tokens]`.
- codex in a directory that is not a git repo fails with `Not inside a
  trusted directory and --skip-git-repo-check was not specified.` The error
  reaches the model verbatim; the default args are unchanged (the check is a
  safety feature; override `args` to skip it).
- Known gap confirmed: the status line cost after the claude call was
  `$0.000683`, janetcode's own model only; delegate cost is not added.

## Notification deep link

`--web` with `hooks.notification` = `curl -s -X POST --data-binary @-
http://127.0.0.1:18877/hook` and a local Python listener (no external
service). The real model asked to run `echo hello-from-bash` in ask mode.
Captured stdin JSON, trimmed:

    {"notification_type":"permission_prompt","message":"janetcode needs your permission to use bash",
     "url":"http://127.0.0.1:18831/?approve=1&ticket=5f0427ce0d70046d9ab04fb9b1b8f230", ...}

(an `idle_prompt` notification earlier carried `.../?ticket=b5b3...` with no
`approve`). Opening the permission URL with curl:

- first: `HTTP/1.1 303 See Other`, `Location: /?approve=1`,
  `Set-Cookie: janetcode_4f6e1f65=...; Path=/; Max-Age=2592000; HttpOnly; SameSite=Strict`;
  the cookie then returns `200` on `/api/state`.
- second: `HTTP/1.1 401 Unauthorized` and the page text `That link was already
  used or has expired.`

## Not verified here

Phone approval over `tailscale serve` (the machine is not on a tailnet), a
real `op read`, an ntfy topic, the browser rendering of the page iframe
(covered by `make ui-test`, not rerun here), `request_secret` in the web
UI against a real model.
