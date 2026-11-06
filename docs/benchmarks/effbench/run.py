#!/usr/bin/env python3
"""janetcode against pi (and other arms) on hero_agent's effbench, unchanged.

effbench (~/hero/code/hero_agent/verifications/effbench) is the benchmark hero_agent's herox
was measured on: the same tasks, seeded workspaces, hidden-test grading, tamper and cheat
checks, a metering proxy per arm, and a pi arm built from the latest pi. This driver imports
it as a library and adds one thing, a janetcode arm, so janetcode is measured by the same code
that measured herox and pi. Nothing in effbench is modified.

    AIBROKER_AI02_API_KEY=... ./run.py --campaign jc-mimo-1 --model mimo --arms janetcode,pi --seeds 2
    ./run.py ... --arms janetcode,janetcode:noinline=/path/to/janetcode+JANETCODE_OFF=inline
    ./run.py ... --arms janetcode,janetcode:conflict=+set.conflict_exit=true      # a config key
    ./run.py ... --tasks-root runs/impossible-tasks                                # Impossible-effbench

A `set.<key>=<value>` in a variant's spec goes into the arm's config.json instead of its
environment. --tasks-root runs the tasks of another tasks directory (impossible/make.py writes one;
score them with impossible/score.py).

Campaigns go to runs/<campaign>/ beside this file (git-ignored); summarise with
`<effbench>/report.py runs/<campaign>`. The broker key comes from the environment; effbench's
`lab secrets get` lookup is answered from it.
"""
import argparse
import json
import re
import os
import shutil
import subprocess
import sys
import threading
import time
from pathlib import Path

EFFBENCH = Path(os.environ.get("EFFBENCH_DIR", os.path.expanduser("~/hero/code/hero_agent/verifications/effbench")))
sys.path.insert(0, str(EFFBENCH))
import effbench as eb  # noqa: E402

HERE = Path(__file__).resolve().parent
JANETCODE = Path(os.environ.get("JANETCODE_BIN", HERE.parents[2] / "bin" / "janetcode"))

_real_run = subprocess.run


def _run(cmd, *a, **k):
    # effbench reads the broker key with `lab secrets get <name>`; take it from the environment.
    if isinstance(cmd, list) and cmd[:3] == ["lab", "secrets", "get"]:
        return subprocess.CompletedProcess(cmd, 0, stdout=os.environ.get(cmd[3], "") + "\n", stderr="")
    return _real_run(cmd, *a, **k)


subprocess.run = _run


# --branch-from: (campaign dir, arm, K). Every janetcode arm replays the first K model replies of
# that arm's recorded session for the same task and seed (janetcode --ab-replay), then runs live,
# so the arms differ only from step K on. A K below 1 is a fraction of the recording's calls.
BRANCH = None


def recorded_calls(path):
    """How many model calls a janetcode session file holds (what replay.Read counts)."""
    n = 0
    for line in path.read_text().splitlines():
        try:
            ev = json.loads(line)
        except ValueError:
            continue
        if ev.get("t") == "assistant/message" and ev.get("message") and ev.get("surface_op") != "replace":
            n += 1
    return n


def branch_args(run):
    """The --ab-replay flags for this unit, or none when there is no recording to branch from.
    The decision depends only on the recording, so every arm makes the same one for a unit."""
    if not BRANCH:
        return []
    camp, arm, at = BRANCH
    sessions = sorted((camp / arm / run.name / "janetcode-sessions").glob("*.jsonl"))
    if not sessions:
        eb.log(f"{run.name}: no recorded session under {camp.name}/{arm}; this unit runs unbranched")
        return []
    calls = recorded_calls(sessions[0])
    k = int(at) if at >= 1 else int(at * calls)
    if k < 1 or k >= calls:
        eb.log(f"{run.name}: branch point {k} is outside the recording's {calls} calls; this unit runs unbranched")
        return []
    (run / "branch.json").write_text(json.dumps({"from": str(sessions[0]), "calls": k, "recorded_calls": calls}))
    return ["--ab-replay", str(sessions[0]), "--ab-replay-calls", str(k)]


# Hiding: an agent in an unsandboxed workspace can read the whole disk, and on this box that holds the
# graded tests and full upstream clones with the fixes (evals/), and every other arm's workspace. With
# HIDE set, each arm's process runs under the same bwrap: those paths are empty tmpfs, the workspace
# root shows only that arm's own workspace, and nothing else changes (network, HOME, caches).
# Grading runs outside it. --no-hide turns it off.
HIDE = []


def hide_prefix(ws):
    if not HIDE or not shutil.which("bwrap"):
        return []
    cmd = ["bwrap", "--dev-bind", "/", "/", "--die-with-parent"]
    for p in HIDE:
        cmd += ["--tmpfs", str(p)]
    cmd += ["--tmpfs", str(eb.WS_ROOT), "--bind", str(ws), str(ws)]
    return cmd + ["--chdir", str(ws)]


def strace_prefix(run):
    if not os.environ.get("EFFBENCH_STRACE"):
        return []
    return ["strace", "-f", "--seccomp-bpf", "-e", "trace=connect", "-o", str(run / "connect.strace")]


class QwenArm(eb.QwenCodeArm):
    """effbench's qwencode arm, its command run under hide_prefix."""

    def prepare(self, home, ws, run):
        cmd, env = super().prepare(home, ws, run)
        return hide_prefix(ws) + cmd, env


class PiStraceArm(eb.PiArm):
    """effbench's pi arm, plus what effbench only does for the competitor arms: the connect() audit
    (EFFBENCH_STRACE) and hide_prefix, so all three arms are held to the same checks."""

    def run(self, task, ws, run, prompt):
        base = getattr(self, "_base", None) or self.cmd0
        self._base = base
        self.cmd0 = strace_prefix(run) + hide_prefix(ws) + base
        try:
            info = super().run(task, ws, run, prompt)
        finally:
            self.cmd0 = base
        outside = eb.CompetitorArm.connects_outside_proxy(self, run)
        if outside is not None:
            info["connects_outside_proxy"] = outside
        return info


class JanetcodeArm(eb.CompetitorArm):
    """janetcode headless (`-p --output-format json --yolo`), in a throwaway HOME holding one
    config: the model's context window. The provider is the arm's metering proxy through
    OPENAI_BASE_URL; every other credential is scrubbed from the environment (competitor_env)."""
    home_prefix = "effj-"

    def __init__(self, camp, cfg, model, timeout, key, binary=JANETCODE, name="janetcode", env_extra=None):
        super().__init__(camp, cfg, model, timeout, key)
        env_extra = dict(env_extra or {})
        # set.<key>=<value>: a janetcode config key for this arm, JSON-decoded when it parses.
        self.settings = {}
        for k in [k for k in env_extra if k.startswith("set.")]:
            v = env_extra.pop(k)
            try:
                self.settings[k[4:]] = json.loads(v)
            except ValueError:
                self.settings[k[4:]] = v
        self.name, self.bin, self.env_extra = name, str(binary), env_extra
        v = _real_run([self.bin, "--version"], capture_output=True, text=True)
        self.version = {"bin": self.bin, "sha": eb.sha_short(Path(self.bin)),
                        "version": (v.stdout or v.stderr).strip(), "env": self.env_extra,
                        "settings": self.settings}

    def prepare(self, home, ws, run):
        cfg = home / ".config" / "janetcode"
        cfg.mkdir(parents=True)
        (cfg / "config.json").write_text(json.dumps({"models": {self.model: {"context_window": self.ctx}},
                                                     **self.settings}))
        env = eb.competitor_env(home, None, OPENAI_BASE_URL=self.base_url, **self.env_extra)
        env["OPENAI_API_KEY"] = self.key
        prompt = (run / "prompt.txt").read_text()
        return hide_prefix(ws) + [self.bin, "-p", prompt, "--yolo", "--output-format", "json",
                "--provider", "openai", "--model", self.model] + branch_args(run), env

    def collect(self, home, run):
        sess = home / ".local" / "share" / "janetcode" / "sessions"
        if sess.is_dir():
            shutil.copytree(sess, run / "janetcode-sessions", dirs_exist_ok=True)

    def _result(self, run):
        try:
            return json.loads((run / f"{self.name}.out").read_text())
        except (OSError, ValueError):
            return {}

    def reply(self, run):
        r = self._result(run)
        return r.get("result") or "", ("error" if r.get("is_error") else None)

    def own_metrics(self, run):
        r = self._result(run)
        u = r.get("usage") or {}
        return {"own_llm_calls": r.get("num_turns"), "tool_calls": r.get("tool_calls"),
                "own_input_tokens": (u.get("input_tokens") or 0) + (u.get("cache_read_tokens") or 0)
                + (u.get("cache_write_tokens") or 0),
                "own_cached_tokens": u.get("cache_read_tokens"), "own_output_tokens": u.get("output_tokens"),
                "own_served": r.get("served")}


def main():
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--campaign", required=True)
    ap.add_argument("--subset", choices=sorted(eb.SUBSETS), default="default")
    ap.add_argument("--tasks", nargs="*")
    ap.add_argument("--model", default="mimo", help="a config of ./configs (kimi-for-coding) or effbench's "
                    "longtasks configs: mimo | deepseek | qwen | ...")
    ap.add_argument("--no-hide", action="store_true", help="do not hide evals/, the tasks dir and other arms' "
                    "workspaces from the agents (see hide_prefix)")
    ap.add_argument("--arms", default="janetcode,pi",
                    help="janetcode, pi, qwencode, hermes, openclaw; janetcode:<name>=<binary>[+K=V,...] for a variant")
    ap.add_argument("--seeds", type=int, default=2)
    ap.add_argument("--jobs", type=int, default=2)
    ap.add_argument("--timeout", type=int, default=1200)
    ap.add_argument("--tasks-root", type=Path,
                    help="run the tasks of this directory instead of effbench's (e.g. impossible/make.py's output); "
                         "with no --tasks, every task in it")
    ap.add_argument("--branch-from", metavar="CAMPAIGN[:ARM]",
                    help="A/B that branches mid-run: every janetcode arm replays the first --branch-at replies of "
                         "this campaign's recorded session (arm janetcode by default) for the same task and seed, "
                         "then runs live. Only janetcode arms can replay, so other arms are refused")
    ap.add_argument("--branch-at", type=float, default=0.5,
                    help="with --branch-from: model calls to replay; below 1, a fraction of the recording's calls")
    a = ap.parse_args()

    global BRANCH
    if a.branch_from:
        bcamp, _, barm = a.branch_from.partition(":")
        bdir = HERE / "runs" / bcamp
        if not (bdir / (barm or "janetcode")).is_dir():
            raise SystemExit(f"--branch-from: no {barm or 'janetcode'} arm under {bdir}")
        BRANCH = (bdir, barm or "janetcode", a.branch_at)

    if a.tasks_root:
        eb.TASKS_ROOT = a.tasks_root.resolve()
    tasks = a.tasks or (sorted(p.parent.name for p in eb.TASKS_ROOT.glob("*/seed")) if a.tasks_root
                        else eb.SUBSETS[a.subset])
    cfg = HERE / "configs" / f"{a.model}.toml"
    if not cfg.exists():
        cfg = eb.LONGTASKS / "configs" / f"{a.model}.toml"
    global HIDE
    if not a.no_hide:
        HIDE = [HERE.parents[2] / "evals"] + ([eb.TASKS_ROOT] if a.tasks_root else [])
        eb.WS_ROOT.mkdir(parents=True, exist_ok=True)
    model = eb.served_model(cfg)
    eb.PROVIDER = eb.BACKEND_PROVIDER[eb.served_backend(cfg)]
    prov = eb.PROVIDERS[eb.PROVIDER]
    eb.UPSTREAM, eb.KEY_NAME, eb.PATH_HINT = prov["upstream"], prov["key_env"], prov["path_hint"]
    key = os.environ.get(prov["key_env"]) or os.environ.get("AIBROKER_API_KEY")
    if not key:
        raise SystemExit(f"set {prov['key_env']} (or AIBROKER_API_KEY) to the broker key")
    os.environ[prov["key_env"]] = key
    camp = HERE / "runs" / a.campaign
    camp.mkdir(parents=True, exist_ok=True)
    py = eb.grading_python()
    builds = sorted(Path(os.path.expanduser("~/.cache/effbench")).glob("pi-*/packages/coding-agent/dist/bundle/cli.js"),
                    key=lambda p: p.stat().st_mtime, reverse=True)
    pi_cli = os.environ.get("PI_CLI") or (str(builds[0]) if builds else shutil.which("pi") or "pi")

    arms = []
    # A comma starts a new arm only before an arm's name, so one variant can
    # carry several K=V settings: janetcode:x=bin+set.a=1,set.b=2,pi
    for spec in re.split(r",(?=(?:janetcode|pi|qwencode|hermes|openclaw)\b)", a.arms):
        if spec == "janetcode":
            arms.append(JanetcodeArm(camp, cfg, model, a.timeout, key))
        elif spec.startswith("janetcode:"):
            name, _, rest = spec[len("janetcode:"):].partition("=")
            binary, plus, envspec = rest.partition("+")
            env = dict(p.split("=", 1) for p in envspec.split(",") if p) if plus else {}
            arms.append(JanetcodeArm(camp, cfg, model, a.timeout, key, binary=binary or JANETCODE,
                                     name=name, env_extra=env))
        elif spec == "pi":
            arms.append(PiStraceArm(camp, cfg, model, a.timeout, pi_cli, key))
        elif spec == "qwencode":
            arms.append(QwenArm(camp, cfg, model, a.timeout, key))
        elif spec in ("hermes", "openclaw"):
            arms.append(eb.ARM_CLASSES[spec](camp, cfg, model, a.timeout, key))
        else:
            raise SystemExit(f"unknown arm {spec}")
    if BRANCH and any(not isinstance(arm, JanetcodeArm) for arm in arms):
        raise SystemExit("--branch-from: only janetcode arms can replay a recording; a pi arm would start from scratch "
                         "and the comparison would not be paired")

    (camp / "meta.json").write_text(json.dumps({
        "campaign": a.campaign, "model": model, "config": str(cfg), "tasks": tasks, "seeds": a.seeds,
        "jobs": a.jobs, "timeout": a.timeout, "started": time.strftime("%Y-%m-%dT%H:%M:%S"),
        "provider": eb.PROVIDER, "upstream": eb.UPSTREAM, "effbench": str(EFFBENCH),
        "tasks_root": str(eb.TASKS_ROOT),
        "branch": {"from": str(BRANCH[0]), "arm": BRANCH[1], "at": BRANCH[2]} if BRANCH else None,
        "arms": {arm.name: arm.version for arm in arms}}, indent=2))
    eb.log(f"{a.campaign}: model={model} tasks={len(tasks)} seeds={a.seeds} arms={[x.name for x in arms]}")

    # Every (seed, task, arm) unit; an arm runs one task at a time (its proxy logs one task), so
    # with jobs >= 2 the arms run side by side and see the same broker load.
    pending = [(s, t, arm) for s in range(1, a.seeds + 1) for t in tasks for arm in arms]
    busy, cv = set(), threading.Condition()

    def worker():
        while True:
            with cv:
                while True:
                    unit = next((u for u in pending if u[2].name not in busy), None)
                    if unit or not pending:
                        break
                    cv.wait()
                if not unit:
                    return
                pending.remove(unit)
                busy.add(unit[2].name)
            s, t, arm = unit
            try:
                eb.run_task(arm, camp, t, s, py, model)
            except Exception as exc:  # noqa: BLE001 — one broken task must not end the arm
                eb.log(f"{arm.name} {t}: harness error {exc!r}")
            finally:
                with cv:
                    busy.discard(arm.name)
                    cv.notify_all()

    ths = [threading.Thread(target=worker) for _ in range(max(1, min(a.jobs, len(arms))))]
    for th in ths:
        th.start()
    for th in ths:
        th.join()
    eb.log(f"done -> {camp}")


if __name__ == "__main__":
    main()
