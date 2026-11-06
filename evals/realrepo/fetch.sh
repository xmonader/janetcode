#!/bin/sh
# Prepare the repositories the realrepo tasks run against. Idempotent.
#
#   repos/<repo>       shared full clone (prometheus, hugo, caddy, traefik).
#                      Only gen.py reads it (fix diffs, test files, validation);
#                      the eval agent never sees it.
#   repos/<task-name>  one small repo per task whose history ends at that task's
#                      `base` (the fix's parent): a depth-50 fetch of base from
#                      the shared clone, with no remote, so the fix commit and
#                      everything newer simply is not there to read.
#
# The task list comes from the rr-*.json files (name, tags[1] = shared repo,
# base); run `python3 -I gen.py` first if they are missing.
set -eu
cd "$(dirname "$0")"
here=$(pwd)
mkdir -p repos
clone() { # name url
	if [ -d "repos/$1/.git" ]; then
		# A blobless clone from an older fetch.sh cannot serve the per-task
		# fetches below (no lazy fetching): make it full.
		if git -C "repos/$1" config --get remote.origin.partialclonefilter >/dev/null; then
			git -C "repos/$1" config --unset remote.origin.partialclonefilter
			git -C "repos/$1" config --unset remote.origin.promisor || true
			git -C "repos/$1" config --unset extensions.partialclone || true
			git -C "repos/$1" fetch --quiet --refetch origin
		fi
		git -C "repos/$1" fetch --quiet origin
	else
		git clone --quiet "$2" "repos/$1"
	fi
}
clone prometheus https://github.com/prometheus/prometheus.git
clone hugo       https://github.com/gohugoio/hugo.git
clone caddy      https://github.com/caddyserver/caddy.git
clone traefik    https://github.com/traefik/traefik.git
# kubernetes is only for the scale smoke test (see README.md): depth 1.
if [ "${WITH_KUBERNETES:-}" = 1 ] && [ ! -d repos/kubernetes/.git ]; then
	git clone --quiet --depth 1 --filter=blob:none https://github.com/kubernetes/kubernetes.git repos/kubernetes
fi

# name, shared repo and base of every task, one per line.
python3 -I - "$here" <<'PY' | while read -r name repo base; do
import glob, json, os, sys
for f in sorted(glob.glob(os.path.join(sys.argv[1], "rr-*.json"))):
    t = json.load(open(f))
    print(t["name"], t["tags"][1], t["base"])
PY
	task="repos/$name"
	if [ -d "$task/.git" ] && git -C "$task" cat-file -e "$base^{commit}" 2>/dev/null; then
		continue
	fi
	rm -rf "$task"
	git -C "repos/$repo" update-ref "refs/rr/$name" "$base"
	git init --quiet "$task"
	# No blob filter: every object arrives now, so nothing lazily fetches
	# from a remote later. No remote is configured afterwards either.
	git -C "$task" fetch --quiet --depth 50 "file://$here/repos/$repo" "refs/rr/$name:refs/heads/base"
	git -C "$task" symbolic-ref HEAD refs/heads/base
	git -C "$task" remote remove origin 2>/dev/null || true
	echo "built $task at $base"
done
