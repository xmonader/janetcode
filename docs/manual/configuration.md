# Configuration

Part of the [janetcode manual](README.md); the [README](../../README.md) has the short version.

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
  "hints": true,
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

### What janetcode records about you

Two small files in the config directory, and nothing else:

- `hints/<project>.json` — the ids of the hints already given for one
  project, so each is given once.
- `usage.json` — a map from feature name to how often you have used it,
  written once when a session ends. It is what lets the hints skip the
  features you already know and `/stats` name up to five you have never
  tried.

Neither leaves the machine. There is no endpoint, no identifier and no
timestamp; nothing in janetcode reads them except the hint rules and
`/stats`, and deleting them costs nothing but a few repeated hints. The
record of *what you did* — the conversations, the tool calls, the costs —
is the session log, which is also local and which `/history` and
`/resume` read.

### Hints

`"hints": false` turns off the one-time notes about features a session
could have used. They are on by default, and there are nine of them: the
verification gate after the third changing turn or the second time you run
the tests yourself with `!`, the `r` key after the third refusal of the
same shape of call, `ctrl+g` after a command holds a turn for twenty
seconds, `/context` and `/compact` when the window crosses 60%, `@path`
after a large paste or a pasted stack trace, `/usage` when a session
crosses $0.50, the trust command for a project file that is present but
not enabled, the `prices` key for a model nothing can price, and
`/context` on a session resumed after a compaction.

Each fires at most once per project, ever, and never while you are
answering a permission prompt or reading a picker. The record of what has
been said is a small JSON file under the config directory, keyed by
project — never in the project itself. `/hints off` silences them for one
session, `/hints on` brings them back, and `/hints` alone says how many
have been given.

### Model fallback

`fallback_models` is an ordered list (at most three are used, duplicates and
the model in use dropped) tried when a call fails in a way another model
could survive: an exhausted quota or plan, or rate limits and server errors
that outlived the provider's own retries. A bad request, a context overflow
or a cancellation fails as it always did — another model would fail the same
way. Each name is routed like any other, so a fallback may live on a
different provider. The partial answer from the failed attempt is discarded,
the conversation is resent whole, and the model that works becomes the
session's model (`/model` switches back).

```json
{ "model": "qwen3.7-plus", "fallback_models": ["glm-5", "claude-opus-5"] }
```

### Stream watchdog

A streamed model call can stop producing without ever failing: the socket
stays open and the turn waits forever. Two bounds end it. `stream_idle_timeout`
(seconds, default 240) is how long the stream may produce nothing; every
increment resets it, so a slow model that keeps talking is never cut off.
`stream_max_duration` (seconds, default 900) is the longest one call may
take however steadily it streams, and nothing resets it. Either one ends
the turn with a normal failure you can resend; set a bound negative to
disable it.

```json
{ "stream_idle_timeout": 240, "stream_max_duration": 900 }
```

### Hook limits

A hook is a shell command on the critical path of a tool call whose output
is pasted into the conversation, so what hooks may cost is bounded, and the
bounds are yours to set. They live in the `"hooks"` object beside the hooks
themselves, under `limits`: `timeout` (seconds a hook that names no timeout
of its own may run, default 10), `project_timeout` (the most a hook from
`.janetcode/hooks.json` may ask for, default 60), `output` (bytes kept of
one hook's stdout and stderr, default 65536) and `per_event` (hooks run for
one event, default 20).

```json
"hooks": {
  "post_tool": [{"command": "gofmt -l ."}],
  "limits": {"timeout": 5, "output": 8192}
}
```

A key left out, or zero, means the default; a negative figure is refused on
load (exit 52). A `limits` object in a project's hook file is ignored, with
a note: trusting a checkout's hooks is not a decision to let it choose how
long they may run. The hooks themselves, and what they can do, are
described under Hooks in the interface manual.

### Hooks and the network

A hook from `.janetcode/hooks.json` may not reach the network: trusting a
checkout's hooks is a decision about this checkout, not a decision to hand
the repository everything you can reach. Your own hooks may, and a project
that needs one says so in its README for you to copy across.

A hook says which it is with `"network"`:

```json
"hooks": {
  "post_tool": [
    {"command": "curl -s https://ci.example/notify", "network": true},
    {"command": "grep -rn \"https://\" .", "network": false}
  ]
}
```

`true` is a declaration that the command goes off the machine, and a
project hook that carries it is refused by name. `false` says it does not,
and a project hook that carries it runs even though its command text looks
like it might. Leave the key out and the command text is guessed at
instead, which is what happened to every hook before the key existed: the
guess reads `curl`, `wget`, `ssh`, `rsync` and a URL as reaching out, so it
refuses a `grep` for a URL and cannot see a python one-liner that opens a
socket. Say what the hook does and the guess is not consulted.

### Project instructions

`JANETCODE.md`, `AGENTS.md` or `CLAUDE.md` (first found in a directory
wins) is read from every directory between the repository root and the
working directory, farthest first, so what the repository says about
itself and what a particular package says about itself both reach the
model, with the most specific one last. Without a repository only the
working directory is read: there is no boundary to walk up to, and
walking to the filesystem root would read whatever a home directory
happens to hold.

A line that is nothing but `@path` pulls another file in where it stands,
which is how one standard is kept in one place and referred to from
several. Paths resolve against the importing file's directory, imports are
followed at most five deep, a file is included once per chain, and a path
outside the project is skipped with a note saying so. An `@` in the middle
of a sentence, or inside a fenced block, is prose.

### Path-scoped rules

Anything in the system prompt is paid for on every request of every
session, which is why project instructions have to stay short — and why
the things worth saying about one corner of a repository usually go
unsaid. A rule says them where they belong. Put a markdown file in
`.janetcode/rules/` (or `rules/` under the config directory, or
`.claude/rules/`) with a `paths:` list in its frontmatter:

```markdown
---
description: migrations are append-only
paths: db/migrations/**, *.sql
---
Never edit a migration that has been applied. Add a new one instead, and
keep the down migration in the same file.
```

Nothing is sent until a tool call touches a matching path; then the rule
is added to the conversation once for the session, as harness text — so
it costs nothing in the sessions and turns it does not apply to. A rule
with no `paths:` is ignored: instructions that always apply belong in
`JANETCODE.md`, where their cost is visible. Globs are matched against
the path the call named, `*` spans anything including `/`, and a glob
without a `/` is matched against the file name too. `/rules` lists what
is defined, what brings each in, and which have been sent this session;
`janetcode --inspect rules` prints the same outside the UI.

## Themes

The `"theme"` key selects the palette: `auto` (detect the terminal
background, the default), `dark`, `light`, or the name of a builtin or a
theme you define. The builtins are `default`, `solarized-dark`,
`solarized-light`, `high-contrast`, `muted`, `mono`, `gruvbox` and `nord`;
`janetcode --inspect themes` lists them, [interface.md](interface.md)
describes them, and `/theme` switches between them live.

A theme names fourteen colour roles — `accent`, `dim`, `faint`, `warn`,
`err`, `ok`, `user`, `tool`, `head_bg`, `head_fg`, `border`,
`border_active`, `diff_add`, `diff_del` — plus a `gutter` (the transcript's
left margin, default 2) and whether it is `dark`. A role's colour is one
string used in both terminal backgrounds, or a `{"light":…, "dark":…}`
pair when it should adapt. A colour is a `#rrggbb` (or `#rgb`) hex or a
`0`–`255` ANSI palette index.

### User themes in the config file

The `"themes"` object maps a name to a theme. Give it a `"base"` and it
inherits every role you do not set, so a tweak is two lines:

```json
{
  "theme": "warm-night",
  "themes": {
    "warm-night": {
      "description": "gruvbox with a brighter prompt caret",
      "dark": true,
      "base": "gruvbox",
      "gutter": 2,
      "roles": {
        "accent": "#fabd2f",
        "border_active": "#fabd2f"
      }
    }
  }
}
```

A theme with no `base` must fill all fourteen roles itself. A bad theme is
a configuration error at startup (exit 52), naming the theme and the field:
an unknown role key, a colour that is neither hex nor an ANSI index, an
unknown base, or a missing role after inheritance. A user theme may share a
builtin's name to override it.

### Theme files

A theme can also live in its own file at `<config-dir>/themes/<name>.json`
(the config directory is the one holding `config.json`, honouring
`XDG_CONFIG_HOME`). The file is the theme object without the outer name —
the file name is the theme name:

```json
{
  "description": "my paper theme",
  "dark": false,
  "base": "solarized-light",
  "roles": { "accent": "#1c72b8" }
}
```

Files are loaded at startup and validated the same way; a name defined in
the config `"themes"` object wins over a file of the same name.

## Prices

`/cost`, the turn summary and the eval reports price tokens from three
sources, in this order: `"prices"` entries in the config file; the price a
provider's model listing carried when the model was picked with `/model`
(OpenRouter lists them); and LiteLLM's public rate table, fetched into
`model-rates.json` beside the config file on the first run with network
and refreshed after a day. The table is the one `ccusage` prices against.
A figure from any of these is an API-equivalent estimate at list price and
is labelled so; only a cost the provider itself reported with the response
(OpenRouter does) is shown as reported. `janetcode --doctor` says which
table is in use and how old it is.
