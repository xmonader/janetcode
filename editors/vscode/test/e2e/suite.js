// Runs inside a real VS Code (the extension host) with the extension
// loaded from source: drives its commands and checks what they did,
// through the fake janetcode (fake-janetcode.js) the workspace settings
// point at. No test framework: run() rejecting fails the run.
"use strict";

const assert = require("assert");
const fs = require("fs");
const path = require("path");
const vscode = require("vscode");

const out = process.env.JC_OUT;

function read(name) {
  try {
    return fs.readFileSync(path.join(out, name), "utf8");
  } catch {
    return "";
  }
}

async function waitFor(what, check, ms = 20000) {
  const deadline = Date.now() + ms;
  for (;;) {
    const v = await check();
    if (v) return v;
    if (Date.now() > deadline) throw new Error("timed out waiting for " + what);
    await new Promise((r) => setTimeout(r, 200));
  }
}

async function run() {
  const root = vscode.workspace.workspaceFolders[0].uri.fsPath;
  const ext = vscode.extensions.all.find((e) => e.packageJSON.name === "janetcode");
  assert.ok(ext, "the janetcode extension is not loaded");

  // 1. Open: a terminal named janetcode, running janetcode itself in the
  // workspace directory.
  await vscode.commands.executeCommand("janetcode.open");
  const started = JSON.parse(await waitFor("the terminal's janetcode to start", () => read("terminal-started.json")));
  assert.strictEqual(fs.realpathSync(started.cwd), fs.realpathSync(root), "terminal cwd");
  assert.ok(vscode.window.terminals.some((t) => t.name === "janetcode"), "no terminal named janetcode");
  console.log("ok  open: terminal janetcode started in", started.cwd);

  // 2. Add Selection: "@path (lines a-b) " typed into that session.
  const doc = await vscode.workspace.openTextDocument(path.join(root, "src", "hello.go"));
  const ed = await vscode.window.showTextDocument(doc);
  ed.selection = new vscode.Selection(1, 0, 3, 0);
  await vscode.commands.executeCommand("janetcode.sendSelection");
  const typed = await waitFor("the selection to be typed", () => read("input.txt").includes("@src/hello.go") && read("input.txt"));
  assert.strictEqual(typed, "@src/hello.go (lines 2-3) ");
  console.log("ok  sendSelection typed", JSON.stringify(typed));

  // 3. Add This File: the whole file as @path, into the same terminal.
  await vscode.commands.executeCommand("janetcode.sendFile");
  await waitFor("the file to be typed", () => read("input.txt").endsWith("@src/hello.go "));
  assert.strictEqual(vscode.window.terminals.filter((t) => t.name === "janetcode").length, 1, "a second terminal was opened");
  console.log("ok  sendFile typed into the same terminal");

  // 4. Open in a Panel: janetcode --web on the configured port with a
  // token, and a webview panel framing it.
  await vscode.commands.executeCommand("janetcode.openWeb");
  const webStarted = JSON.parse(await waitFor("janetcode --web to start", () => read("web-started.json")));
  const port = vscode.workspace.getConfiguration("janetcode").get("webPort");
  assert.deepStrictEqual(webStarted.argv.slice(-3), ["--web", "--web-addr", "127.0.0.1:" + port]);
  assert.ok(webStarted.token, "JANETCODE_WEB_TOKEN was not set");
  const tab = await waitFor("a janetcode webview tab", () =>
    vscode.window.tabGroups.all
      .flatMap((g) => g.tabs)
      .find((t) => t.input instanceof vscode.TabInputWebview && t.input.viewType.includes("janetcode.web")),
  );
  assert.strictEqual(tab.label, "janetcode");
  // The panel's frame asks the server for the page with the token.
  const req = await waitFor("the panel's frame to load the page", () =>
    read("web-requests.txt").split("\n").find((l) => l.includes("token=") && l.includes("embed=1")),
  );
  console.log("ok  openWeb: --web on port", port, "with a token, webview tab", JSON.stringify(tab.label), "framing", req.replace(/token=[^&]+/, "token=…"));
}

module.exports = { run };
