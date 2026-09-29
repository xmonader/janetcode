// The parts of the extension that do not need VS Code, so they can be
// tested with plain node (test/extension.test.js).
"use strict";

const path = require("path");
const crypto = require("crypto");

// mention is what is typed into janetcode's input for a file: "@path" when
// the path is one word (janetcode attaches it), else the path in quotes
// for the model to read itself. lines, when given, is [first, last],
// 1-based, and is said after it.
function mention(root, file, lines) {
  let rel = root ? path.relative(root, file) : file;
  if (!rel || rel.startsWith("..") || path.isAbsolute(rel)) rel = file;
  rel = rel.split(path.sep).join("/");
  let out = /\s/.test(rel) ? JSON.stringify(rel) : "@" + rel;
  if (lines && lines[0] > 0) {
    out += lines[1] > lines[0] ? ` (lines ${lines[0]}-${lines[1]})` : ` (line ${lines[0]})`;
  }
  return out + " ";
}

// selectionLines turns a VS Code selection (0-based, end exclusive when it
// sits at column 0 of the next line) into 1-based first and last lines.
function selectionLines(start, end) {
  let last = end.line;
  if (end.character === 0 && end.line > start.line) last--;
  return [start.line + 1, last + 1];
}

function newToken() {
  return crypto.randomBytes(24).toString("hex");
}

// webURL is the address the panel frames: the token trades for a cookie
// the frame can keep (embed=1 asks for one that survives being framed
// cross-site, which janetcode grants on loopback only).
function webURL(base, token) {
  return base.replace(/\/+$/, "") + "/?token=" + encodeURIComponent(token) + "&embed=1";
}

// panelHTML is the webview's page: nothing but a frame of janetcode's own
// browser interface, allowed the microphone for its voice button.
function panelHTML(frameURL, cspSource) {
  const origin = new URL(frameURL).origin;
  const esc = (s) => s.replace(/&/g, "&amp;").replace(/"/g, "&quot;").replace(/</g, "&lt;");
  return `<!doctype html>
<html lang="en"><head><meta charset="utf-8">
<meta http-equiv="Content-Security-Policy" content="default-src 'none'; frame-src ${esc(origin)}; style-src ${esc(cspSource)} 'unsafe-inline';">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>janetcode</title>
<style>html,body,iframe{margin:0;padding:0;border:0;width:100%;height:100%;overflow:hidden}body{height:100vh}</style>
</head><body>
<iframe src="${esc(frameURL)}" title="janetcode" allow="microphone; clipboard-read; clipboard-write"></iframe>
</body></html>`;
}

module.exports = { mention, selectionLines, newToken, webURL, panelHTML };
