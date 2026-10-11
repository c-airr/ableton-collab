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
*) fail "nieobslugiwany system $(uname -s) - na Windowsie uzyj install.ps1" ;;
esac
case "$(uname -m)" in
x86_64 | amd64) arch=amd64 ;;
arm64 | aarch64) arch=arm64 ;;
*) fail "nieobslugiwany procesor $(uname -m)" ;;
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
		fail "potrzebny curl albo wget"
	fi
}

sha256() {
	if command -v sha256sum >/dev/null 2>&1; then
		sha256sum "$1" | cut -d' ' -f1
	else
		shasum -a 256 "$1" | cut -d' ' -f1
	fi
}

echo "pobieranie $asset..."
fetch "$BASE/$asset" "$tmp/$asset" || fail "nie udalo sie pobrac $BASE/$asset"
fetch "$BASE/checksums.txt" "$tmp/checksums.txt" || fail "nie udalo sie pobrac sum kontrolnych"
want="$(grep " $asset\$" "$tmp/checksums.txt" | cut -d' ' -f1)"
got="$(sha256 "$tmp/$asset")"
[ -n "$want" ] && [ "$want" = "$got" ] || fail "zla suma sha256 pobranego $asset - przerywam"

tar -xzf "$tmp/$asset" -C "$tmp"
mkdir -p "$DEST"
cp "$tmp/abcollab" "$DEST/abcollab.new"
chmod 755 "$DEST/abcollab.new"
mv -f "$DEST/abcollab.new" "$DEST/abcollab"
echo "zainstalowano: $DEST/abcollab ($("$DEST/abcollab" version 2>/dev/null || echo '?'))"

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
			echo "dopisano $DEST do PATH w $rc"
		fi
		echo "otworz nowy terminal (albo: . $rc), zeby komenda byla widoczna"
		;;
	esac
fi
echo
echo "Gotowe. Wpisz: abcollab"
