// End-to-end test of the extension in a real VS Code: downloads VS Code
// (cached in .vscode-test/), makes a throwaway workspace whose settings
// point janetcode.path at a fake binary, and runs suite.js in the
// extension host. Needs a display: `xvfb-run -a node test/e2e/run.js`,
// or `make vscode-test` from the repository root.
"use strict";

const fs = require("fs");
const os = require("os");
const path = require("path");
const net = require("net");
const { runTests } = require("@vscode/test-electron");

function freePort() {
  return new Promise((resolve, reject) => {
    const s = net.createServer();
    s.listen(0, "127.0.0.1", () => {
      const { port } = s.address();
      s.close(() => resolve(port));
    });
    s.on("error", reject);
  });
}

async function main() {
  const ext = path.resolve(__dirname, "..", "..");
  const tmp = fs.mkdtempSync(path.join(os.tmpdir(), "janetcode-vscode-"));
  const ws = path.join(tmp, "workspace");
  const out = path.join(tmp, "out");
  fs.mkdirSync(path.join(ws, "src"), { recursive: true });
  fs.mkdirSync(path.join(ws, ".vscode"));
  fs.mkdirSync(out);
  fs.writeFileSync(path.join(ws, "src", "hello.go"), "package main\n\nfunc main() {}\n\n// end\n");
  const fake = path.join(__dirname, "fake-janetcode.js");
  fs.chmodSync(fake, 0o755);
  fs.writeFileSync(
    path.join(ws, ".vscode", "settings.json"),
    JSON.stringify({ "janetcode.path": fake, "janetcode.webPort": await freePort(), "security.workspace.trust.enabled": false }),
  );
  let failed = false;
  try {
    await runTests({
      version: process.env.VSCODE_VERSION || "stable",
      extensionDevelopmentPath: ext,
      extensionTestsPath: path.join(__dirname, "suite.js"),
      launchArgs: [ws, "--disable-extensions", "--skip-welcome", "--skip-release-notes", "--disable-gpu", "--user-data-dir", path.join(tmp, "user")],
      extensionTestsEnv: { JC_OUT: out },
    });
  } catch (e) {
    console.error("vscode e2e failed:", e.message || e);
    failed = true;
  }
  if (!process.env.KEEP) fs.rmSync(tmp, { recursive: true, force: true });
  process.exit(failed ? 1 : 0);
}

main();
