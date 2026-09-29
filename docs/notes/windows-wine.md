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

## Second pass (2026-09-30)

**Undo line endings: fixed.** Every git call that copies content for a
snapshot, an undo, a scratch checkout or an editing subagent's worktree
now runs with `gitx.ByteExactEnv`: `core.autocrlf=false`,
`core.safecrlf=false` and no global attributes file through
`GIT_CONFIG_COUNT`, and `GIT_ATTR_SOURCE` set to the empty tree so a
`.gitattributes` `eol=`/`text` rule cannot convert either (not for
`git apply`: git 2.43's apply segfaults with it set). The shadow store
for non-git directories also ignores the system and global config.
Under wine with MinGit (whose system config sets `autocrlf=true`) the
new byte-exact tests pass for snapshot, worktree and the subagent
worktree, and the `internal/snapshot` and `internal/worktree` tests that
failed before on `"a2\r\n"` now pass. Left failing there: `TestUncovered`
(wine's symlinks) and `TestOpenReplacesAStaleRegistration` (the sharing
violation above).

**The bash tool runs, through busybox-w32.** PowerShell 7 still does
not run under wine, so a shell override was added: `JANETCODE_SHELL`
names a POSIX shell that takes `-c` (Git Bash, or busybox-w32 run as
`busybox.exe sh -c`). With `JANETCODE_SHELL=C:\busybox.exe`
(busybox64.exe from frippery.org):

- `busybox.exe sh -c '…; exit 3'` returns 3 under wine.
- A live `janetcode.exe -p` run against `deepseek/deepseek-v4.1-flash`
  used the bash tool three times: `wc -l calc.txt` (2 lines), `echo DONE
  > out.txt` (the file holds `DONE`), and `exit 7` (reported as exit
  code 7). Exit 0.
- `internal/agent`'s undo, redo, subagent-worktree and task tests, which
  all drive the bash tool, pass under wine with it, apart from
  `TestUndoRevertsATypechange` (wine's symlinks).
- `internal/tool`: bash runs, but 13 tests still fail for test-side
  reasons: POSIX paths (`/etc/passwd` as the outside file), Unix file
  modes, busybox printing `pwd` with forward slashes, and wine's
  symlinks.

To repeat: put `busybox64.exe` in the prefix as `C:\busybox.exe`, then
`JANETCODE_SHELL='C:\busybox.exe' GOOS=windows go test -exec wine ./internal/agent`.
PowerShell itself remains unexercised until the `test-os` CI job runs on
a real `windows-latest`.
