#!/bin/sh
set -eu

REPO="Stack-Cairn/K-brain"
API="https://api.github.com/repos/$REPO"

say() { printf '  %s\n' "$*"; }
die() { printf 'k-brain install: %s\n' "$*" >&2; exit 1; }
need() { command -v "$1" >/dev/null 2>&1 || die "required command not found: $1"; }

need uname
need mktemp
need curl
TOKEN=""
if command -v gh >/dev/null 2>&1 && gh auth status >/dev/null 2>&1; then
  TOKEN=$(gh auth token)
elif [ -n "${GH_TOKEN:-}" ]; then
  TOKEN="$GH_TOKEN"
fi
api() {
  if [ -n "$TOKEN" ]; then
    curl -fsSL -H "Authorization: Bearer $TOKEN" -H "Accept: application/vnd.github+json" "$API/$1"
  else
    curl -fsSL -H "Accept: application/vnd.github+json" "$API/$1"
  fi
}
api_asset() {
  if [ -n "$TOKEN" ]; then
    curl -fsSL -H "Authorization: Bearer $TOKEN" -H "Accept: application/octet-stream" "$API/releases/assets/$1" -o "$2"
  else
    curl -fsSL -H "Accept: application/octet-stream" "$API/releases/assets/$1" -o "$2"
  fi
}
if command -v sha256sum >/dev/null 2>&1; then SHA() { sha256sum "$1" | cut -d' ' -f1; }
elif command -v shasum >/dev/null 2>&1; then SHA() { shasum -a 256 "$1" | cut -d' ' -f1; }
else die "need sha256sum or shasum"; fi
os=$(uname -s); arch=$(uname -m)
case "$os" in Linux) os=linux;; Darwin) os=darwin;; *) die "unsupported OS: $os (Linux/macOS only)";; esac
case "$arch" in x86_64|amd64) arch=x64;; arm64|aarch64) arch=arm64;; *) die "unsupported arch: $arch";; esac
ASSET="k-brain-$os-$arch"
HELPER_ASSET="k-brain-computer-$os-$arch"
VERSION="${K_BRAIN_VERSION:-}"
say "Resolving ${VERSION:-latest} release..."
if [ -n "$VERSION" ]; then
  REL=$(api "releases/tags/$VERSION") || die "release $VERSION not found"
else
  REL=$(api "releases/latest") || die "could not reach the releases API"
fi
[ -n "$REL" ] || die "empty release response"
json_field() {
  printf '%s' "$REL" | python3 -c "import json,sys; r=json.load(sys.stdin); print($1)" 2>/dev/null
}
if command -v python3 >/dev/null 2>&1; then
  VERSION=$(json_field "r['tag_name']")
BIN_ID=$(json_field "next(a['id'] for a in r['assets'] if a['name']=='$ASSET')")
  HELPER_ID=$(json_field "next(a['id'] for a in r['assets'] if a['name']=='$HELPER_ASSET')")
  SUMS_ID=$(json_field "next(a['id'] for a in r['assets'] if a['name']=='SHA256SUMS')")
else
  VERSION=$(printf '%s' "$REL" | grep '"tag_name"' | head -1 | sed 's/.*"tag_name": *"\([^"]*\)".*/\1/')
  BIN_ID=$(printf '%s' "$REL" | tr -d '\n' | sed 's/.*"assets": *\[//' | sed 's/},{/}\n{/g' \
    | grep "\"name\": *\"$ASSET\"" | head -1 | sed 's/^[^{]*{[^i]*"id": *\([0-9]*\).*/\1/')
  HELPER_ID=$(printf '%s' "$REL" | tr -d '\n' | sed 's/.*"assets": *\[//' | sed 's/},{/}\n{/g' \
    | grep "\"name\": *\"$HELPER_ASSET\"" | head -1 | sed 's/^[^{]*{[^i]*"id": *\([0-9]*\).*/\1/')
  SUMS_ID=$(printf '%s' "$REL" | tr -d '\n' | sed 's/.*"assets": *\[//' | sed 's/},{/}\n{/g' \
    | grep '"name": *"SHA256SUMS"' | head -1 | sed 's/^[^{]*{[^i]*"id": *\([0-9]*\).*/\1/')
fi
[ -n "$VERSION" ] || die "could not determine the latest release (set K_BRAIN_VERSION)"
[ -n "$BIN_ID" ] || die "no asset $ASSET in release $VERSION"
[ -n "$HELPER_ID" ] || die "no asset $HELPER_ASSET in release $VERSION"
[ -n "$SUMS_ID" ] || die "no SHA256SUMS in release $VERSION"

printf '\nk-brain %s — %s\n' "$VERSION" "$ASSET"
tmp=$(mktemp -d); trap 'rm -rf "$tmp"' EXIT
say "Downloading $ASSET..."
api_asset "$BIN_ID" "$tmp/$ASSET" || die "download failed"
say "Downloading $HELPER_ASSET..."
api_asset "$HELPER_ID" "$tmp/$HELPER_ASSET" || die "helper download failed"
say "Downloading SHA256SUMS..."
api_asset "$SUMS_ID" "$tmp/SHA256SUMS" || die "checksum list download failed"

expected=$(grep " $ASSET\$" "$tmp/SHA256SUMS" | cut -d' ' -f1)
[ -n "$expected" ] || die "no checksum for $ASSET in SHA256SUMS"
actual=$(SHA "$tmp/$ASSET")
say "expected sha256: $expected"
say "actual   sha256: $actual"
[ "$expected" = "$actual" ] || die "CHECKSUM MISMATCH — refusing to install. The download does not match the published checksum."
say "OK: checksum verified"
helper_expected=$(grep " $HELPER_ASSET\$" "$tmp/SHA256SUMS" | cut -d' ' -f1)
[ -n "$helper_expected" ] || die "no checksum for $HELPER_ASSET in SHA256SUMS"
helper_actual=$(SHA "$tmp/$HELPER_ASSET")
[ "$helper_expected" = "$helper_actual" ] || die "helper checksum mismatch"
chmod 755 "$tmp/$ASSET"
in_path() { case ":$PATH:" in *":$1:"*) return 0;; *) return 1;; esac; }
DEST=""
if [ -n "${K_BRAIN_BIN_DIR:-}" ]; then
  mkdir -p "$K_BRAIN_BIN_DIR" 2>/dev/null || true
  DEST="$K_BRAIN_BIN_DIR"
else
  for d in /usr/local/bin /opt/homebrew/bin "$HOME/.local/bin" "$HOME/bin"; do
    if [ -d "$d" ] && [ -w "$d" ]; then DEST="$d"; break; fi
  done
  [ -z "$DEST" ] && { mkdir -p "$HOME/.local/bin" && DEST="$HOME/.local/bin"; }
fi
[ -n "$DEST" ] && [ -w "$DEST" ] || die "no writable install directory found"

mv "$tmp/$ASSET" "$DEST/kn"
mv "$tmp/$HELPER_ASSET" "$DEST/k-brain-computer"
chmod 755 "$DEST/k-brain-computer"
say "OK: installed to $DEST/kn"
if ! in_path "$DEST"; then
  esc=$(printf '%s' "$DEST" | sed "s/'/'\\\\''/g")
  line="export PATH='$esc':\"\$PATH\""
  touch "$HOME/.profile" 2>/dev/null || true
  for rc in "$HOME/.zshrc" "$HOME/.bashrc" "$HOME/.profile"; do
    [ -e "$rc" ] || continue
    grep -qF "$line" "$rc" 2>/dev/null || printf '\n# k-brain\n%s\n' "$line" >> "$rc"
  done
  say "Added $DEST to your PATH — restart your shell, or run now: $line"
fi

printf '\n'
"$DEST/kn" --version || true

if [ "$os" = "darwin" ] && [ "$arch" = "arm64" ]; then
  cat <<'EOF'

macOS notes:
  • First run may trigger Gatekeeper — if "kn" is blocked, allow it in
    System Settings → Privacy & Security, or run: xattr -d com.apple.quarantine $(which kn)
  • The Go computer helper's macOS native automation backend is not implemented yet.
EOF
fi

printf '\nDone. Run `kn` to start.\n'
