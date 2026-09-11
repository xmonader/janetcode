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

### Project instructions

If the working directory contains `JANETCODE.md`, `AGENTS.md` or `CLAUDE.md`
(first found wins), its contents are added to the system prompt.

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
