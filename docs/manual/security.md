# Security and release evidence

This page is what a security reviewer or packager needs: what janetcode defends against and what it does not, what it sends where, how to check that a binary is the one published, and which commands produced the evidence for a release. Every command here is a Makefile target or a `janetcode` flag that runs on your own machine; none of it depends on a hosted CI service. A test (`make release-meta`, also part of `go test ./...`) fails when this page names a gate, a sandbox backend, a toolchain or an outbound host that no longer matches the code.

`janetcode --doctor security` (or `/doctor security`, `/inspect security`) prints the live version of most of this for the machine and configuration in front of you, and never prints a credential.

## Threat model

janetcode is a coding agent: it reads files, edits them and runs shell commands on your behalf, driven by a language model whose output you do not control. The assets are your files, your credentials and your machine; the adversaries are text that steers the model (a malicious file, web page, issue, MCP server or tool result), a repository that ships configuration meant to run when you open it, and a network position between you and a provider.

What stands between a steered model and your machine, in the order a request meets it:

- **Permission rules.** Reads inside the workspace are free; writes and shell commands ask, or match a saved rule such as `bash(go test *)`. Files that hold credentials, and the files that decide trust (the config, `mcp-trust.json`, `extensions.json`), are protected from the agent's own file tools.
- **Project trust.** A repository's `.janetcode/` hooks, MCP servers, skills and instructions are inert until you approve that project at a level, and the approval is recorded per file by content, so a changed hook asks again.
- **Staged extension trust.** An MCP server has three capabilities, `connect`, `read` and `mutate`, enabled one at a time. A newly installed server, and any server declared by a repository, starts with fewer than it needs (`/mcp add` grants connect and read; `/mcp add --from` grants nothing and does not start the server). Disabling a server stops its process and refuses further calls, including through tools a running turn already holds. `/doctor mcp` shows each server's grants without calling a tool.
- **Sandbox (opt-in).** `--sandbox` wraps `bash` in a backend that confines writes (see below).
- **Egress policy (opt-in).** `"egress"` in the config limits the hosts `fetch_url`, `web_search` and package lookups may reach.
- **Secret handling.** Credentials the secret scanner recognises are redacted from tool output and from every diagnostic report; the scanner is pattern based, so an unusual secret format can slip through, and a file the model reads is sent as it is.

What this does not give you, stated plainly: with no sandbox, a command you approve runs with your permissions. An MCP server you grant `connect` is a process that can do anything your user can; the staged grants control what the agent may ask of it, not what the process does. The model sees whatever the file tools and the shell return.

## Sandbox backends and fallback

`--sandbox`, `/sandbox on` or `"sandbox"` in the config wrap local `bash` in one of four backends, chosen for the machine unless you force one:

| backend | where | what it does |
|---|---|---|
| `bwrap` | Linux with bubblewrap installed | mount-namespace confinement: the profile's write roots are the only writable mounts; `strict` drops the network |
| `docker` | any host with a running Docker daemon | runs the command in a container with the workspace mounted |
| `landlock` | Linux 5.13 or later with Landlock enabled | kernel path-based write restriction on the command's process tree |
| `seatbelt` | macOS | `sandbox-exec` profile |

A profile says what is confined: `workspace` (the default for "on": writes only inside the project, extra directories and temp, git hooks and credential files off limits, network allowed), `read-only` (no writes) and `strict` (as `workspace` with git history off limits and no network).

**Fallback.** If you did not ask for a sandbox, `bash` runs on the host. If you asked for one and the machine cannot provide any backend, janetcode does not silently continue unsandboxed: starting with `--sandbox` fails, and `--doctor security` reports `requested-unavailable` and exits non-zero. The report keeps four states apart so nobody reads one as another:

- `enabled`: on, with the backend and profile named.
- `disabled-by-choice`: off because nobody asked for it, on a machine that could run one.
- `unavailable`: off, and this machine has no backend (the report says why for each).
- `requested-unavailable`: asked for, cannot be provided. This is a failed check.

The sandbox wraps `bash` only. The file tools (`read_file`, `edit_file`, `write_file`) always act on the workspace on the host.

## Known limitations

### Windows safety classification

The command classifier that decides whether a shell command is read-only, writes inside the workspace or is destructive understands POSIX shell. On Windows, `cmd.exe` and PowerShell syntax (`del /s`, `Remove-Item -Recurse`, `&` chaining, `^` escapes) are only partly recognised, so a command may be classified as safer than it is and an approval prompt may under-describe it. The Windows build is cross-compiled in every run and exercised under Wine locally; the CI workflow also runs the suite on a Windows runner, but a run on one is not part of the evidence unless `gates.json` says so. There is no Windows sandbox backend. Treat approvals on Windows as the only barrier and read each command.

### Remote workspace trust

`/ssh` and `/docker` retarget the file tools and `bash` at another machine or container. Project trust (hooks, MCP servers, skills, instructions) is recorded for the local project path; it says nothing about the remote tree, whose files can contain instructions the model reads. The sandbox does not follow the retarget: the file tools stay on the host they were pointed at and `bash` runs wherever the session was retargeted. A remote workspace is as trusted as the account you connect with. Do not point a session at a remote tree you would not run a build in.

### Provider-specific guarantees

janetcode sends your prompts, the file contents the model reads and tool results to the provider endpoint you selected, and to the provider of any fallback or routed model. What that provider retains, trains on, or logs is its policy, not janetcode's, and it differs by provider and by plan. Prompt caching, zero-retention modes and regional processing are provider features that janetcode neither enables nor verifies. A provider reached over plain `http://` to a non-local host is reported as a failed check, but janetcode cannot attest what a gateway or proxy between you and the provider does with the traffic.

### Other limits

- A stdio MCP server granted `connect` runs as your user and can do anything your user can; staged trust gates the agent's calls, not the process.
- The seatbelt profile has not been exercised on a macOS machine in this project's test setup; its mach-lookup allowlist is untested.
- The `workspace` profile allows network access and keeps unix sockets, so a sandboxed command can still reach local daemons such as Docker's socket.
- MCP servers contributed by plugins are not covered by the extension report.
- A binary built without VCS information (`-buildvcs=false`, or outside a git checkout) cannot show which source it came from; `--doctor security` says so.

## Telemetry and data retention

janetcode has no telemetry, analytics or crash reporting. It sends no usage data to its authors or anyone else. The only network traffic is what is listed in the endpoint table below, and each row says whether it is on by default and how to turn it off.

Retention is local and manual. Sessions, memory notes and usage records live under the data directory (`~/.local/share/janetcode` on Linux; `janetcode --doctor security` prints the path in use) with no automatic expiry. `janetcode --backup` copies them, `janetcode --uninstall --purge` removes them together with the config directory, and deleting the directories is equivalent. `janetcode --update` checks the downloaded release against its `SHA256SUMS` before replacing the binary. Credentials live in the config file (mode 0600) or the environment, or are referenced by `key_env` or an `op://` 1Password reference so they never touch disk.

### Endpoints

| host | what for | default | how to stop it |
|---|---|---|---|
| the provider you configure (presets below) | model requests: prompts, file contents, tool results | on, required | choose a local provider such as Ollama on `127.0.0.1` |
| `api.openai.com` | OpenAI preset | when selected | select another provider |
| `api.anthropic.com` | Anthropic preset | when selected | select another provider |
| `generativelanguage.googleapis.com` | Gemini preset | when selected | select another provider |
| `openrouter.ai` | OpenRouter preset; the default model for the decision service | when selected | select another provider; `"decisions"` off |
| `api.deepseek.com` | DeepSeek preset | when selected | select another provider |
| `api.z.ai`, `open.bigmodel.cn` | Z.ai and BigModel presets | when selected | select another provider |
| `api.groq.com`, `api.mistral.ai`, `api.x.ai`, `api.together.xyz`, `api.fireworks.ai`, `api.cerebras.ai`, `api.moonshot.ai`, `api.minimax.io`, `dashscope-intl.aliyuncs.com` | other provider presets | when selected | select another provider |
| `opencode.ai` | OpenCode Zen preset | when selected | select another provider |
| `coding-intl.dashscope.aliyuncs.com` | DashScope coding preset | when selected | select another provider |
| `github.com` | the daily update check (latest release tag), `--update`, and plugin and PR commands | update check on in the TUI | `"update_check": false` |
| `raw.githubusercontent.com` | LiteLLM's public model price table, at most once a day, a plain GET | on, not configurable | block the host; costs then show as unknown |
| `proxy.golang.org`, `registry.npmjs.org`, `pypi.org` | looks up a package the model is about to install | on, under the egress policy | `"egress"` policy, or deny the hosts |
| `registry.modelcontextprotocol.io` | `/mcp search` | only when you run it | do not run it |
| `html.duckduckgo.com`, `google.serper.dev`, `api.search.brave.com`, `api.exa.ai`, `api.tavily.com` | the `web_search` backends, each reached only when selected (and keyed, except duckduckgo) | duckduckgo when `web_search` is used | `"egress"` policy, or remove the tool |
| any URL the model passes to `fetch_url` | fetching a page | when the tool is called, subject to permission rules | `"egress"` policy |
| any MCP server URL you configure | remote MCP | when connected | `/mcp disable <name>` |

Two more strings that look like hosts are never contacted: `in-toto.io` and `janetcode.dev` appear only as identifiers inside the signed proof notes' attestation format. `api.ai02.ourhero.ai` is the default upstream of the `effbench` developer tool, which is not part of the `janetcode` command.

## Verify a release on a clean machine

A release directory holds the binaries (`janetcode-<version>-<os>-<arch>`), `SHA256SUMS`, optionally `SHA256SUMS.asc`, `provenance.json`, `sbom.spdx.json`, `licenses.json`, `THIRD-PARTY-LICENSES.txt`, `gates.json` and `RELEASE-EVIDENCE.md`.

With nothing but a shell and the release directory:

1. Check the files against the list: `sha256sum -c SHA256SUMS` (macOS: `shasum -a 256 -c SHA256SUMS`). Every line must say `OK`.
2. If `SHA256SUMS.asc` is present, import the maintainer's public key from a source you trust and check the signature: `gpg --verify SHA256SUMS.asc SHA256SUMS`. Compare the key fingerprint it prints with the fingerprint published out of band. A valid signature by an unexpected key is not a verification.
3. Check the binary reports what the provenance claims: `go version -m janetcode-<version>-<os>-<arch>` prints the source commit (`vcs.revision`), `vcs.modified=false`, `-trimpath=true`, `CGO_ENABLED=0` and every module linked in. The revision must equal `commit` in `provenance.json`. (`go version -m` needs only a Go toolchain; it does not run the binary.)
4. Optionally rebuild and compare, which proves the published source produces the published bytes: with Go 1.25.4 installed, `git clone https://github.com/xmonader/janetcode && cd janetcode && git checkout <commit> && make dist VERSION=<version>`, then `sha256sum dist/janetcode-*` against `SHA256SUMS`. The build uses `-trimpath` and `CGO_ENABLED=0`, so the bytes do not depend on the directory or machine.

Where a checkout and Go are available, one command does steps 1 to 3 and the cross-checks between the files: `make verify-dist`. It fails on a mismatched sum, a binary that is not in `provenance.json`, a file not covered by `SHA256SUMS`, a binary built from a modified tree, or a bad signature (`make verify-dist SIGNER=<fingerprint>` additionally requires a signature by that key).

## Producing the evidence

Run from a clean checkout of the commit being released, in this order:

- `make dist VERSION=vX.Y.Z` builds the five binaries (linux and darwin on amd64 and arm64, windows on amd64) with Go 1.25.4, `-trimpath` and `CGO_ENABLED=0`.
- `make release-evidence VERSION=vX.Y.Z` writes `licenses.json` and `THIRD-PARTY-LICENSES.txt` (inventory from each linked module's own licence file, failing on a copyleft or unrecognised licence), `provenance.json` (commit, toolchain, flags, each artifact's digest; refuses a binary built from a different commit or a modified tree), `sbom.spdx.json` (SPDX 2.3, the modules actually linked into the binaries, with package URLs), `RELEASE-EVIDENCE.md`, and a fresh `SHA256SUMS` over everything.
- `make release-gates` runs every gate below and records the command, the commit and the result of each in `gates.json`.
- `make release-sign GPG_KEY=<key>` writes `SHA256SUMS.asc`. Signing is optional; without it the sums are integrity but not authenticity evidence.
- `make verify-dist` checks the result.

Dependencies come from `go.mod` and `go.sum`; the SBOM is derived from the built binaries' embedded module list, not from a second description of them, so it cannot list a module that was not linked.

### Release gates

Each gate is one command. `make release-gates` runs them in this order and writes the commit it ran on beside each result; a gate whose program is missing (`node`, a Chrome) is recorded as skipped with the reason, never as passed. The same table lives in `cmd/releasepkg/gates.go` and a test keeps this list identical to it.

| gate | command | what it proves |
|---|---|---|
| unit | `go test ./...` | every package's tests, including the dependency-direction and release-metadata drift tests |
| race | `go test -race ./...` | the same suite under the race detector |
| lint | `make lint` | go vet, and staticcheck when it is installed |
| cross | `make cross` | build for windows/amd64 and darwin/arm64, which the Linux run only compiles |
| security | `go test ./internal/shell ./internal/perm ./internal/secret ./internal/extension ./internal/secreport ./internal/trust ./internal/egress ./internal/archtest` | the sandbox, permission, secret-scanning, extension-trust, security-report, trust-store, egress and architecture tests on their own |
| security-doctor | `make build && ./bin/janetcode --doctor security` | the built binary's own security report; it exits non-zero when a check fails |
| ui | `make ui-test ui-smoke` | the web UI in headless Chrome: interactions and a console-error smoke test |
| ui-visual | `make ui-visual` | pixel comparison of every web UI state with the committed baseline |
| reproducible | `make repro-check` | rebuild the release binaries from a fresh clone with an empty build cache and compare them byte for byte |
| supply-chain | `make release-evidence verify-dist` | SBOM, licence inventory and provenance generated from the binaries, then verified against SHA256SUMS |

The release evidence for a given version names the commit in three places that must agree: `provenance.json` (`commit`), `gates.json` (`commit`) and the `vcs.revision` embedded in each binary. `make verify-dist` checks the first and third; `RELEASE-EVIDENCE.md` prints the second beside the gate table.

## What `--doctor security` reports

`janetcode --doctor security [--output-format json]` and `/doctor security [json]` report, without printing any credential (a key is described by where it comes from, `config file`, an environment variable's name or `1Password reference`, never its value; URL userinfo and token-bearing query parameters are removed):

- **Binary**: version, source commit and whether the tree was modified, build flags (`-trimpath`, CGO), toolchain, module count, and the executable's sha256 to compare with `SHA256SUMS`.
- **Sandbox**: the four states above, every backend with why it is or is not available on this machine, and what the sandbox does not cover.
- **Trust**: the project approval store, extension grants and MCP login store, with a failure when one is writable by others or unreadable.
- **Providers**: each configured endpoint, where its key comes from, a failure when a key would be sent over plain http to a non-local host, and a warning for a key stored in clear text.
- **MCP and hooks**: user-global and project-local servers counted separately, with trust and capabilities; `--doctor mcp` has the per-server detail.
- **Privacy**: telemetry (none), the update check, price table, egress policy, search backend, decision service and retention.

It exits non-zero when any check is a failure; warnings are choices and limits worth knowing, not faults.
