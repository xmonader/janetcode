// Plain-node checks of the extension: the helpers' output, and that
// extension.js registers every command package.json contributes, run
// against a stub of the vscode module. Run: node test/extension.test.js
"use strict";

const assert = require("assert");
const path = require("path");
const Module = require("module");
const { mention, selectionLines, webURL, panelHTML } = require("../lib");

assert.strictEqual(mention("/w", "/w/src/a.go"), "@src/a.go ");
assert.strictEqual(mention("/w", "/w/src/a.go", [3, 9]), "@src/a.go (lines 3-9) ");
assert.strictEqual(mention("/w", "/w/src/a.go", [4, 4]), "@src/a.go (line 4) ");
assert.strictEqual(mention("/w", "/w/my dir/a.go"), '"my dir/a.go" ');
assert.strictEqual(mention("/w", "/elsewhere/b.go"), "@/elsewhere/b.go ");
assert.deepStrictEqual(selectionLines({ line: 2, character: 0 }, { line: 5, character: 0 }), [3, 5]);
assert.deepStrictEqual(selectionLines({ line: 2, character: 4 }, { line: 5, character: 7 }), [3, 6]);
assert.strictEqual(webURL("http://127.0.0.1:8797/", "t k"), "http://127.0.0.1:8797/?token=t%20k&embed=1");
const html = panelHTML("http://127.0.0.1:8797/?token=abc&embed=1", "vscode-resource:");
assert.ok(html.includes("frame-src http://127.0.0.1:8797;"), html);
assert.ok(html.includes('allow="microphone'), html);
assert.ok(html.includes("default-src 'none'"), html);

// Every contributed command is registered, and nothing else is.
const pkg = require("../package.json");
const registered = [];
const stub = {
  commands: { registerCommand: (id) => { registered.push(id); return { dispose() {} }; } },
  window: { terminals: [] },
  workspace: { getConfiguration: () => ({ get: () => undefined }), workspaceFolders: [] },
  Uri: { parse: (s) => s },
  env: {},
  ViewColumn: { Beside: 2 },
};
const load = Module._load;
Module._load = function (req, ...rest) {
  return req === "vscode" ? stub : load.call(this, req, ...rest);
};
const ext = require(path.join("..", "extension.js"));
Module._load = load;
const ctx = { subscriptions: [] };
ext.activate(ctx);
const contributed = pkg.contributes.commands.map((c) => c.command).sort();
assert.deepStrictEqual(registered.sort(), contributed);
for (const m of Object.values(pkg.contributes.menus).flat()) {
  assert.ok(contributed.includes(m.command), "menu names an unknown command: " + m.command);
}
for (const k of pkg.contributes.keybindings) {
  assert.ok(contributed.includes(k.command), "keybinding names an unknown command: " + k.command);
}
console.log("ok: " + contributed.length + " commands");
