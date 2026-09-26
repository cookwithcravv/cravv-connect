#!/bin/sh
# Install cravv-connect and cravv-relay from a GitHub release.
#
#   curl -fsSL https://raw.githubusercontent.com/cravv/cravv-connect/main/scripts/install.sh | sh
#   curl -fsSL https://raw.githubusercontent.com/cravv/cravv-connect/main/scripts/install.sh | sh -s -- --system
#
# Installs to ~/.local/bin, or /usr/local/bin with --system (using sudo when
# needed). The archive is checked against the release's SHA256SUMS before
# anything is installed.
#
# Environment:
#   CRAVV_VERSION   release tag to install, for example v1.2.0 (default: the latest release)
#   CRAVV_REPO      GitHub repository, owner/name (default: cravv/cravv-connect)
#   CRAVV_BASE_URL  release URL, default https://github.com/$CRAVV_REPO/releases
#                   (it serves <base>/latest and <base>/download/<tag>/<file>);
#                   https only, or plain http on http://127.0.0.1 or http://localhost
set -eu

# The default repository is a placeholder: update it (here and in the URLs
# above) once the real repository exists.
repo="${CRAVV_REPO:-cravv/cravv-connect}"
base="${CRAVV_BASE_URL:-https://github.com/$repo/releases}"
bindir="$HOME/.local/bin"
system=0

say() { printf '%s\n' "$*"; }
die() { printf 'install.sh: %s\n' "$*" >&2; exit 1; }

usage() {
	say "usage: install.sh [--system]"
	say "  --system  install to /usr/local/bin instead of ~/.local/bin"
	say "Environment: CRAVV_VERSION (default: latest), CRAVV_REPO (default: cravv/cravv-connect), CRAVV_BASE_URL"
}

while [ $# -gt 0 ]; do
	case "$1" in
	--system) system=1 ;;
	-h | --help)
		usage
		exit 0
		;;
	*)
		usage >&2
		exit 2
		;;
	esac
	shift
done
if [ "$system" = 1 ]; then
	bindir="${CRAVV_SYSTEM_DIR:-/usr/local/bin}"
fi

case "$(uname -s)" in
Darwin) os=darwin ;;
Linux) os=linux ;;
*) die "unsupported operating system $(uname -s) (releases exist for macOS and Linux)" ;;
esac
case "$(uname -m)" in
x86_64 | amd64) arch=amd64 ;;
arm64 | aarch64) arch=arm64 ;;
*) die "unsupported processor $(uname -m) (releases exist for amd64 and arm64)" ;;
esac
# A shell running under Rosetta reports x86_64 on Apple silicon.
if [ "$os" = darwin ] && [ "$arch" = amd64 ] && [ "$(sysctl -n sysctl.proc_translated 2>/dev/null || echo 0)" = 1 ]; then
	arch=arm64
fi

# Downloads use https only. Plain http is allowed for a release server on
# this machine (tests, local mirrors), and never with a user part, which would
# send the request to another host.
loopback=0
case "$base" in
*@*) die "CRAVV_BASE_URL must be an https URL (plain http only for http://127.0.0.1 or http://localhost): $base" ;;
https://?*) ;;
http://127.0.0.1 | http://127.0.0.1[:/]* | http://localhost | http://localhost[:/]*) loopback=1 ;;
*) die "CRAVV_BASE_URL must be an https URL (plain http only for http://127.0.0.1 or http://localhost): $base" ;;
esac

if command -v curl >/dev/null 2>&1; then
	proto='=https'
	[ "$loopback" = 0 ] || proto='=http'
	fetch() { curl --proto "$proto" --proto-redir "$proto" --tlsv1.2 -fsSL -o "$2" "$1"; }
	# The URL that <base>/latest redirects to ends in /tag/<tag>.
	latest_url() { curl --proto "$proto" --proto-redir "$proto" --tlsv1.2 -fsSL -o /dev/null -w '%{url_effective}' "$base/latest"; }
elif command -v wget >/dev/null 2>&1; then
	# BusyBox wget has no --https-only: use it where wget knows it.
	wget_https=""
	if [ "$loopback" = 0 ]; then
		case "$(wget --help 2>&1)" in
		*--https-only*) wget_https="--https-only" ;;
		esac
	fi
	fetch() { wget -q $wget_https -O "$2" "$1"; }
	latest_url() { wget -q $wget_https -O /dev/null --server-response "$base/latest" 2>&1 | sed -n 's/^ *[Ll]ocation: *//p' | tail -n 1 | tr -d '\r'; }
else
	die "curl or wget is needed"
fi

if command -v sha256sum >/dev/null 2>&1; then
	sha256() { sha256sum "$1" | awk '{print $1}'; }
elif command -v shasum >/dev/null 2>&1; then
	sha256() { shasum -a 256 "$1" | awk '{print $1}'; }
else
	die "sha256sum or shasum is needed to check the download"
fi

tag="${CRAVV_VERSION:-}"
if [ -z "$tag" ]; then
	url="$(latest_url)" || die "cannot find the latest release at $base/latest"
	tag="${url##*/tag/}"
	[ "$tag" != "$url" ] || die "cannot find the latest release at $base/latest (no release yet?)"
fi
case "$tag" in
v[0-9]*) ;;
*) die "not a release tag: $tag (expected something like v1.2.0)" ;;
esac
case "$tag" in
*[!A-Za-z0-9.+-]*) die "not a release tag: $tag" ;;
esac

name="cravv-connect_${tag#v}_${os}_${arch}"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
trap 'exit 1' HUP INT TERM

say "Downloading cravv-connect $tag for $os/$arch..."
fetch "$base/download/$tag/$name.tar.gz" "$tmp/$name.tar.gz" || die "download failed: $base/download/$tag/$name.tar.gz"
fetch "$base/download/$tag/SHA256SUMS" "$tmp/SHA256SUMS" || die "download failed: $base/download/$tag/SHA256SUMS"

want="$(awk -v f="$name.tar.gz" '$2 == f || $2 == "*" f {print $1}' "$tmp/SHA256SUMS")"
[ -n "$want" ] || die "SHA256SUMS has no entry for $name.tar.gz"
got="$(sha256 "$tmp/$name.tar.gz")"
[ "$got" = "$want" ] || die "checksum mismatch for $name.tar.gz (expected $want, got $got); nothing was installed"

tar -xzf "$tmp/$name.tar.gz" -C "$tmp"
for b in cravv-connect cravv-relay; do
	[ -f "$tmp/$name/$b" ] || die "the archive has no $b"
done

sudo=""
mkdir -p "$bindir" 2>/dev/null || true
if [ ! -d "$bindir" ] || [ ! -w "$bindir" ]; then
	[ "$system" = 1 ] || die "cannot write to $bindir"
	command -v sudo >/dev/null 2>&1 || die "cannot write to $bindir and sudo is not available"
	sudo="sudo"
	say "Installing to $bindir needs sudo."
	$sudo mkdir -p "$bindir"
fi
upgrade=0
[ -e "$bindir/cravv-connect" ] && upgrade=1
for b in cravv-connect cravv-relay; do
	# Copy then rename, so a running daemon keeps its old file.
	$sudo cp "$tmp/$name/$b" "$bindir/.$b.new"
	$sudo chmod 0755 "$bindir/.$b.new"
	$sudo mv -f "$bindir/.$b.new" "$bindir/$b"
done

say "Installed cravv-connect $tag and cravv-relay to $bindir."
case ":$PATH:" in
*":$bindir:"*) ;;
*)
	say ""
	say "$bindir is not on your PATH. Add it, for example in ~/.profile or ~/.zshrc:"
	say "  export PATH=\"$bindir:\$PATH\""
	;;
esac
say ""
if [ "$upgrade" = 1 ]; then
	say "If the daemon is running, restart it to use the new version:"
	say "  cravv-connect daemon stop && cravv-connect daemon start"
else
	say "Next, on the first machine:  cravv-connect setup"
	say "On every other machine:      cravv-connect setup --join <code>"
fi
