#!/bin/sh
# Instalator abcollab dla Linuksa i macOS:
#
#   curl -fsSL https://raw.githubusercontent.com/c-airr/ableton-collab/main/install.sh | sh
#
# Pobiera najnowsze wydanie z GitHub Releases, sprawdza sume sha256 i instaluje
# do ~/.local/bin (dopisuje go do PATH, jesli trzeba). Bez sudo.
#
# Zmienne (glownie do testow):
#   ABCOLLAB_INSTALL_BASE  skad pobierac (domyslnie najnowsze wydanie)
#   ABCOLLAB_INSTALL_DIR   gdzie zainstalowac (domyslnie ~/.local/bin)
#   ABCOLLAB_NO_PATH=1     nie ruszaj PATH ani plikow powloki
set -eu

REPO="c-airr/ableton-collab"
BASE="${ABCOLLAB_INSTALL_BASE:-https://github.com/$REPO/releases/latest/download}"
DEST="${ABCOLLAB_INSTALL_DIR:-$HOME/.local/bin}"

fail() {
	echo "abcollab: $*" >&2
	exit 1
}

case "$(uname -s)" in
Linux) os=linux ;;
Darwin) os=darwin ;;
*) fail "unsupported system $(uname -s) - on Windows use install.ps1" ;;
esac
case "$(uname -m)" in
x86_64 | amd64) arch=amd64 ;;
arm64 | aarch64) arch=arm64 ;;
*) fail "unsupported CPU $(uname -m)" ;;
esac

asset="abcollab-$os-$arch.tar.gz"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT INT TERM

fetch() {
	if command -v curl >/dev/null 2>&1; then
		curl -fsSL "$1" -o "$2"
	elif command -v wget >/dev/null 2>&1; then
		wget -qO "$2" "$1"
	else
		fail "curl or wget is needed"
	fi
}

sha256() {
	if command -v sha256sum >/dev/null 2>&1; then
		sha256sum "$1" | cut -d' ' -f1
	else
		shasum -a 256 "$1" | cut -d' ' -f1
	fi
}

echo "downloading $asset..."
fetch "$BASE/$asset" "$tmp/$asset" || fail "could not download $BASE/$asset"
fetch "$BASE/checksums.txt" "$tmp/checksums.txt" || fail "could not download the checksums"
want="$(grep " $asset\$" "$tmp/checksums.txt" | cut -d' ' -f1)"
got="$(sha256 "$tmp/$asset")"
[ -n "$want" ] && [ "$want" = "$got" ] || fail "wrong sha256 of the downloaded $asset - aborting"

tar -xzf "$tmp/$asset" -C "$tmp"
mkdir -p "$DEST"
cp "$tmp/abcollab" "$DEST/abcollab.new"
chmod 755 "$DEST/abcollab.new"
mv -f "$DEST/abcollab.new" "$DEST/abcollab"
echo "installed: $DEST/abcollab ($("$DEST/abcollab" version 2>/dev/null || echo '?'))"

if [ "${ABCOLLAB_NO_PATH:-}" != "1" ]; then
	case ":$PATH:" in
	*":$DEST:"*) ;;
	*)
		case "${SHELL:-}" in
		*zsh) rc="$HOME/.zshrc" ;;
		*bash) rc="$HOME/.bashrc" ;;
		*) rc="$HOME/.profile" ;;
		esac
		line="export PATH=\"$DEST:\$PATH\" # abcollab"
		if ! grep -qs "# abcollab" "$rc"; then
			printf '\n%s\n' "$line" >>"$rc"
			echo "added $DEST to PATH in $rc"
		fi
		echo "open a new terminal (or run: . $rc) so the command is found"
		;;
	esac
fi
echo
echo "Done. Type: abcollab"
