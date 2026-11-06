# realrepo: real bug fixes in large real repositories

Ten tasks built from merged bug-fix commits in four real Go repositories
(prometheus, hugo, caddy, traefik; 500 to 2,600 files, 83k to 371k lines of
Go at the base commit). Each task runs in a detached worktree of the repo at the
fix's parent (`base`); the prompt is the bug as a user would report it, taken
from the issue or PR text, without the fix. The grade is the fix commit's own
test file, written after the agent finishes and run narrowly
(`go test ./<pkg>/ -run '^(<tests>)$' -count=1`).

    ./fetch.sh                  # blobless clones into repos/ (gitignored)
    python3 -I gen.py --validate  # regenerate the JSONs and prove every task
    bin/janetcode --model <m> --eval evals/realrepo --eval-sandbox strict --eval-runs 2 --eval-jobs 3

Notes:

- The harness's `hidden` map takes only flat file names in an `acceptance/`
  package and copies the whole worktree per gate run, which does not fit a real
  repository. These tasks instead put `git show <fix>:<test file> > <test file>`
  in `check`, which runs after the agent has finished, so the model never sees
  the grading test.
- Validation (`gen.py --validate`): at `base` with the fix's test files laid
  over, the check FAILS; with the fix's non-test changes applied as well it
  PASSES. All ten tasks satisfy both.
- Contamination: the clones keep upstream history, and an unsandboxed eval
  agent has network access; run with `--eval-sandbox strict` (no network,
  `.git` read-only, go build and module caches kept writable) to close both.
  Unsandboxed: In the first run three agents fetched the upstream fix
  (`curl raw.githubusercontent.com`, the GitHub commit-search API) and one ran
  `git show` on a later commit. Treat a pass as upper bound.
- The prompts name the package, so localisation is easy; the scale smoke test
  (kubernetes) is the harder localisation test.
- The check has a 2-minute cap in the harness, so warm the Go module and build
  caches first (`gen.py --validate` does).

| task | repo | files at base | fix | what |
|---|---|---|---|---|
| rr-prom-regex-adjacent-literal | prometheus | 1668 | 73039e31 | FastRegexMatcher false positive on `.*\|(foo)\|.*` |
| rr-prom-relabel-empty-separator | prometheus | 1658 | 7450865d | explicit empty separator/replacement lost in YAML/JSON round trip |
| rr-prom-ts-of-last-before-epoch | prometheus | 1690 | e36d3b0e | `ts_of_last_over_time` wrong for negative timestamps |
| rr-hugo-replacere-zero-width | hugo | 2564 | ae07063f | `replaceRE` with `\b` / `\B` |
| rr-hugo-joinpath-empty | hugo | 2613 | ce5d232b | `urls.JoinPath` panics on empty slice |
| rr-hugo-union-numeric-types | hugo | 2567 | ffbcdba9 | `union` of slices of different numeric types |
| rr-caddy-strip-prefix-encoded | caddy | 626 | 52dc6709 | `strip_path_prefix` with a shorter percent-encoded path |
| rr-caddy-path-matcher-escaped-overmatch | caddy | 631 | 08ad0641 | `path` matcher with `%2f` over-matches longer paths |
| rr-caddy-fmt-backquote-braces | caddy | 505 | b3e692ed | `caddy fmt` mangles braces inside backquotes |
| rr-traefik-cors-wildcard-credentials | traefik | 2010 | f9d79007 | CORS `*` origin with credentials |

## Results (kimi-for-coding via ai02, 2 runs per task, 3 jobs, 2026-11-06)

First run: 11/20 (55%). Nine failures: four hit the harness's 50-model-call cap
per turn (the check never ran), four timed out at 900 s (the model ran the repo's
own full `./check.sh`, which hit the bash tool's 10-minute limit on hugo, or
generated 30k tokens in 14 minutes), one was a wrong fix (hugo union, the
empty-first-list case). Re-running the four capped tasks once with
`--eval-max-turns 120`: 4/4 pass. A repository map is attached to the first
message on every task (e.g. "947 files, 176 pulled up by the request"), so
`repo_map` was never called; grep was used in 17/20 runs, glob in 4/20.

Kubernetes scale smoke (`--depth 1`, 31,733 files): headless read-only question
about the kubelet's eviction decision. 33 s wall, 198 MB peak RSS; about 10 s of
that was a whole-tree git snapshot before the first model call (fixed, now
0.5 s: 33 s -> 19-20 s). The answer named `pkg/kubelet/eviction/eviction_manager.go`
(`synchronize`), `helpers.go`, `api/types.go` and the signal list correctly.
