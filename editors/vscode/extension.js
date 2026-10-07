// janetcode for VS Code: the terminal session in an integrated terminal,
// the current file or selection sent to it as context, and the browser
// interface (janetcode --web) in a panel. No build step: plain CommonJS.
"use strict";

const vscode = require("vscode");
const cp = require("child_process");
const http = require("http");
const { mention, selectionLines, newToken, webURL, panelHTML } = require("./lib");

const TERMINAL_NAME = "janetcode";
let web = null; // { proc, port, token, panel }

function config() {
  const c = vscode.workspace.getConfiguration("janetcode");
  return { bin: c.get("path") || "janetcode", args: c.get("args") || [], port: c.get("webPort") || 8797 };
}

function workspaceRoot(uri) {
  const folder = (uri && vscode.workspace.getWorkspaceFolder(uri)) || (vscode.workspace.workspaceFolders || [])[0];
  return folder ? folder.uri.fsPath : undefined;
}

// terminal returns the janetcode terminal, starting one in the workspace
// when there is none. janetcode is the terminal's process itself, not a
// command typed into a shell, so closing it closes the terminal.
function terminal(uri) {
  const existing = vscode.window.terminals.find((t) => t.name === TERMINAL_NAME && t.exitStatus === undefined);
  if (existing) return { term: existing, fresh: false };
  const { bin, args } = config();
  const term = vscode.window.createTerminal({ name: TERMINAL_NAME, cwd: workspaceRoot(uri), shellPath: bin, shellArgs: args });
  return { term, fresh: true };
}

// send types text into janetcode's input without pressing enter, so it
// can be added to before it is sent. A terminal that has just started is
// given a moment to draw its input first.
function send(text, uri) {
  const { term, fresh } = terminal(uri);
  term.show(false);
  setTimeout(() => term.sendText(text, false), fresh ? 1500 : 0);
}

function sendFile(uri) {
  const target = uri || (vscode.window.activeTextEditor && vscode.window.activeTextEditor.document.uri);
  if (!target || target.scheme !== "file") {
    vscode.window.showWarningMessage("janetcode: open a file on disk first.");
    return;
  }
  send(mention(workspaceRoot(target), target.fsPath), target);
}

function sendSelection() {
  const ed = vscode.window.activeTextEditor;
  if (!ed || ed.document.uri.scheme !== "file") {
    vscode.window.showWarningMessage("janetcode: open a file on disk first.");
    return;
  }
  const sel = ed.selection;
  const lines = sel.isEmpty ? [sel.active.line + 1, sel.active.line + 1] : selectionLines(sel.start, sel.end);
  send(mention(workspaceRoot(ed.document.uri), ed.document.uri.fsPath, lines), ed.document.uri);
}

// waitForServer polls until the web server answers, or gives up.
function waitForServer(port, ms) {
  const deadline = Date.now() + ms;
  return new Promise((resolve, reject) => {
    const tryOnce = () => {
      const req = http.get({ host: "127.0.0.1", port, path: "/", timeout: 1000 }, (res) => {
        res.resume();
        resolve();
      });
      req.on("error", () => (Date.now() > deadline ? reject(new Error("janetcode --web did not start on port " + port)) : setTimeout(tryOnce, 250)));
      req.on("timeout", () => req.destroy());
    };
    tryOnce();
  });
}

async function openWeb() {
  if (web && web.panel) {
    web.panel.reveal();
    return;
  }
  const { bin, args, port } = config();
  if (!web) {
    const token = newToken();
    const out = vscode.window.createOutputChannel("janetcode web");
    const proc = cp.spawn(bin, [...args, "--web", "--web-addr", "127.0.0.1:" + port], {
      cwd: workspaceRoot(),
      env: { ...process.env, JANETCODE_WEB_TOKEN: token },
    });
    proc.stdout.on("data", (d) => out.append(d.toString()));
    proc.stderr.on("data", (d) => out.append(d.toString()));
    proc.on("exit", (code) => {
      out.appendLine(`janetcode --web exited (${code})`);
      if (web && web.proc === proc) {
        if (web.panel) web.panel.dispose();
        web = null;
      }
    });
    web = { proc, port, token, panel: null };
    try {
      await waitForServer(port, 15000);
    } catch (e) {
      vscode.window.showErrorMessage("janetcode: " + e.message + " (see the \"janetcode web\" output)");
      proc.kill();
      web = null;
      return;
    }
  }
  // In a remote window the port is forwarded; asExternalUri says where it
  // is reachable from the editor, and it stays a loopback address.
  const local = vscode.Uri.parse(`http://127.0.0.1:${web.port}`);
  const external = await vscode.env.asExternalUri(local);
  const panel = vscode.window.createWebviewPanel("janetcode.web", "janetcode", vscode.ViewColumn.Beside, {
    enableScripts: false,
    retainContextWhenHidden: true,
  });
  panel.webview.html = panelHTML(webURL(external.toString(), web.token), panel.webview.cspSource);
  panel.onDidDispose(() => {
    if (web) web.panel = null;
  });
  web.panel = panel;
}

function activate(context) {
  context.subscriptions.push(
    vscode.commands.registerCommand("janetcode.open", () => terminal().term.show(false)),
    vscode.commands.registerCommand("janetcode.sendFile", sendFile),
    vscode.commands.registerCommand("janetcode.sendSelection", sendSelection),
    vscode.commands.registerCommand("janetcode.openWeb", openWeb),
    { dispose: () => web && web.proc && web.proc.kill() },
  );
}

function deactivate() {
  if (web && web.proc) web.proc.kill();
}

module.exports = { activate, deactivate };
