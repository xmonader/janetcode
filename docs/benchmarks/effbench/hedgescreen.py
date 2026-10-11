#!/usr/bin/env python3
"""Pre-screen for request hedging and stream resume (internal/llm/hedge.go, resume.go).

    ./hedgescreen.py runs/            # every campaign under runs/
    ./hedgescreen.py runs/jc-mimo-r13 # one campaign

Replays the metering proxy's per-call records (proxy_calls.jsonl) and counts the calls, and the
runs, that the two mechanisms could have rescued:

- resume: a call that streamed text and then broke without a finish reason, where the break was
  the server's and not the client's (client_gone marks a call the agent itself abandoned — a plan
  budget, a runaway tool-call stream — which no resume should undo).
- hedge outright: a call that failed before its first token (non-200, or no time to first token).
  A duplicate on a second route would have raced it.
- hedge tail: a call whose first token came after the campaign's p95 time to first token. A
  duplicate sent at the p95 mark that answered at the median would have saved
  ttft - (p95 + median), when that is positive. This is the optimistic bound: it assumes the
  second route is as fast as the first one's median and independent of it.

Runs: a run is counted rescuable by resume or outright hedging when it ended without a valid
result (invalid or timed out) and held at least one such call; a timed-out run's tail saving is
reported beside it, since whether it would have finished in time is not knowable from the record.
"""
import glob
import json
import os
import statistics
import sys


def calls_of(run_dir):
    out = []
    try:
        lines = open(os.path.join(run_dir, "proxy_calls.jsonl")).read().splitlines()
    except OSError:
        return out
    for line in lines:
        try:
            out.append(json.loads(line))
        except ValueError:
            continue
    return out


def pct(xs, p):
    xs = sorted(xs)
    if not xs:
        return 0
    return xs[min(len(xs) - 1, int(p / 100 * len(xs)))]


def screen(camp):
    runs = []
    for f in sorted(glob.glob(f"{camp}/*/*/result.json")):
        r = json.load(open(f))
        r["_calls"] = calls_of(os.path.dirname(f))
        runs.append(r)
    ttfts = [c["ttft_ms"] for r in runs for c in r["_calls"] if c.get("status") == 200 and c.get("ttft_ms")]
    if not ttfts:
        return None
    p95, med = pct(ttfts, 95), statistics.median(ttfts)
    s = dict(campaign=os.path.basename(camp.rstrip("/")), runs=len(runs), calls=0, resume=0, aborted=0,
             hedge_fail=0, tail=0, tail_saved_s=0.0, lost=0, lost_rescued=0, timeout_tail_s=0.0, p95=p95, med=med)
    for r in runs:
        lost = (not r.get("valid", True)) or r.get("timeout")
        s["lost"] += bool(lost)
        rescued = False
        for c in r["_calls"]:
            s["calls"] += 1
            ok = c.get("status") == 200
            if ok and c.get("finish") is None and (c.get("streamed_chars") or 0) > 0:
                if c.get("client_gone"):
                    s["aborted"] += 1
                else:
                    s["resume"] += 1
                    rescued = True
            if not ok or not c.get("ttft_ms"):
                s["hedge_fail"] += 1
                rescued = True
            elif c["ttft_ms"] > p95:
                saved = (c["ttft_ms"] - (p95 + med)) / 1000
                if saved > 0:
                    s["tail"] += 1
                    s["tail_saved_s"] += saved
                    if r.get("timeout"):
                        s["timeout_tail_s"] += saved
        s["lost_rescued"] += bool(lost and rescued)
    return s


def main():
    roots = sys.argv[1:] or sys.exit(__doc__)
    camps = []
    for root in roots:
        root = os.path.expanduser(root)
        camps += [root] if os.path.exists(os.path.join(root, "results.jsonl")) else sorted(
            os.path.dirname(p) for p in glob.glob(f"{root}/*/results.jsonl"))
    tot = {}
    print(f"{'campaign':22} {'runs':>4} {'lost':>4} {'calls':>6} {'resume':>6} {'aborted':>7} {'hedge':>5} "
          f"{'tail':>4} {'p95 ms':>7} {'saved s':>8} {'lost-rescued':>12}")
    for camp in camps:
        s = screen(camp)
        if not s:
            continue
        print(f"{s['campaign']:22} {s['runs']:4} {s['lost']:4} {s['calls']:6} {s['resume']:6} {s['aborted']:7} "
              f"{s['hedge_fail']:5} {s['tail']:4} {s['p95']:7} {s['tail_saved_s']:8.0f} {s['lost_rescued']:12}")
        for k, v in s.items():
            if isinstance(v, (int, float)) and k not in ("p95", "med"):
                tot[k] = tot.get(k, 0) + v
    if tot:
        print(f"{'total':22} {tot['runs']:4} {tot['lost']:4} {tot['calls']:6} {tot['resume']:6} {tot['aborted']:7} "
              f"{tot['hedge_fail']:5} {tot['tail']:4} {'':7} {tot['tail_saved_s']:8.0f} {tot['lost_rescued']:12}")
        print(f"tail saving inside timed-out runs: {tot['timeout_tail_s']:.0f}s")


if __name__ == "__main__":
    main()
