#!/usr/bin/env python3
"""Compare effbench arms across campaigns in hero_agent's final-lean table format.

    ./compare.py runs/jc-mimo-r2:janetcode runs/jc-mimo-r1:pi [~/hero/.../runs/final-mimo:lean]

The first arm is the reference for "speed-adjusted": every other arm's wall time is recomputed
at the reference arm's decode speed in its own run, wall - model + model * (arm tok/s / ref tok/s),
with tok/s = output tokens / (latency - time to first token) summed over the proxy's calls. That
is the definition that reproduces hero_agent's 2026-10-08 final-lean report, and it removes the
broker's load from the comparison; passes and token counts do not depend on it.
"""
import glob
import json
import os
import sys


def arm_runs(spec):
    camp, arm = spec.rsplit(":", 1)
    camp = os.path.expanduser(camp)
    rows = []
    for f in sorted(glob.glob(f"{camp}/{arm}/*/result.json")):
        r = json.load(open(f))
        calls = []
        for line in open(os.path.join(os.path.dirname(f), "proxy_calls.jsonl")):
            try:
                c = json.loads(line)
            except ValueError:
                continue
            if c.get("status") == 200 and c.get("completion_tokens"):
                calls.append(c)
        r["_decode_s"] = sum(max(0, (c.get("latency_ms") or 0) - (c.get("ttft_ms") or 0)) for c in calls) / 1000
        r["_model_s"] = sum((c.get("latency_ms") or 0) for c in calls) / 1000
        rows.append(r)
    return rows


def main():
    specs = sys.argv[1:] or sys.exit(__doc__)
    arms = [(s, arm_runs(s)) for s in specs]
    S = lambda rs, k: sum(r.get(k) or 0 for r in rs)  # noqa: E731
    ref = arms[0][1]
    ref_tps = S(ref, "output_tokens") / max(1e-9, sum(r["_decode_s"] for r in ref))
    print("| arm | runs | passed | time | speed-adjusted | calls | input | cached | fresh input | output | tamper |")
    print("|---|--:|--:|--:|--:|--:|--:|--:|--:|--:|--:|")
    for spec, rs in arms:
        if not rs:
            print(f"| {spec} | 0 | | | | | | | | | |")
            continue
        wall, model = S(rs, "wall_s"), sum(r["_model_s"] for r in rs)
        tps = S(rs, "output_tokens") / max(1e-9, sum(r["_decode_s"] for r in rs))
        adj = wall - model + model * (tps / ref_tps)
        inp, cached = S(rs, "input_tokens"), S(rs, "cached_tokens")
        passed = sum(r["verdict"] == "PASS" for r in rs)
        tamper = sum(bool(r.get("tampered")) for r in rs)
        print(f"| {spec} | {len(rs)} | {passed}/{len(rs)} | {wall:,.0f} s | {adj:,.0f} s | {S(rs, 'llm_calls')} | "
              f"{inp / 1e6:.2f}M | {100 * cached / max(1, inp):.0f}% | {S(rs, 'uncached_input_tokens') / 1e3:.0f}k | "
              f"{S(rs, 'output_tokens') / 1e3:.0f}k | {tamper} |")
    print()
    tasks = sorted({r["task"] for _, rs in arms for r in rs})
    print("| task | " + " | ".join(s.split("/")[-1] for s, _ in arms) + " |")
    print("|---|" + "---|" * len(arms))
    for t in tasks:
        cells = []
        for _, rs in arms:
            mine = sorted((r for r in rs if r["task"] == t), key=lambda r: r["seed"])
            cells.append(" ".join(f"{'P' if r['verdict'] == 'PASS' else 'F'}{r.get('llm_calls')}c/{(r.get('wall_s') or 0):.0f}s" for r in mine))
        print(f"| {t} | " + " | ".join(cells) + " |")


if __name__ == "__main__":
    main()
