# Large repositories: what was measured and changed (2026-09-29)

The tree: kubernetes `master` as a source tarball (31,426 files, 17,871
Go files, 373 MB), measured as a plain directory and again as a git
repository (every file committed). The machine: 12 cores, Linux 6.8,
shared with other builds at the time, so wall times vary by 2–3× between
runs; the allocation figures are stable and are the ones to compare.

How to reproduce: the two gated tests run only when
`JANETCODE_LARGE_TREE` names a tree, and `JANETCODE_LARGE_GITINIT=1`
makes that tree a repository first (it runs `git init`, `add -A` and
`commit` in it, so point it at a copy).

```sh
JANETCODE_LARGE_TREE=/path/to/kubernetes go test -run TestLargeTree -v ./internal/repomap ./internal/tool
```

## Search tools

| call | before | after |
|---|---|---|
| grep, rare identifier (whole tree, no match limit) | 10.5–23.5 s, **1,021 MB** allocated | 2.2 s, 49 MB |
| grep, common word (stops at 500 matches) | 0.5–3.4 s, 81 MB | 0.17 s, 2 MB |
| grep with `glob: *.go` | 0.6–2.4 s, 69 MB | 0.17 s, 4 MB |
| glob `**/*.go` (stops at 1,000) | 0.4–1.5 s, 26 MB | unchanged |
| list_dir at the root | <1 ms | unchanged |

grep allocated a new string for every line of every file it read, and a
new 8 KB buffer and bufio reader per file. It now matches on the bytes
of a reused line buffer and converts only a matching line.

## Repository map

| | before | after |
|---|---|---|
| files indexed | the first 5,000 in sorted order | 10,000: changed files, then code, then tests, then generated, a tier that does not fit sampled evenly |
| build (plain directory) | 12.8 s for 5,000 files, 658 MB allocated | 2.7 s for 5,000; 5.2 s for 10,000 (798 MB allocated, 91 MB live) |
| build (git repository) | — | 9.2 s for 10,000 (git ls-files lists 17,886 source files, vendor included) |
| rank + render | 70–200 ms | 60–290 ms |
| a scheduler preemption bug: rank of `pkg/scheduler/framework/preemption/preemption.go` | not indexed | 45th, behind other preemption files; the top eight all name preemption or the scheduler |
| "kubelet eviction manager memory pressure": rank of `pkg/kubelet/eviction/eviction_manager.go` | 3,575th | 28th; the top five are `pkg/kubelet/eviction` files |

Three things were wrong on a tree this size:

- **What was indexed.** Sorted-order truncation reached `api/`, `cmd/`
  and part of `pkg/` and never `pkg/scheduler` or most of `staging/`.
  The header now also says "N of M source files" so the model does not
  take a file's absence from the map for its absence from the tree.
- **How long it took.** Parsing is now spread over the cores; the index
  is the same whichever file finishes first.
- **How the request counted.** A word's pull is divided among the files
  it matches, which on 10,000 files is nothing (`scheduler` names about
  800), and PageRank scores span three orders of magnitude, so the map
  for any request led with `apimachinery`'s sets package. Past 1,000
  indexed files the pull is shared by the square root of the spread,
  the graph score is square-rooted, and a file the request clearly pulls
  ranks ahead of every file it does not. Below 1,000 files the ranking is
  exactly what it was. Every path segment the request names now pulls,
  not only the first, and a file name counts by its words
  (`eviction_manager` for "eviction manager").

## Left as measured

- The git fingerprint (`git status` plus HEAD), which keys the shell
  cache and runs before every bash call, took 0.67 s on the git copy of
  kubernetes, cold. The directory walk it falls back to outside git
  stops at 20,000 files and took 0.35–0.61 s.
- Glob stops at 1,000 results and grep at 500 matches; both say so.
