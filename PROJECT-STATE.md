# janetcode — project state

## Current State
- Done: Go terminal coding agent. Providers: Anthropic (official SDK, adaptive thinking, effort, prompt caching) and OpenAI-compatible (hand-rolled, streaming, tool calls, dialect fallbacks). Tools: read_file, write_file, edit_file, list_dir, glob, grep, bash (path-confined, diff previews, non-interactive env, credential scrubbing). Agent loop with permissions (allow / deny / allow-for-session), parallel read-only tools, automatic and manual context compaction. JSONL sessions with resume, listing, rotation on compaction. Bubbletea TUI with streaming markdown, coloured diffs, prompt history, /commands. Headless `-p` mode with piped stdin. All tests offline against fake servers; `make race` green.
- Next: real-API verification (no key available in the build environment), then cost display per model, dangerous-command highlighting for bash, MCP client, sub-agents, web fetch.
- Blocked: nothing.

## Key Decisions
- Go + bubbletea; product name is janetcode, not the Janet language (2026-09-07).
- Anthropic via official SDK per policy; OpenAI-compatible hand-rolled to tolerate dialects (Qwen, DeepSeek, vLLM, Ollama, OpenRouter) and learn quirks from 400s at runtime (2026-09-07).
- Compaction instead of in-place history pruning: Fable 5.1 preserved-thinking rejects edited history, so the harness is append-only and compaction starts a fresh history (2026-09-07).
- Tool results for a turn always go back in one user message; history stays provider-valid on every return path, including cancellation (2026-09-07).
- Sessions rotate to a new file when history shrinks so pre-compaction transcripts are never overwritten (2026-09-07).

## Architecture Notes
- `internal/llm` is the provider-neutral core: Message/Block, ToolDef, Request, Event, Provider.Stream(ctx, req, emit) → Response.
- `internal/agent.Agent.Run` is the loop; `Approver` is the permission hook; `EstimateTokens`/`Compact` handle context size.
- TUI never touches agent state while a turn runs; the agent goroutine sends `agentEventMsg`/`agentDoneMsg`/`permissionMsg` through `model.send`.
- Terminal background is detected once before the program starts (`lipgloss.HasDarkBackground`); creating glamour renderers with AutoStyle later would re-query the terminal and eat keystrokes.
- Testing a TUI under a real pty: `scratchpad/smoke/drive.py` pattern — reply to OSC 11 (`\e]11;rgb:…\e\\`) and DSR (`\e[1;1R`) or bubbletea/termenv blocks for 5 s.
