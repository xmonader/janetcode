#!/bin/sh
# Clone the real repositories the realrepo tasks run against, as blobless
# clones under repos/ (gitignored). Blobs are fetched lazily the first
# time a commit is checked out, so the clones stay small. Idempotent.
set -eu
cd "$(dirname "$0")"
mkdir -p repos
clone() { # name url
	if [ -d "repos/$1/.git" ]; then
		git -C "repos/$1" fetch --quiet origin
	else
		git clone --quiet --filter=blob:none "$2" "repos/$1"
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
