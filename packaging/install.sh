#!/bin/sh
# SPDX-License-Identifier: MIT
#
# Installs defy-installer on macOS or Linux from its latest release:
#
#   curl -fsSL https://github.com/borovaka/defy-installer/releases/latest/download/install.sh | sh
#
# It downloads the archive for this computer, checks it against the
# release's SHA256SUMS and unpacks it into ~/defy-installer, keeping the
# defy-installer.yaml there. DEFY_INSTALLER_DIR and DEFY_INSTALLER_BASE
# change the folder and the download URL. Everything runs from main, so a
# download cut short runs nothing.

set -eu

die() {
  echo "install.sh: $*" >&2
  exit 1
}

main() {
  base=${DEFY_INSTALLER_BASE:-https://github.com/borovaka/defy-installer/releases/latest/download}
  dest=${DEFY_INSTALLER_DIR:-$HOME/defy-installer}
  case $(uname -s)/$(uname -m) in
  Darwin/*) archive=defy-installer-macos.zip ;;
  Linux/x86_64) archive=defy-installer-linux-x64.tar.gz ;;
  Linux/aarch64 | Linux/arm64) archive=defy-installer-linux-arm64.tar.gz ;;
  *) die "there is no defy-installer for $(uname -s) on $(uname -m)" ;;
  esac

  tmp=$(mktemp -d "${TMPDIR:-/tmp}/defy-installer.XXXXXX")
  trap 'rm -rf "$tmp"' EXIT
  curl -fsSL -o "$tmp/$archive" "$base/$archive"
  curl -fsSL -o "$tmp/SHA256SUMS" "$base/SHA256SUMS"
  want=$(awk -v f="$archive" '$2 == f { print $1 }' "$tmp/SHA256SUMS")
  got=$( (sha256sum "$tmp/$archive" 2>/dev/null || shasum -a 256 "$tmp/$archive") | cut -d ' ' -f 1)
  if [ -z "$want" ] || [ "$got" != "$want" ]; then
    die "$archive does not match the release's SHA256SUMS"
  fi

  # Files that curl downloads and unzip unpacks carry no quarantine mark, so
  # macOS starts the program without asking.
  case $archive in
  *.zip) unzip -q "$tmp/$archive" -d "$tmp" ;;
  *) tar -xzf "$tmp/$archive" -C "$tmp" ;;
  esac
  mkdir -p "$dest"
  for file in "$tmp"/defy-installer/*; do
    name=${file##*/}
    if [ "$name" = defy-installer.yaml ] && [ -e "$dest/$name" ]; then
      continue
    fi
    # A rename, not an overwrite: Linux refuses to overwrite a running
    # program, and macOS can kill a signed one changed in place.
    cp "$file" "$dest/.$name.new"
    mv -f "$dest/.$name.new" "$dest/$name"
  done

  echo "Installed $("$dest/defy-installer" --version) in $dest. Next, run:"
  echo "  cd \"$dest\" && ./defy-installer install    # or update, on a keyboard that runs ZMK"
}

main
