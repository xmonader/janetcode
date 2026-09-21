#!/usr/bin/env python3
"""Build the mid-turn 'stuck' corpus from recorded eval runs.

Input: one or more --eval-json files (each result carries its full
session event log under "events"). Output: stuck.jsonl, the corpus the
`--decisions-replay` driver's "stuck" branch scores.

Every label is made by construction from the log, never by judgement:

  waste   the tool call at this point is one of
            (a) a call whose result came back is_error (or denied),
            (b) a call inside a loop: >=3 consecutive calls with the
                same tool and the same arguments, or >=3 calls on the
                same (tool, path) inside a window of five, or
            (c) a call in the dead tail: the run did not pass, and
                nothing after the call's position ever produced a
                successful mutating tool result or a passing gate.
  healthy any other call.

The state is exactly what a live mid-turn consumer could build with no
extra reads: the turn's own prompt as the goal, plus the last eight tool
calls rendered the way internal/agent.StuckState renders them. Keep this
file and StuckState in step.

The state stops BEFORE the labelled call runs. The window holds the
calls up to i-1 with their outcomes, and call i appears as a pending
line with no result:

    9. About to run: edit_file {"path":"main.go",…} -> (not run yet)

This is the fix to the first round's leakage. Rendering call i with its
own `ERROR:` line made `next_call_will_fail` score whether Jev can read
a flag the harness already holds, not whether it can predict one; label
(a) is derived from exactly that line. `--leaky` restores the old
rendering for comparison and should only ever be used to reproduce the
first round's numbers.

Usage:
  python3 docs/benchmarks/decisions/build-stuck-corpus.py \
      --out docs/benchmarks/decisions/stuck.jsonl run-a.json run-b.json
"""

import argparse
import collections
import json
import random
import re

WINDOW = 8          # must match agent.StuckWindow
LINE_BUDGET = 220   # must match agent.stuckLineBudget
GOAL_BUDGET = 1000  # must match agent.stuckGoalBudget
STATE_BUDGET = 2500 # must match agent.stuckStateBudget

MUTATORS = {"edit_file", "write_file", "apply_patch", "create_file",
            "multi_edit", "insert_lines", "replace_lines"}


def clip(s, n):
    s = s or ""
    return s if len(s) <= n else s[:n - 1] + "…"


def truthy(v):
    return v is True or str(v).lower() == "true"


def argpath(raw):
    try:
        d = json.loads(raw or "{}")
    except Exception:
        return ""
    for k in ("path", "file", "filename", "file_path"):
        if isinstance(d.get(k), str):
            return d[k]
    return ""


def norm(raw):
    return re.sub(r"\s+", " ", (raw or "").strip())


def goal_text(msg):
    """The user prompt for the turn, out of the logged message blob."""
    if not msg:
        return ""
    if isinstance(msg, str):
        try:
            msg = json.loads(msg)
        except Exception:
            return msg
    if isinstance(msg, dict):
        parts = [b.get("text", "") for b in msg.get("blocks", [])
                 if isinstance(b, dict) and b.get("type") == "text"]
        return " ".join(p for p in parts if p)
    return str(msg)


def one_line(s):
    return re.sub(r"\s+", " ", (s or "").strip())


def strip_repomap(s):
    """The logged user message carries the attached repository map. A
    live consumer would pass the request, not the attachment."""
    i = s.find("[repository map")
    return s[:i].strip() if i > 0 else s


def render_state(goal, window, pending=None):
    """Render the goal, the finished calls in window, and — unless the
    corpus is being built leaky — the pending call with no outcome."""
    out = ["Goal: " + clip(goal, GOAL_BUDGET)]
    if not window and pending is None:
        out.append("Recent tool calls: none yet")
        return clip("\n".join(out) + "\n", STATE_BUDGET)
    out.append("Recent tool calls, oldest first:")
    if not window:
        out.append("(none yet)")
    for i, s in enumerate(window):
        line = "%d. %s %s -> " % (i + 1, s["tool"], s["args"])
        if s["failed"]:
            line += "ERROR: "
        line += s["outcome"]
        out.append(clip(line, LINE_BUDGET))
    if pending is not None:
        out.append(clip("%d. About to run: %s %s -> (not run yet)" % (
            len(window) + 1, pending["tool"], pending["args"]), LINE_BUDGET))
    return clip("\n".join(out) + "\n", STATE_BUDGET)


def label_run(res, leaky=False):
    """Return [(index, state, truth)] for every tool call in one run."""
    ev = res.get("events") or []
    calls, results, samples = [], {}, []
    goal_at = {}
    goal = ""
    for e in ev:
        if e["t"] == "user/message":
            goal = strip_repomap(one_line(goal_text(e.get("message"))))
        elif e["t"] == "tool/call":
            goal_at[int(e["seq"])] = goal
            calls.append(e)
        elif e["t"] == "tool/result" and e.get("name"):
            cid = e.get("call_id")
            if cid:
                results[cid] = e

    # (a) errored calls
    waste = set()
    for i, c in enumerate(calls):
        r = results.get(c.get("call_id"))
        if r is not None and (truthy(r.get("is_error")) or truthy(r.get("denied"))):
            waste.add(i)

    # (b) loops
    i = 0
    while i < len(calls):
        j = i
        key = (calls[i].get("name"), norm(calls[i].get("arguments")))
        while j + 1 < len(calls) and (calls[j + 1].get("name"), norm(calls[j + 1].get("arguments"))) == key:
            j += 1
        if j - i + 1 >= 3:
            waste.update(range(i, j + 1))
        i = j + 1
    for i in range(len(calls)):
        win = list(range(i, min(i + 5, len(calls))))
        cnt = collections.Counter((calls[k].get("name"), argpath(calls[k].get("arguments"))) for k in win)
        for (nm, pth), n in cnt.items():
            if pth and n >= 3:
                waste.update(k for k in win
                             if (calls[k].get("name"), argpath(calls[k].get("arguments"))) == (nm, pth))

    # (c) dead tail
    if not res.get("passed"):
        last_progress = -1
        for i, c in enumerate(calls):
            r = results.get(c.get("call_id"))
            if r is not None and c.get("name") in MUTATORS and not truthy(r.get("is_error")):
                last_progress = i
        gate_ok_seq = max([int(e["seq"]) for e in ev
                           if e["t"] == "verify/gate" and not truthy(e.get("is_error"))] or [0])
        for i, c in enumerate(calls):
            if i > last_progress and int(c["seq"]) > gate_ok_seq:
                waste.add(i)

    rendered = []
    for c in calls:
        r = results.get(c.get("call_id"))
        rendered.append({
            "tool": c.get("name", "?"),
            "args": clip(one_line(c.get("arguments")), 120),
            "failed": bool(r is not None and truthy(r.get("is_error"))),
            "outcome": clip(one_line(r.get("output")) if r is not None else "(no result recorded)", 120),
        })

    for i in range(len(calls)):
        goal = goal_at.get(int(calls[i]["seq"]), "")
        if leaky:
            state = render_state(goal, rendered[max(0, i - WINDOW + 1):i + 1])
        else:
            state = render_state(goal, rendered[max(0, i - WINDOW + 1):i],
                                 pending=rendered[i])
        samples.append((i, state, "waste" if i in waste else "healthy"))
    return samples


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("inputs", nargs="+")
    ap.add_argument("--out", required=True)
    ap.add_argument("--per-class", type=int, default=40)
    ap.add_argument("--seed", type=int, default=7)
    ap.add_argument("--leaky", action="store_true",
                    help="render the labelled call's own result in the state "
                         "(the first round's leaky framing; for comparison only)")
    args = ap.parse_args()

    rng = random.Random(args.seed)
    pools = {"waste": [], "healthy": []}
    for path in args.inputs:
        doc = json.load(open(path))
        arm = doc.get("variant") or path
        for res in doc["results"]:
            for idx, state, truth in label_run(res, leaky=args.leaky):
                name = "%s:%s#%s:call%d" % (arm, res["name"], res.get("run"), idx)
                pools[truth].append({"name": name, "truth": truth,
                                     "state": state, "consumer": "stuck"})

    rows = []
    for truth, pool in pools.items():
        rng.shuffle(pool)
        rows.extend(pool[:args.per_class])
    rng.shuffle(rows)
    with open(args.out, "w") as f:
        for r in rows:
            f.write(json.dumps(r, sort_keys=True) + "\n")
    print("%s: %d rows (%d waste, %d healthy) from pools of %d/%d" % (
        args.out, len(rows),
        sum(1 for r in rows if r["truth"] == "waste"),
        sum(1 for r in rows if r["truth"] == "healthy"),
        len(pools["waste"]), len(pools["healthy"])))


if __name__ == "__main__":
    main()
