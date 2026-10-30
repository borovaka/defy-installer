#!/usr/bin/env bash
# SPDX-License-Identifier: MIT
#
# Builds defy-installer for each OS and architecture that a release
# covers, into the folder given, from the repository's root:
#
#   packaging/build.sh dist
#
# The binaries are named defy-installer-<os>-<arch>, with .exe for
# Windows. The installer uses no cgo, so one machine builds them all.
# -trimpath leaves this machine's paths out of the binaries, and -s -w the
# symbol table and debugger information, about a third of their size. A
# panic still names its functions and lines, and go version -m still lists
# the modules.
set -euo pipefail

if [ $# -ne 1 ]; then
  echo "usage: packaging/build.sh <folder>" >&2
  exit 2
fi
out=$1
mkdir -p "$out"
for target in darwin/amd64 darwin/arm64 linux/amd64 linux/arm64 windows/amd64 windows/arm64; do
  os=${target%/*}
  arch=${target#*/}
  ext=
  if [ "$os" = windows ]; then
    ext=.exe
  fi
  CGO_ENABLED=0 GOOS=$os GOARCH=$arch go build -trimpath -ldflags='-s -w' -o "$out/defy-installer-$os-$arch$ext" .
done
ls -l "$out"
