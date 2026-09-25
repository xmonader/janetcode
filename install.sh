#!/bin/sh
# Install janetcode from its latest GitHub release:
#
#   curl -fsSL https://raw.githubusercontent.com/xmonader/janetcode/master/install.sh | sh
#
# It downloads the binary for this machine and the release's SHA256SUMS,
# refuses a binary that does not match, and puts it in ~/.local/bin (or
# $JANETCODE_INSTALL_DIR). JANETCODE_VERSION=v0.3.0 picks a release;
# JANETCODE_RELEASES points at a mirror with GitHub's layout. After this,
# janetcode --update keeps it current.
set -eu

base="${JANETCODE_RELEASES:-https://github.com/xmonader/janetcode}"
dir="${JANETCODE_INSTALL_DIR:-$HOME/.local/bin}"

fail() { echo "install.sh: $*" >&2; exit 1; }

command -v curl >/dev/null 2>&1 || fail "needs curl"

case "$(uname -s)" in
  Linux) os=linux ;;
  Darwin) os=darwin ;;
  *) fail "no release build for $(uname -s); build from source: go install github.com/xmonader/janetcode/cmd/janetcode@latest" ;;
esac
case "$(uname -m)" in
  x86_64 | amd64) arch=amd64 ;;
  aarch64 | arm64) arch=arm64 ;;
  *) fail "no release build for $(uname -m); build from source: go install github.com/xmonader/janetcode/cmd/janetcode@latest" ;;
esac

tag="${JANETCODE_VERSION:-}"
if [ -z "$tag" ]; then
  # The redirect of /releases/latest names the tag, and unlike the API it
  # has no rate limit.
  loc=$(curl -fsSI "$base/releases/latest" | tr -d '\r' | sed -n 's/^[Ll]ocation: *//p' | tail -n 1)
  tag=${loc##*/releases/tag/}
  [ -n "$loc" ] && [ "$tag" != "$loc" ] && [ -n "$tag" ] || fail "could not find the latest release at $base (has one been published?)"
fi

asset="janetcode-$tag-$os-$arch"
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT INT TERM

echo "downloading janetcode $tag for $os/$arch"
curl -fsSL -o "$tmp/$asset" "$base/releases/download/$tag/$asset" || fail "no $asset in release $tag"
curl -fsSL -o "$tmp/SHA256SUMS" "$base/releases/download/$tag/SHA256SUMS" || fail "release $tag has no SHA256SUMS"

want=$(awk -v a="$asset" '$2 == a || $2 == "*" a { print $1 }' "$tmp/SHA256SUMS")
[ -n "$want" ] || fail "$asset is not listed in SHA256SUMS"
if command -v sha256sum >/dev/null 2>&1; then
  got=$(sha256sum "$tmp/$asset" | awk '{ print $1 }')
elif command -v shasum >/dev/null 2>&1; then
  got=$(shasum -a 256 "$tmp/$asset" | awk '{ print $1 }')
else
  fail "needs sha256sum or shasum to check the download"
fi
[ "$got" = "$want" ] || fail "$asset does not match SHA256SUMS; nothing was installed"

mkdir -p "$dir"
chmod 755 "$tmp/$asset"
# Beside the target first, then renamed: a half-copied binary is never
# the one on PATH.
cp "$tmp/$asset" "$dir/.janetcode.new"
mv -f "$dir/.janetcode.new" "$dir/janetcode"
echo "installed $dir/janetcode ($tag)"

case ":$PATH:" in
  *":$dir:"*) echo "run: janetcode" ;;
  *) echo "$dir is not on your PATH; add it, e.g. echo 'export PATH=\"$dir:\$PATH\"' >> ~/.profile" ;;
esac
