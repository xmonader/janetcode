#!/usr/bin/env node
// A stand-in for the janetcode binary in the end-to-end test: it records
// how it was started (argv, cwd, the web token's presence) in $JC_OUT,
// serves a page for --web, and otherwise appends whatever is typed into
// its terminal to $JC_OUT/input.txt, so the test can see what the
// extension sent without a model.
"use strict";

const fs = require("fs");
const path = require("path");
const http = require("http");

const out = process.env.JC_OUT;
const args = process.argv.slice(2);
const web = args.includes("--web");
fs.writeFileSync(
  path.join(out, (web ? "web" : "terminal") + "-started.json"),
  JSON.stringify({ argv: args, cwd: process.cwd(), token: Boolean(process.env.JANETCODE_WEB_TOKEN) }),
);

if (web) {
  const addr = args[args.indexOf("--web-addr") + 1] || "127.0.0.1:8797";
  const [host, port] = addr.split(":");
  http
    .createServer((req, res) => {
      fs.appendFileSync(path.join(out, "web-requests.txt"), req.url + "\n");
      res.writeHead(200, { "content-type": "text/html" });
      res.end("<!doctype html><title>fake janetcode</title><p>fake janetcode web</p>");
    })
    .listen(Number(port), host);
} else {
  process.stdout.write("fake janetcode ready\r\n> ");
  if (process.stdin.isTTY) process.stdin.setRawMode(true);
  process.stdin.on("data", (d) => fs.appendFileSync(path.join(out, "input.txt"), d));
}
