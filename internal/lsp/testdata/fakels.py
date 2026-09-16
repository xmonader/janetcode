#!/usr/bin/env python3
"""A tiny language server for tests: every line containing ERR is an error,
every line containing WARN a warning. It also asks the client for
workspace/configuration after initialized, like gopls does."""
import json, sys, time

def read():
    length = None
    while True:
        line = sys.stdin.buffer.readline()
        if not line:
            return None
        line = line.strip()
        if not line:
            break
        k, _, v = line.partition(b":")
        if k.strip().lower() == b"content-length":
            length = int(v)
    return json.loads(sys.stdin.buffer.read(length))

def send(msg):
    body = json.dumps(msg).encode()
    sys.stdout.buffer.write(b"Content-Length: %d\r\n\r\n" % len(body))
    sys.stdout.buffer.write(body)
    sys.stdout.buffer.flush()

def diagnose(uri, version, text):
    diags = []
    for i, line in enumerate(text.split("\n")):
        for word, sev in (("ERR", 1), ("WARN", 2)):
            if word in line:
                diags.append({"range": {"start": {"line": i, "character": line.index(word)}, "end": {"line": i, "character": line.index(word) + len(word)}},
                              "severity": sev, "source": "fake", "message": "found " + word})
    params = {"uri": uri, "diagnostics": diags}
    if version is not None:
        params["version"] = version
    send({"jsonrpc": "2.0", "method": "textDocument/publishDiagnostics", "params": params})

no_version = "--no-version" in sys.argv

# --garbage-after-open corrupts the stream after the first didOpen and then
# keeps running, standing in for a server that writes something that is not
# LSP onto stdout (a panic, a stray print) without exiting.
garbage_after_open = "--garbage-after-open" in sys.argv

# --slow-init=SECONDS holds the initialize answer back, standing in for a
# cold rust-analyzer indexing a large tree.
slow_init = 0.0
for arg in sys.argv[1:]:
    if arg.startswith("--slow-init="):
        slow_init = float(arg[len("--slow-init="):])

# --child=PATH forks a long-lived grandchild and records its pid, standing
# in for the workers a real server starts. It only dies with the group.
for arg in sys.argv[1:]:
    if arg.startswith("--child="):
        import subprocess
        kid = subprocess.Popen(["sleep", "100"])
        with open(arg[len("--child="):], "w") as f:
            f.write(str(kid.pid))
while True:
    msg = read()
    if msg is None:
        break
    method = msg.get("method")
    if method == "initialize":
        if slow_init:
            time.sleep(slow_init)
        send({"jsonrpc": "2.0", "id": msg["id"], "result": {"capabilities": {"textDocumentSync": 1}}})
    elif method == "initialized":
        send({"jsonrpc": "2.0", "id": 99, "method": "workspace/configuration", "params": {"items": [{"section": "fake"}]}})
        send({"jsonrpc": "2.0", "method": "window/logMessage", "params": {"type": 1, "message": "fake server error log"}})
    elif method == "textDocument/didOpen":
        td = msg["params"]["textDocument"]
        diagnose(td["uri"], None if no_version else td["version"], td["text"])
        if garbage_after_open:
            sys.stdout.buffer.write(b"Not-An-LSP-Header: 1\r\n\r\n")
            sys.stdout.buffer.flush()
            while True:
                time.sleep(3600)
    elif method == "textDocument/didChange":
        td = msg["params"]["textDocument"]
        diagnose(td["uri"], None if no_version else td["version"], msg["params"]["contentChanges"][-1]["text"])
    elif method == "shutdown":
        send({"jsonrpc": "2.0", "id": msg["id"], "result": None})
    elif method == "exit":
        break
    elif "id" in msg and method is None:
        pass  # response to our configuration request
