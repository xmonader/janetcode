#!/usr/bin/env python3
"""Tables for a realrepo-vs-agents campaign, built from the result.json files of one or more
effbench campaign dirs (arms keep their names across dirs, so a campaign split in two aggregates).

    ./realrepo_report.py runs/rr-kimi-a runs/rr-kimi-b > out.md

Per arm: pass count, calls, tokens, cache hit, context, time split, passed-vs-failed split, tool usage
(from each agent's own log), harness flags. Then the task x arm pass matrix and an audit of each
agent's commands for network or upstream lookups.
"""
import collections
import json
import re
import sys
from pathlib import Path

ARMS = ["janetcode", "pi", "qwencode"]
CAT = {  # tool name -> category, per agent
    "read_file": "read", "read": "read", "list_dir": "read", "ls": "read",
    "grep": "search", "grep_search": "search", "glob": "search", "find": "search", "repo_map": "search",
    "edit_file": "edit", "edit": "edit", "write_file": "edit", "write": "edit", "apply_patch": "edit",
    "bash": "bash", "run_shell_command": "bash",
}
SUSPECT = re.compile(r"raw\.githubusercontent|api\.github\.com|github\.com/\S+/commit|git (show|fetch|clone|log --all|"
                     r"cat-file|ls-remote)|\bcurl\b|\bwget\b|realrepo|evals/|effbench|go (get|mod download|install) ")


def load(dirs):
    rows = []
    for d in dirs:
        for f in sorted(Path(d).glob("*/*/result.json")):
            r = json.loads(f.read_text())
            r["_dir"] = f.parent
            rows.append(r)
    return rows


def tools_of(r):
    """(tool name counter, commands list, denied events) from the agent's own log."""
    d, arm = r["_dir"], r["arm"]
    names, cmds, denied = collections.Counter(), [], collections.Counter()
    if arm == "pi":
        for l in (d / "pi_events.jsonl").read_text(errors="ignore").splitlines():
            try:
                e = json.loads(l)
            except ValueError:
                continue
            if e.get("type") == "tool_execution_start":
                names[e["toolName"]] += 1
                cmds.append(json.dumps(e.get("args")))
    elif arm == "qwencode":
        for l in (d / "qwencode.out").read_text(errors="ignore").splitlines():
            try:
                e = json.loads(l)
            except ValueError:
                continue
            if e.get("type") == "assistant":
                for b in e["message"].get("content") or []:
                    if b.get("type") == "tool_use":
                        names[b["name"]] += 1
                        cmds.append(json.dumps(b.get("input")))
    else:
        for f in (d / "janetcode-sessions").rglob("*.jsonl"):
            for l in f.read_text(errors="ignore").splitlines():
                try:
                    e = json.loads(l)
                except ValueError:
                    continue
                if e.get("t") == "tool/call":
                    names[e["name"]] += 1
                    cmds.append(e.get("arguments") or "")
                elif e.get("t") == "permission" and e.get("denied"):
                    denied[e.get("reason") or "?"] += 1
    return names, cmds, denied


def net_flags(r):
    """connects_outside_proxy split: loopback (a test server the agent started), DNS (a tool resolving
    a name), anything else (a real outside connection)."""
    out = r.get("connects_outside_proxy") or {}
    lo = sum(v for k, v in out.items() if k.startswith("127.0.0.1:"))
    dns = sum(v for k, v in out.items() if k.endswith(":53"))
    other = {k: v for k, v in out.items() if not k.startswith("127.0.0.1:") and not k.endswith(":53")}
    return lo, dns, other


def k(n):
    return f"{n/1000:.1f}k" if n < 1e6 else f"{n/1e6:.2f}M"


def agg(rs):
    n = len(rs) or 1
    s = lambda key: sum((r.get(key) or 0) for r in rs)
    calls = s("llm_calls")
    return dict(n=len(rs), calls=calls, tools=s("tool_calls"), fresh=s("uncached_input_tokens"), cached=s("cached_tokens"),
                inp=s("input_tokens"), out=s("output_tokens"), reas=s("reasoning_tokens"), wall=s("wall_s"),
                llm=s("llm_seconds"), per=n, avg_ctx=s("input_tokens") / calls if calls else 0)


def main():
    rows = load(sys.argv[1:])
    by = {a: [r for r in rows if r["arm"] == a] for a in ARMS}
    tasks = sorted({r["task"] for r in rows})
    seeds = sorted({r["seed"] for r in rows})
    P = lambda rs: [r for r in rs if r["verdict"] == "PASS"]
    print("## Headline (per arm, all runs)\n")
    print("| arm | pass | model calls | tool calls | fresh in | cached in | output | reasoning | cache hit | ctx / solved task | avg ctx / call | wall total | wall / run | % in model | % in tools+agent |")
    print("|---|--:|--:|--:|--:|--:|--:|--:|--:|--:|--:|--:|--:|--:|--:|")
    for a in ARMS:
        rs = by[a]
        g = agg(rs)
        sol = len(P(rs))
        print(f"| {a} | {sol}/{len(rs)} | {g['calls']} | {g['tools']} | {k(g['fresh'])} | {k(g['cached'])} | {k(g['out'])} | "
              f"{k(g['reas'])} | {100*g['cached']/max(g['inp'],1):.0f}% | {k(g['inp']/max(sol,1))} | {k(g['avg_ctx'])} | "
              f"{g['wall']/60:.0f} min | {g['wall']/max(len(rs),1):.0f} s | {100*g['llm']/max(g['wall'],1):.0f}% | "
              f"{100*(1-g['llm']/max(g['wall'],1)):.0f}% |")
    print("\n## Per-run averages and passed vs failed\n")
    print("| arm | runs | calls/run | tool calls/run | input/run | output/run | wall/run s | | passed: runs | calls | tool calls | input | output | wall s | | failed: runs | calls | tool calls | input | output | wall s |")
    print("|---|--:|--:|--:|--:|--:|--:|---|--:|--:|--:|--:|--:|--:|---|--:|--:|--:|--:|--:|--:|")
    for a in ARMS:
        rs = by[a]
        cells = []
        for grp in (rs, P(rs), [r for r in rs if r["verdict"] != "PASS"]):
            g, n = agg(grp), max(len(grp), 1)
            cells.append(f"{len(grp)} | {g['calls']/n:.1f} | {g['tools']/n:.1f} | {k(g['inp']/n)} | {k(g['out']/n)} | {g['wall']/n:.0f}")
        print(f"| {a} | " + " | | ".join(cells) + " |")
    print("\n## Pass matrix (task x arm; one cell per seed, P = pass, f = fail, T = timeout; wall s)\n")
    print("| task | " + " | ".join(ARMS) + " |")
    print("|---|" + "---|" * len(ARMS))
    for t in tasks:
        cells = []
        for a in ARMS:
            c = []
            for s in seeds:
                r = next((r for r in by[a] if r["task"] == t and r["seed"] == s), None)
                c.append("-" if not r else ("P" if r["verdict"] == "PASS" else ("T" if r.get("timeout") else "f")) + f" {r['wall_s']:.0f}s")
            cells.append(" / ".join(c))
        print(f"| {t} | " + " | ".join(cells) + " |")
    print("\n## Tool usage (calls per arm, from each agent's own log; category totals then names)\n")
    print("| arm | read | search | edit | bash | other | by name |")
    print("|---|--:|--:|--:|--:|--:|---|")
    audit = []
    denied_all = collections.defaultdict(collections.Counter)
    for a in ARMS:
        names = collections.Counter()
        for r in by[a]:
            nm, cmds, den = tools_of(r)
            names += nm
            denied_all[a] += den
            for c in cmds:
                if SUSPECT.search(c):
                    audit.append((a, r["task"], r["seed"], SUSPECT.search(c).group(0), c[:140].replace("\n", " ")))
        cat = collections.Counter()
        for n, v in names.items():
            cat[CAT.get(n, "other")] += v
        print(f"| {a} | {cat['read']} | {cat['search']} | {cat['edit']} | {cat['bash']} | {cat['other']} | "
              + ", ".join(f"{n} {v}" for n, v in names.most_common()) + " |")
    print("\n## Harness flags per arm\n")
    print("| arm | runs | timeouts | agent not ok | invalid / no usage | <5 calls | tampered | model mismatch | leftover procs | loopback connects (test servers) | DNS lookups | other outside connects | cheat-marker calls | ws mismatch calls | tool refusals (denied) |")
    print("|---|--:|--:|--:|--:|--:|--:|--:|--:|--:|--:|--:|--:|--:|---|")
    for a in ARMS:
        rs = by[a]
        nf = [net_flags(r) for r in rs]
        other = collections.Counter()
        for _, _, o in nf:
            other.update(o)
        print(f"| {a} | {len(rs)} | {sum(1 for r in rs if r.get('timeout'))} | {sum(1 for r in rs if not r.get('agent_ok'))} | "
              f"{sum(1 for r in rs if not r.get('valid'))} | {sum(1 for r in rs if r['llm_calls'] < 5)} | "
              f"{sum(1 for r in rs if r.get('tampered'))} | {sum(1 for r in rs if not r.get('model_ok'))} | "
              f"{sum(1 for r in rs if r.get('leftover_processes'))} | {sum(x[0] for x in nf)} | {sum(x[1] for x in nf)} | "
              f"{dict(other) or 0} | {sum(r.get('cheat_marker_calls') or 0 for r in rs)} | {sum(r.get('ws_mismatch_calls') or 0 for r in rs)} | "
              f"{dict(denied_all[a]) or 0} |")
    print("\n## Command audit (commands matching network / upstream / grading-path patterns)\n")
    if not audit:
        print("none")
    for a, t, s, m, c in audit:
        print(f"- {a} {t} s{s}: `{m}` in `{c}`")
    print("\n## Per run\n")
    print("| task | seed | arm | verdict | wall s | calls | tools | input | cached | output | exit | note |")
    print("|---|--:|---|---|--:|--:|--:|--:|--:|--:|--:|---|")
    for r in sorted(rows, key=lambda r: (r["task"], r["seed"], ARMS.index(r["arm"]))):
        note = "timeout" if r.get("timeout") else (r.get("agent_error") or "")
        print(f"| {r['task']} | {r['seed']} | {r['arm']} | {r['verdict']} | {r['wall_s']:.0f} | {r['llm_calls']} | {r.get('tool_calls')} | "
              f"{k(r['input_tokens'])} | {k(r['cached_tokens'])} | {k(r['output_tokens'])} | {r.get('exit_code')} | {note} |")


if __name__ == "__main__":
    main()
