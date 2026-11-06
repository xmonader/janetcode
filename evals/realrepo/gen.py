#!/usr/bin/env python3 -I
"""Generate the realrepo task JSONs from SPECS and (with --validate) prove each
one: at `base` with the fix commit's test files laid over, the check must fail;
with the fix commit's non-test changes applied as well, it must pass.

The grading test files are taken from the fix commit with `git show` inside each
task's `check` command and written only when the check runs, after the agent has
finished, so the model never sees them. (The eval harness's `hidden` map only takes flat file names in
an `acceptance/` package and copies the whole worktree per gate run, which does
not fit a real repository.)
"""
import base64, json, os, subprocess, sys, shutil

HERE = os.path.dirname(os.path.abspath(__file__))
REPOS = os.path.join(HERE, "repos")

SPECS = [
 dict(name="rr-prom-regex-adjacent-literal", repo="prometheus", fix="73039e31", pkg="model/labels",
      tests="TestFastRegexMatcher_MatchString|TestIsSimpleConcatenationPattern",
      prompt="""Label regex matching in model/labels gives a wrong answer for some patterns. The FastRegexMatcher is meant to be an optimised drop-in for the plain RE2 (regexp) matcher, but with a pattern like `.*\\|(foo)\\|.*` (a capturing group written directly between literals and wildcards) it reports a match for values that RE2 rejects, for example it says "x-ab-y" style inputs match `.*-(ab)-.*` when they should not, and the same for `|foo|` against the first pattern. Without the capturing group the optimised path is fine.

Find why the optimised matcher disagrees with RE2 for these patterns and fix it, keeping the optimisation for patterns where it is actually correct (for instance `.*\\|foo\\|.*` must still use it). Do not weaken or delete the existing tests; add test cases for the new patterns."""),
 dict(name="rr-prom-relabel-empty-separator", repo="prometheus", fix="7450865d", pkg="model/relabel",
      tests="TestConfig_UnmarshalThenMarshal|TestRegexp_JSONUnmarshalThenMarshal",
      prompt="""Bug report: a relabel config that sets `separator: ""` and/or `replacement: ""` explicitly does not survive a YAML or JSON round trip in model/relabel. Example:

    source_labels: [namespace, k8s_app]
    separator: ""
    regex: ""
    target_label: target_cluster
    replacement: ""
    action: replace

Unmarshalling this and marshalling it back (YAML, and also JSON) drops the empty separator and replacement and, for YAML, the empty regex. When the result is reloaded, the defaults (";" separator, "$1" replacement, "(.*)" regex) silently come back, so the config means something different from what the user wrote. Explicitly empty values must be preserved by both serialisations; a config that never set them must still behave as before. Fix it."""),
 dict(name="rr-prom-ts-of-last-before-epoch", repo="prometheus", fix="e36d3b0e", pkg="promql",
      tests="TestTsOfLastOverTimeBeforeEpoch",
      prompt="""PromQL `ts_of_last_over_time(metric[1s])` returns the wrong result for samples with timestamps before the Unix epoch (negative timestamps). For a float sample at t=-1000ms and an instant query at t=-1000ms, `ts_of_last_over_time(float_metric[1s])` should return -1 (seconds), and the same for a native-histogram series, but the result is wrong (the series with a negative timestamp is effectively treated as if it had no sample, or reports 0). Timestamps >= 0 work fine. Find the cause in the promql package and fix it, for both float and histogram samples. Add a regression test."""),
 dict(name="rr-hugo-replacere-zero-width", repo="hugo", fix="ae07063f", pkg="tpl/strings",
      tests="TestReplaceRE",
      prompt="""The Hugo template function `strings.ReplaceRE` misbehaves with patterns that use zero-width assertions such as `\\b` or `\\B`. Examples of what users expect (same as Go's regexp.ReplaceAllString):

    {{ replaceRE `\\Bcd` "-" "abcdef" }}        -> ab-ef   (currently the match is missed)
    {{ replaceRE `\\b` "|" "foo bar" }}          -> |foo| |bar|
    {{ replaceRE `\\Bb` "X" "foobar bar bab" }}  -> fooXar bar baX
    {{ replaceRE `\\Bb` "X" "foobar bar bab" 1 }} -> fooXar bar bab   (the optional limit argument)

Anything with a lookaround-like assertion that depends on the text around the match gives wrong output, while ordinary patterns work. Fix replaceRE in tpl/strings, keeping the optional limit argument and `$1`-style expansion working, and add tests."""),
 dict(name="rr-hugo-joinpath-empty", repo="hugo", fix="ce5d232b", pkg="tpl/urls",
      tests="TestJoinPath",
      prompt="""Hugo's `urls.JoinPath` template function panics (slice bounds out of range) when it is given nothing to join: an empty slice, e.g. `{{ urls.JoinPath (slice) }}`, `{{ urls.JoinPath (slice) (slice) }}` (several empty slices), or any call whose arguments flatten to zero elements. A template should never crash the build for this. It should return an empty string, and mixing empty slices with real elements should still join the real elements (`urls.JoinPath (slice) "a" (slice)` is `a`; `(slice) (slice "a" "b") "c"` is `a/b/c`). Fix it and add tests."""),
 dict(name="rr-hugo-union-numeric-types", repo="hugo", fix="ffbcdba9", pkg="tpl/collections",
      tests="TestUnion",
      prompt="""Hugo's `collections.Union` returns an empty result when the two slices have different numeric element types. For example `union (slice 1) (slice 1.0)` gives `[]` instead of the unified list, and the same for `[]int{1,2}` with `[]float64{2.0, 3.0}`, which should yield `[1 2 3]` (the type of the first list wins), and `[]float64{1.0}` with `[]int{1, 2}` should give `[1 2]` as floats. `intersect` already compares numbers across numeric types, so this is inconsistent. Be careful with an empty first list of a different numeric type than the second: the result must not panic or contain elements of the wrong type. Slices of non-numeric mismatching types (e.g. strings with ints) must keep behaving as they do today. Fix it in tpl/collections and add tests."""),
 dict(name="rr-caddy-strip-prefix-encoded", repo="caddy", fix="52dc6709", pkg="modules/caddyhttp/rewrite",
      tests="TestRewrite",
      prompt="""Caddy's `rewrite` handler with `strip_path_prefix` strips the wrong thing when the request path is percent-encoded and shorter than the prefix. With `strip_path_prefix /aaaaaa` and a request for `/%61%61` (which decodes to `/aa`, a path that is NOT under the prefix `/aaaaaa`), the request is rewritten as if the prefix had matched, instead of being left untouched. A path that really does start with the prefix, whether written plainly or percent-encoded, must still be stripped. Find and fix the bug in the rewrite module and add a regression case to its tests."""),
 dict(name="rr-caddy-path-matcher-escaped-overmatch", repo="caddy", fix="08ad0641", pkg="modules/caddyhttp",
      tests="TestPathMatcher",
      prompt="""Caddy's `path` request matcher over-matches when the configured path contains a percent-escape. With `path /foo%2fbar`, the request path `/foo%2fbarbaz` is matched, even though the pattern has no trailing wildcard and `/foo%2fbarbaz` is a longer path. The same happens for `/foo%2f` vs `/foo%2fx`, and `/admin%2fpanel` vs `/admin%2fpanelX`: it behaves like a prefix match. Patterns without escapes are fine, and a pattern with escapes must still match exactly the equivalent path (and wildcard forms like `%*` must keep working). This is a security-relevant routing bug (a path matcher guarding `/admin%2fpanel` also applies to unrelated longer paths). Fix MatchPath in modules/caddyhttp and add tests."""),
 dict(name="rr-caddy-fmt-backquote-braces", repo="caddy", fix="b3e692ed", pkg="caddyconfig/caddyfile",
      tests="TestFormatter",
      prompt="""`caddy fmt` mangles Caddyfile content that contains braces inside a backquoted string. For this input

    block {respond `All braces should remain: {{now | date "2006"}}`}

the formatter splits the `{{ ... }}` template braces across lines and re-indents them as if they were block delimiters, changing the string's content. The braces inside the backquoted string must come out exactly as written (the whole example above should be returned unchanged, and the same is true for a double-quoted string that contains backquotes and braces). Block braces outside of strings must still be formatted as before. Fix the formatter in caddyconfig/caddyfile and add tests."""),
 dict(name="rr-traefik-cors-wildcard-credentials", repo="traefik", fix="f9d79007", pkg="pkg/middlewares/headers",
      tests="TestNewHeader_CORSPreflights|TestNewHeader_CORSResponses",
      prompt="""Traefik's headers middleware sends an invalid CORS response when `accessControlAllowOriginList` contains `*` and `accessControlAllowCredentials` is true. It answers with `Access-Control-Allow-Origin: *` together with `Access-Control-Allow-Credentials: true`, which browsers reject: the Fetch spec forbids the wildcard when credentials are allowed. In that combination the response should echo the request's Origin (for example `https://foo.bar.org`) in `Access-Control-Allow-Origin` instead of `*`, for both preflight (OPTIONS) requests and normal responses. Without credentials, `*` must keep working as today. Fix this in pkg/middlewares/headers and update the existing tests accordingly."""),
]

def git(repo, *a, check=True):
    return subprocess.run(["git", "-C", os.path.join(REPOS, repo), *a], capture_output=True, text=True, check=check).stdout

def build(s):
    fix = git(s["repo"], "rev-parse", s["fix"]).strip()
    base = git(s["repo"], "rev-parse", fix + "^").strip()
    files = git(s["repo"], "diff-tree", "--no-commit-id", "--name-only", "-r", fix).split()
    tests = [f for f in files if f.endswith("_test.go")]
    code = [f for f in files if not f.endswith("_test.go")]
    cmd = ["set -e"]
    for f in tests:
        cmd.append(f"git show {fix}:{f} > {f}")
    cmd.append(f"go test ./{s['pkg']}/ -run '^({s['tests']})$' -count=1")
    n_files = len(git(s["repo"], "ls-tree", "-r", "--name-only", base).split("\n")) - 1
    task = dict(name=s["name"], prompt=s["prompt"], repo=f"repos/{s['repo']}", base=base,
                check="\n".join(cmd), timeout=900, max_tool_calls=80, tags=["realrepo", s["repo"]])
    meta = dict(fix=fix, tests=tests, code=code, pkg=s["pkg"], n_files=n_files)
    return task, meta

def sh(cmd, cwd):
    r = subprocess.run(["bash", "-c", cmd], cwd=cwd, capture_output=True, text=True, timeout=900)
    return r.returncode, (r.stdout + r.stderr)[-600:]

def validate(task, meta, s):
    wt = os.path.join(os.environ.get("WT", "/tmp/rr-validate"), s["name"])
    os.makedirs(os.path.dirname(wt), exist_ok=True)
    if os.path.isdir(wt):
        git(s["repo"], "worktree", "remove", "--force", wt, check=False)
    git(s["repo"], "worktree", "add", "-q", "--detach", wt, task["base"])
    try:
        rc1, o1 = sh(task["check"], wt)
        for f in meta["code"]:
            body = subprocess.run(["git", "-C", os.path.join(REPOS, s["repo"]), "show", f"{meta['fix']}:{f}"], capture_output=True).stdout
            open(os.path.join(wt, f), "wb").write(body)
        rc2, o2 = sh(task["check"], wt)
    finally:
        git(s["repo"], "worktree", "remove", "--force", wt, check=False)
    return rc1 != 0, rc2 == 0, o1, o2

def main():
    only = [a for a in sys.argv[1:] if not a.startswith("--")]
    val = "--validate" in sys.argv
    meta_all = {}
    for s in SPECS:
        if only and s["name"] not in only:
            continue
        task, meta = build(s)
        json.dump(task, open(os.path.join(HERE, s["name"] + ".json"), "w"), indent=2)
        open(os.path.join(HERE, s["name"] + ".json"), "a").write("\n")
        line = f"{s['name']:48} files={meta['n_files']:6} fix={meta['fix'][:8]} code={meta['code']}"
        if val:
            fails_base, passes_fix, o1, o2 = validate(task, meta, s)
            line += f"  base-fails={fails_base} fix-passes={passes_fix}"
            if not (fails_base and passes_fix):
                line += "\n  base: " + o1 + "\n  fix: " + o2
        print(line, flush=True)

main()
