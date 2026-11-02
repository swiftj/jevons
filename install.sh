#!/bin/sh
# Install Jev-Pro: download the jev binary for this machine from GitHub
# Releases, check its SHA-256, and run `jev install`.
#
#   curl -fsSL https://raw.githubusercontent.com/swiftj/jevons/main/install.sh | sh
#
# Pin a version with JEV_VERSION=v0.1.0. Extra arguments go to `jev install`
# (e.g. `sh -s -- -yes`).
set -eu

repo="swiftj/jevons"

case "$(uname -s)" in
Darwin) asset="jev_darwin_universal" ;;
Linux)
	case "$(uname -m)" in
	x86_64 | amd64) asset="jev_linux_amd64" ;;
	arm64 | aarch64) asset="jev_linux_arm64" ;;
	*)
		echo "jev: unsupported architecture $(uname -m)" >&2
		exit 1
		;;
	esac
	;;
*)
	echo "jev: unsupported OS $(uname -s)" >&2
	exit 1
	;;
esac

if [ -n "${JEV_VERSION:-}" ]; then
	base="https://github.com/$repo/releases/download/$JEV_VERSION"
else
	base="https://github.com/$repo/releases/latest/download"
fi

if command -v shasum >/dev/null 2>&1; then
	sha256() { shasum -a 256 "$1" | cut -d' ' -f1; }
elif command -v sha256sum >/dev/null 2>&1; then
	sha256() { sha256sum "$1" | cut -d' ' -f1; }
else
	echo "jev: need shasum or sha256sum to verify the download" >&2
	exit 1
fi

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT INT TERM

echo "Downloading $asset ${JEV_VERSION:-(latest)}..."
curl -fsSL -o "$tmp/jev" "$base/$asset"
curl -fsSL -o "$tmp/SHA256SUMS" "$base/SHA256SUMS"

want="$(awk -v n="$asset" '$2 == n || $2 == "*" n { print $1 }' "$tmp/SHA256SUMS")"
got="$(sha256 "$tmp/jev")"
if [ -z "$want" ] || [ "$want" != "$got" ]; then
	echo "jev: checksum mismatch for $asset; not installing" >&2
	exit 1
fi
echo "Checksum OK."
chmod +x "$tmp/jev"

# Under `curl | sh` stdin is this script, so prompts read from the terminal.
if [ -r /dev/tty ] && (: </dev/tty) 2>/dev/null; then
	"$tmp/jev" install "$@" </dev/tty
else
	"$tmp/jev" install "$@"
fi
