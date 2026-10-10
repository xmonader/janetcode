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
        return [self.bin, "-p", prompt, "--yolo", "--output-format", "json",
                "--provider", "openai", "--model", self.model], env

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
    ap.add_argument("--model", default="mimo", help="effbench longtasks config: mimo | deepseek | qwen | ...")
    ap.add_argument("--arms", default="janetcode,pi",
                    help="janetcode, pi, qwencode, hermes, openclaw; janetcode:<name>=<binary>[+K=V,...] for a variant")
    ap.add_argument("--seeds", type=int, default=2)
    ap.add_argument("--jobs", type=int, default=2)
    ap.add_argument("--timeout", type=int, default=1200)
    ap.add_argument("--tasks-root", type=Path,
                    help="run the tasks of this directory instead of effbench's (e.g. impossible/make.py's output); "
                         "with no --tasks, every task in it")
    a = ap.parse_args()

    if a.tasks_root:
        eb.TASKS_ROOT = a.tasks_root.resolve()
    tasks = a.tasks or (sorted(p.parent.name for p in eb.TASKS_ROOT.glob("*/seed")) if a.tasks_root
                        else eb.SUBSETS[a.subset])
    cfg = eb.LONGTASKS / "configs" / f"{a.model}.toml"
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
    for spec in a.arms.split(","):
        if spec == "janetcode":
            arms.append(JanetcodeArm(camp, cfg, model, a.timeout, key))
        elif spec.startswith("janetcode:"):
            name, _, rest = spec[len("janetcode:"):].partition("=")
            binary, plus, envspec = rest.partition("+")
            env = dict(p.split("=", 1) for p in envspec.split(",") if p) if plus else {}
            arms.append(JanetcodeArm(camp, cfg, model, a.timeout, key, binary=binary or JANETCODE,
                                     name=name, env_extra=env))
        elif spec == "pi":
            arms.append(eb.PiArm(camp, cfg, model, a.timeout, pi_cli, key))
        elif spec in ("qwencode", "hermes", "openclaw"):
            arms.append(eb.ARM_CLASSES[spec](camp, cfg, model, a.timeout, key))
        else:
            raise SystemExit(f"unknown arm {spec}")

    (camp / "meta.json").write_text(json.dumps({
        "campaign": a.campaign, "model": model, "config": str(cfg), "tasks": tasks, "seeds": a.seeds,
        "jobs": a.jobs, "timeout": a.timeout, "started": time.strftime("%Y-%m-%dT%H:%M:%S"),
        "provider": eb.PROVIDER, "upstream": eb.UPSTREAM, "effbench": str(EFFBENCH),
        "tasks_root": str(eb.TASKS_ROOT),
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
