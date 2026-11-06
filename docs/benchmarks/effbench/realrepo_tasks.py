#!/usr/bin/env python3
"""Turn evals/realrepo into an effbench tasks directory (for run.py --tasks-root).

    ./realrepo_tasks.py [--out runs/realrepo-tasks]

Per task <name> it writes, in effbench's own task layout:

    <name>/seed/        the per-task repo checked out at `base`, with its .git (pre-base history
                        only, no remote): what the agent works in
    <name>/task.md      the task prompt, verbatim
    <name>/tests/       what effbench's grader runs (`pytest <name>/tests`, WORKSPACE=<workspace>):
        test_grade.py   runs check.sh in the workspace after the agent has finished
        check.sh        the realrepo `check`, verbatim: lays the graded test file(s) over the
                        workspace, then a narrow `go test`
        <name>/<path>   the graded test files (EVAL_TASK_DIR=<name>/, as the check expects)

The graded files are written after the agent is done, so editing them is pointless, and a
passing grade needs the real fix. The seed has no remote and no commit after `base`.
"""
import argparse
import json
import shutil
import subprocess
from pathlib import Path

HERE = Path(__file__).resolve().parent
RR = HERE.parents[2] / "evals" / "realrepo"

GRADE = '''"""Grade: realrepo's check, run in the workspace the agent left (WORKSPACE)."""
import os
import subprocess
from pathlib import Path

TASK = Path(__file__).resolve().parent.parent


def test_realrepo_check():
    env = dict(os.environ, EVAL_TASK_DIR=str(TASK))
    r = subprocess.run(["bash", str(Path(__file__).with_name("check.sh"))], cwd=os.environ["WORKSPACE"],
                       env=env, capture_output=True, text=True)
    print((r.stdout + r.stderr)[-6000:])
    assert r.returncode == 0
'''


def git(*a, cwd):
    subprocess.run(["git", *a], cwd=cwd, check=True, capture_output=True)


def build(js, out):
    name = js["name"]
    dst = out / name
    if dst.exists():
        shutil.rmtree(dst)
    seed = dst / "seed"
    seed.mkdir(parents=True)
    src = RR / js["repo"]
    shutil.copytree(src / ".git", seed / ".git", symlinks=True)
    git("checkout", "-q", "-f", "base", cwd=seed)   # the repo's one branch, at js["base"]
    (dst / "task.md").write_text(js["prompt"])
    tests = dst / "tests"
    tests.mkdir()
    (tests / "test_grade.py").write_text(GRADE)
    (tests / "check.sh").write_text(js["check"] + "\n")
    shutil.copytree(RR / "tests" / name, tests / name)
    return dst


def main():
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--out", type=Path, default=HERE / "runs" / "realrepo-tasks")
    a = ap.parse_args()
    a.out.mkdir(parents=True, exist_ok=True)
    for f in sorted(RR.glob("rr-*.json")):
        js = json.loads(f.read_text())
        d = build(js, a.out)
        head = subprocess.run(["git", "rev-parse", "HEAD"], cwd=d / "seed", capture_output=True, text=True).stdout.strip()
        assert head == js["base"], (js["name"], head)
        n = subprocess.run(["git", "rev-list", "--all", "--count"], cwd=d / "seed", capture_output=True, text=True).stdout.strip()
        print(f"{js['name']:<42} base {head[:8]}  {n} commits")


if __name__ == "__main__":
    main()
