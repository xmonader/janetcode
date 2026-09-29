# Windows under wine (2026-09-29)

No Windows machine was available, so janetcode's Windows build was run
under wine 9.0 (Ubuntu 24.04), with MinGit 2.56.0 for Windows and
PowerShell 7.6.6 (portable zip) in the wine prefix.

## What ran

- `GOOS=windows go test -exec wine ./internal/...`: 42 packages passed
  and 33 failed on the first full run (194 failing tests), before the
  fixes below.
- `janetcode.exe --version`, and a live headless run against an
  OpenAI-compatible endpoint (`deepseek/deepseek-v4.1-flash`):
  `janetcode.exe -p "winelive\add.go: the add function subtracts…"
  --yolo --output-format json`. The model read the file and fixed it
  with `edit_file` (2 tool calls, exit 0), and the JSON result was well
  formed. A path outside the working directory was refused, as it
  should be.

## What wine cannot tell us

- **PowerShell 7 does not run under wine 9.** `pwsh -Command "exit 3"`
  exits 0 with no output, and `pwsh -Version` crashes in
  `ConsoleHost.Start`. Wine's own `powershell.exe` is a stub. So the
  `bash` tool, hooks, verification commands and the status line (all of
  which run through PowerShell on Windows) could not be exercised, and
  every test that runs a command fails under wine for that reason alone.
- **MSYS2's `sh.exe` (MinGit) hangs under wine**, so Git for Windows'
  shell could not stand in either.
- Symlinks, file modes and case folding behave like wine's mapping of
  the Linux filesystem rather than like NTFS.

The `test-os` job in `.github/workflows/ci.yml` runs the suite on
`windows-latest` and `macos-latest`; that is where those paths get
their first real run.

## Fixed from what wine showed

- The command classifier read `/etc/hosts` as a relative path on Windows
  and backslashes as bash escapes, so `echo x > /etc/hosts` and
  `rm -rf C:\Users` were ordinary commands (`internal/shell`).
- Permission subjects were backslashed, so a directory rule saved from a
  prompt (`write_file(docs/*)`) never matched the call it was saved for
  (`internal/perm`).
- A shell write to janetcode's own config by its Windows path did not
  ask (`internal/tool/selfmod.go`).

## Seen and not fixed here

- **Undo through git under Git for Windows' default `core.autocrlf=true`
  restores LF files with CRLF endings** (`internal/snapshot` tests:
  `contents not restored: a="a2\r\n"`; `internal/worktree` scratch
  tests likewise). The snapshot's git commands should run with
  `-c core.autocrlf=false` and without filters so a restore is byte-exact.
- "Sharing violation" removing temp dirs after worktree and subagent
  tests: something keeps a handle or a working directory open on
  Windows. Needs a real Windows run to tell wine from a leak.
- Many tests are written with POSIX paths (`/work/proj`, `/tmp`); in
  `internal/shell` they now skip on Windows, elsewhere they still fail
  there for that reason.
