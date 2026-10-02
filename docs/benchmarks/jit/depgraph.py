#!/usr/bin/env python3
"""How many model calls in recorded sessions were round trips a compiler
could have removed?

For every turn of every session, each model call ("step") that made tool
calls is placed on a dependency graph:

  data dependency     step k depends on step j when an argument of one of
                      k's calls contains a token (identifier, path, number
                      of 5+ characters) that first appeared in a result of
                      step j — not in the user's prompt, not earlier in the
                      conversation, and not invented by the model itself.
  control dependency  (conservative mode only) step k also depends on step
                      k-1 when k-1 had a failed result or ran a command:
                      what a command printed can change what the model
                      does next without any of it appearing in the next
                      call's arguments.

level(k) = 1 + max level of what k depends on. The deepest level is the
critical path: the fewest model calls a planner that knew the data flow
would still need. Every other call was a round trip spent waiting for an
observation the next call did not use.

This over-estimates in one way (a decision can depend on a result without
quoting it: the data mode misses that, the conservative mode catches the
common cases) and under-estimates in another (calls that could have been
batched with an *earlier* turn are not counted). Read the two modes as a
range, not a point.

Usage: depgraph.py [--json] SESSION.jsonl...
"""
import json
import re
import sys
from collections import defaultdict

TOKEN = re.compile(r"[A-Za-z0-9_][A-Za-z0-9_./\-]{4,}")
# Tokens that appear everywhere and prove nothing about where an argument
# came from.
STOP = {
    "true", "false", "null", "path", "pattern", "command", "content",
    "old_string", "new_string", "timeout", "description", "return",
    "import", "error", "print", "string", "value", "files", "function",
}
FAKE_MODELS = {"m", "x", "fake", "fake-live-model", "scripted"}
FAILED = re.compile(r"(exit status [1-9]|exit code [1-9]|\bFAIL\b|Traceback|error:|Error:|panic:|not found|No such file)")
COMMAND_TOOLS = {"bash", "terminal", "job", "monitor", "execute_code", "run_code"}


def tokens(text):
    return {t for t in TOKEN.findall(text or "") if t.lower() not in STOP and (not t.isdigit() or len(t) >= 6)}


def call_args(call):
    v = call.get("input")
    return v if isinstance(v, str) else json.dumps(v)


def analyse_session(path):
    meta, events = None, []
    with open(path) as f:
        for line in f:
            try:
                e = json.loads(line)
            except ValueError:
                continue
            if e.get("type") == "meta":
                meta = e.get("meta", {})
            elif e.get("type") == "event":
                events.append(e)
    if not meta or meta.get("model") in FAKE_MODELS:
        return None

    known = set()  # tokens known before the current turn
    turns = defaultdict(lambda: {"prompt": "", "steps": defaultdict(lambda: {"calls": [], "text": "", "results": [], "usage": {}})})
    order = []
    for e in events:
        t, turn = e.get("t"), e.get("turn")
        if turn is None:
            continue
        if turn not in turns:
            order.append(turn)
        tr = turns[turn]
        if t == "user/message":
            msg = e.get("message") or {}
            tr["prompt"] += " ".join(b.get("text", "") for b in msg.get("blocks", []) if isinstance(b, dict))
            tr["prompt"] += " " + (e.get("output") or "")
        elif t == "assistant/message":
            st = tr["steps"][e.get("step", 0)]
            msg = e.get("message") or {}
            for b in msg.get("blocks", []):
                if b.get("type") == "tool_use":
                    st["calls"].append(b)
                elif b.get("type") in ("text", "thinking"):
                    st["text"] += b.get("text", "")
            st["usage"] = e.get("usage") or {}
        elif t == "tool/result":
            tr["steps"][e.get("step", 0)]["results"].append(e)

    out = []
    for turn in order:
        tr = turns[turn]
        known |= tokens(tr["prompt"])
        first_seen = {}  # token -> step whose results first showed it
        steps = sorted(tr["steps"].items())
        level = {"data": {}, "cons": {}}
        prev = None
        rows = []
        for k, st in steps:
            if not st["calls"]:
                # A text-only step: the answer. It needs everything, so it
                # sits one level past the deepest call; counted on both sides.
                rows.append((k, None, st))
                continue
            deps = set()
            for c in st["calls"]:
                for tok in tokens(call_args(c)):
                    if tok in known:
                        continue
                    j = first_seen.get(tok)
                    if j is not None and j < k:
                        deps.add(j)
            ld = 1 + max((level["data"][j] for j in deps), default=0)
            cdeps = set(deps)
            if prev is not None:
                pst = tr["steps"][prev]
                failed = any(FAILED.search(r.get("output") or "") or r.get("error") for r in pst["results"])
                ran = any(c.get("name") in COMMAND_TOOLS for c in pst["calls"])
                if failed or ran:
                    cdeps.add(prev)
            lc = 1 + max((level["cons"][j] for j in cdeps), default=0)
            level["data"][k], level["cons"][k] = ld, lc
            rows.append((k, (ld, lc), st))
            for r in st["results"]:
                for tok in tokens(r.get("output") or ""):
                    first_seen.setdefault(tok, k)
            prev = k
        calls = [r for r in rows if r[1] is not None]
        if not calls:
            known |= set(first_seen)
            continue
        n = len(calls)
        crit_d = max(r[1][0] for r in calls)
        crit_c = max(r[1][1] for r in calls)

        def inp(st):
            u = st["usage"]
            return (u.get("input_tokens") or 0) + (u.get("cache_read_tokens") or 0) + (u.get("cache_write_tokens") or 0)

        # The calls a compiler would keep: one per level (the cheapest
        # stand-in is the first call at each level).
        def kept_tokens(mode):
            seen, total = set(), 0
            for k, lv, st in calls:
                if lv[mode] not in seen:
                    seen.add(lv[mode])
                    total += inp(st)
            return total
        all_tok = sum(inp(st) for _, _, st in calls)
        out.append({
            "session": meta.get("id"), "model": meta.get("model"), "turn": turn,
            "calls": n, "crit_data": crit_d, "crit_cons": crit_c,
            "tool_calls": sum(len(st["calls"]) for _, _, st in calls),
            "in_tokens": all_tok, "kept_tokens_data": kept_tokens(0), "kept_tokens_cons": kept_tokens(1),
        })
        known |= set(first_seen)
        for _, _, st in rows:
            known |= tokens(st["text"])
    return out


def main(argv):
    as_json = "--json" in argv
    paths = [a for a in argv if not a.startswith("--")]
    turns = []
    for p in paths:
        r = analyse_session(p)
        if r:
            turns.extend(r)
    if as_json:
        json.dump(turns, sys.stdout, indent=1)
        return
    multi = [t for t in turns if t["calls"] >= 2]
    def agg(ts):
        n = sum(t["calls"] for t in ts)
        d = sum(t["crit_data"] for t in ts)
        c = sum(t["crit_cons"] for t in ts)
        tok = sum(t["in_tokens"] for t in ts)
        kd = sum(t["kept_tokens_data"] for t in ts)
        kc = sum(t["kept_tokens_cons"] for t in ts)
        return n, d, c, tok, kd, kc
    print(f"sessions with real models: {len({t['session'] for t in turns})}, turns with tool calls: {len(turns)}, of which multi-step: {len(multi)}")
    for label, ts in (("all turns", turns), ("multi-step turns", multi)):
        n, d, c, tok, kd, kc = agg(ts)
        if not n:
            continue
        print(f"\n{label}: {n} model calls that made tool calls, {sum(t['tool_calls'] for t in ts)} tool calls")
        print(f"  critical path, data deps only:      {d:5d}  -> {100*(n-d)/n:4.1f}% of model calls avoidable")
        print(f"  critical path, data + control deps: {c:5d}  -> {100*(n-c)/n:4.1f}% of model calls avoidable")
        if tok:
            print(f"  input tokens: {tok:,}; kept (data) {kd:,} -> {100*(tok-kd)/tok:4.1f}% saved; kept (cons) {kc:,} -> {100*(tok-kc)/tok:4.1f}% saved")
    by_model = defaultdict(list)
    for t in multi:
        by_model[t["model"]].append(t)
    print("\nper model (multi-step turns): calls, avoidable data%, avoidable cons%")
    for m, ts in sorted(by_model.items(), key=lambda kv: -sum(t["calls"] for t in kv[1])):
        n, d, c, *_ = agg(ts)
        print(f"  {m:40s} {n:5d}  {100*(n-d)/n:5.1f}%  {100*(n-c)/n:5.1f}%")


if __name__ == "__main__":
    main(sys.argv[1:])
