# janetcode for VS Code

Runs the [janetcode](https://github.com/xmonader/janetcode) coding agent
inside VS Code. janetcode itself must be installed (`janetcode --version`
works in a terminal) and have a provider key.

| command | what it does |
|---|---|
| **janetcode: Open janetcode** (ctrl+alt+j / cmd+alt+j) | the terminal session, in the workspace folder, as an integrated terminal |
| **janetcode: Add Selection to janetcode** (ctrl+alt+k / cmd+alt+k, editor context menu) | types `@path (lines a-b)` into janetcode's input, which attaches the file |
| **janetcode: Add This File to janetcode** (editor and explorer context menus) | types `@path` into the input |
| **janetcode: Open janetcode in a Panel** | starts `janetcode --web` on loopback and shows its browser interface beside the editor |

Nothing is sent until you press enter in janetcode, so you can add to the
request first.

Settings: `janetcode.path` (the binary, default `janetcode` on PATH),
`janetcode.args` (extra flags for the session, such as `["--model",
"qwen3.8-27b"]`), `janetcode.webPort` (the panel's loopback port, default
8797).

The panel frames janetcode's own page. It asks the server for a cookie
that survives being framed (`?embed=1`), which janetcode grants on a
loopback address only; in a remote window the port is forwarded and
stays loopback.

## Install

```sh
cd editors/vscode
npx @vscode/vsce package      # makes janetcode-0.1.0.vsix
code --install-extension janetcode-0.1.0.vsix
```

or, without packaging, link the folder into VS Code's extensions:
`ln -s "$PWD" ~/.vscode/extensions/xmonader.janetcode-0.1.0`.

`npm run check` checks the code with plain node.
