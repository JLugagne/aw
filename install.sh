#!/bin/sh
# Install aw from the latest GitHub release.
#   curl -fsSL https://raw.githubusercontent.com/JLugagne/aw/main/install.sh | sh
# Environment: AW_INSTALL_DIR (default ~/.local/bin), AW_VERSION (default latest).
set -eu

repo="JLugagne/aw"
install_dir="${AW_INSTALL_DIR:-$HOME/.local/bin}"
version="${AW_VERSION:-latest}"

fail() {
	echo "aw install: $*" >&2
	exit 1
}

os=$(uname -s | tr '[:upper:]' '[:lower:]')
case "$os" in
linux | darwin) ;;
*) fail "unsupported OS: $os" ;;
esac

arch=$(uname -m)
case "$arch" in
x86_64 | amd64) arch=amd64 ;;
aarch64 | arm64) arch=arm64 ;;
*) fail "unsupported architecture: $arch" ;;
esac

if [ "$version" = latest ]; then
	base="https://github.com/$repo/releases/latest/download"
	go_ref=latest
else
	base="https://github.com/$repo/releases/download/v${version#v}"
	go_ref="v${version#v}"
fi
archive="aw_${os}_${arch}.tar.gz"

download() {
	if command -v curl >/dev/null 2>&1; then
		curl -fsSL -o "$2" "$1"
	elif command -v wget >/dev/null 2>&1; then
		wget -q -O "$2" "$1"
	else
		fail "curl or wget is required"
	fi
}

sha256() {
	if command -v sha256sum >/dev/null 2>&1; then
		sha256sum "$1" | cut -d' ' -f1
	else
		shasum -a 256 "$1" | cut -d' ' -f1
	fi
}

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT INT TERM

mkdir -p "$install_dir"

if ! download "$base/$archive" "$tmp/$archive" || ! download "$base/checksums.txt" "$tmp/checksums.txt"; then
	if command -v go >/dev/null 2>&1; then
		echo "no release found, building with go install" >&2
		GOBIN="$install_dir" go install "github.com/$repo/cmd/aw@$go_ref"
		echo "aw installed in $install_dir"
		exit 0
	fi
	fail "cannot download $base/$archive"
fi

want=$(awk -v f="$archive" '$2 == f { print $1 }' "$tmp/checksums.txt")
[ -n "$want" ] || fail "$archive not listed in checksums.txt"
got=$(sha256 "$tmp/$archive")
[ "$want" = "$got" ] || fail "checksum mismatch for $archive"

tar -xzf "$tmp/$archive" -C "$tmp" aw
install -m 0755 "$tmp/aw" "$install_dir/aw"
echo "aw installed in $install_dir/aw"

case ":$PATH:" in
*":$install_dir:"*) ;;
*) echo "note: $install_dir is not in your PATH" >&2 ;;
esac
