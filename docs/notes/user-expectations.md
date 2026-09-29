# What users expect of a coding agent

A checklist of what people expect from a terminal coding agent, each item
marked against janetcode. Written 2026-09-29 as a gap analysis; the gaps
were closed on 2026-09-30 (CHANGELOG "User-expectation gaps"). Keep it
current: when an expectation is found that is not here, add it with its
status rather than a TODO elsewhere.

✅ have it · 🟡 partial or not verified · ❌ missing · ➖ deliberately out of scope

## 1. Getting started
| Expectation | Status |
|---|---|
| One-line install, self-update | ✅ `install.sh`, `--update` (SHA256-checked) |
| Guided provider and key setup | ✅ `/connect`, `--init` |
| Setup diagnostics that repair what they can | ✅ `--doctor --fix` |
| Project bootstrap (instruction file) | ✅ `/init`, `--init-project` |
| Uninstall | ✅ `--uninstall` / `--purge`, install.sh receipt |
| Log in with a Claude/ChatGPT subscription | ➖ API keys only, by decision (2026-09-30) |
| Windows native | 🟡 run under wine (tests, live headless turn, bash via `JANETCODE_SHELL`); PowerShell needs the windows CI job |

## 2. Models and providers
| Expectation | Status |
|---|---|
| Anthropic and OpenAI-compatible providers, local models | ✅ |
| Anthropic path verified live | ✅ through a Messages-API broker (docs/notes/anthropic-live.md); 🟡 not api.anthropic.com itself |
| Switch model / effort mid-session | ✅ `/model`, `/effort`, alt+e |
| Fallback and retry | ✅ classified by status, error type and net error |
| Prompt caching | ✅ markers verified on the wire; 🟡 discount unobserved (broker reports no cache usage) |
| Proxy and private CA | ✅ `HTTPS_PROXY`/`NO_PROXY`, `ca_file` / `JANETCODE_CA_FILE` |

## 3. Understanding the codebase
| Expectation | Status |
|---|---|
| Read, search, glob, list | ✅ |
| Repo map, LSP diagnostics and navigation | ✅ |
| Large repositories | ✅ measured on kubernetes (docs/notes/large-repos.md) |
| PDFs, images | ✅ |
| Jupyter notebooks | ✅ `read_file` renders cells, `notebook_edit` |
| Web search and fetch | ✅ resolved-IP guard, egress policy |

## 4. Editing
| Expectation | Status |
|---|---|
| Diff before every change, per-hunk review | ✅ |
| Near-miss edit repair, encodings, stale-read detection | ✅ |
| Protected paths | ✅ |

## 5. Running things
| Expectation | Status |
|---|---|
| Shell with timeout, cancel, background jobs | ✅ |
| Commands that ask questions | ✅ `stdin`, `interactive: true` (pty), prompt detection on Linux, `job stdin` |
| Keep going until green | ✅ `--until`, `--watch`, verify gates |
| Sandbox | ✅ docker / Landlock+seccomp / seatbelt (🟡 seatbelt never run on a Mac); `.git` hooks/config guarded |
| Remote and container workspaces | ✅ `/ssh`, `/docker` |

## 6. Safety and control
| Expectation | Status |
|---|---|
| Permission modes | ✅ ask / plan / auto / yolo / read-only |
| Typed permission scopes | ✅ `cmd:` `read:` `write:` `host:` `touches:` `arg:` `secret:`, `ask` rules |
| Secret redaction | ✅ |
| Project trust levels | ✅ untrusted / instructions / full, fingerprinted |
| Egress control and report | ✅ `/egress`, host policy, enforced for sandboxed bash through a proxy |
| Dry run | ✅ `--dry-run`, `/dryrun`, apply afterwards |
| Audit log | ✅ hash-chained, `--audit`, file changes including bash's (in git) |
| Enterprise managed policy | ✅ `/etc/janetcode/managed.json` |

## 7. Undo and recovery
| Expectation | Status |
|---|---|
| Undo / redo, by change or description, rewind with the conversation | ✅ |
| Checkpoints in the UI | ✅ `/checkpoints`, esc esc, web card |
| Undo outside git | ✅ private shadow repository; line endings byte-exact under autocrlf |
| Worktree isolation, backup/restore | ✅ |

## 8. Sessions and context
| Expectation | Status |
|---|---|
| Resume, continue, fork, title | ✅ (web too) |
| Compaction with retrieval | ✅ |
| Two processes on one session | ✅ OS lease; `--fork` / `--show` for a held one |
| Search across sessions | ✅ `--search`, `/history`, web filter |
| Memory, confirmed memory | ✅ `/remember` entries safe from the model's forget; `memory_confirm` |

## 9. Honesty and verification
| Expectation | Status |
|---|---|
| No unearned success claims; gamed-test detection | ✅ computed verdicts |
| Why did it do that; provenance | ✅ `/why`, `/trajectory`, proof notes |

## 10. Workflow and git
| Expectation | Status |
|---|---|
| Diff, review, security review | ✅ `/review`, `/security-review` |
| Commit with a good message | ✅ `/commit` |
| PRs, PR comments, issues | ✅ `/pr`, `/pr-comments`, `/issue` (🟡 tested with a stub `gh`) |
| CI integration | ✅ `action.yml`, run end-to-end under act (`make action-test`) |
| Scheduled runs | ✅ `/loop`, cron |

## 11. Extensibility
| Expectation | Status |
|---|---|
| MCP tools, resources, prompts, OAuth | ✅ |
| MCP reconnect | ✅ backoff and `/mcp reconnect`, live-tested with a killed server |
| janetcode as an MCP server | ✅ `--mcp-serve` |
| Hooks, skills, custom commands, agents | ✅ |
| Plugins and marketplaces | ✅ Claude Code layout, `/plugin` |
| SDK | ✅ Go `sdk/` over ACP, MCP servers included |

## 12. Interface
| Expectation | Status |
|---|---|
| TUI, web, headless, ACP | ✅ |
| VS Code, JetBrains | ✅ `editors/vscode` (run in real VS Code, `make vscode-test`); JetBrains ACP config documented and handshake-tested |
| Queue / steer mid-turn | ✅ |
| `@file`, `!cmd`, images, clipboard | ✅ |
| Prompt in `$EDITOR` | ✅ ctrl+x ctrl+e |
| Prompt-history search | ✅ ctrl+r |
| Vim mode, custom keybindings | ✅ `/vim`, `keybindings` |
| Notifications | ✅ |
| Accessibility | ✅ `--screen-reader`, web a11y checks |
| Localization | ✅ `language` (answers in any language; interface in es/fr, `/help all` partly) |
| Voice input | ✅ `/voice`, web microphone (transcription live-tested; no real microphone here) |
| Cost, usage, budget | ✅ |
