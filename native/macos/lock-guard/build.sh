#!/bin/bash
# SPDX-License-Identifier: Apache-2.0
# Build the arm64 lock guard, the installer and the rollback command, sign them
# with the hardened runtime and verify the signatures.
#
# Set LINA_TEAM_ID to LINA's Developer ID team identifier; set LINA_SIGN_IDENTITY
# to the Developer ID Application identity and LINA_NOTARY_PROFILE to a
# notarytool keychain profile to notarize and staple. The unlock plug-in bundle
# is built by ../unlock-plugin/build.sh and must be built first.
set -euo pipefail

root=$(cd "$(dirname "$0")" && pwd)
: "${LINA_TEAM_ID:?set LINA_TEAM_ID to LINA's Developer ID team identifier}"
identity=${LINA_SIGN_IDENTITY:--}
out="$root/build"

if [[ $(uname -s) != Darwin ]]; then
  echo 'the guard builds on macOS only' >&2
  exit 1
fi
if [[ $(uname -m) != arm64 ]]; then
  echo 'the guard builds for arm64 only' >&2
  exit 1
fi

mkdir -p "$out"

build() {
  local name=$1
  shift
  xcrun --sdk macosx clang \
    -arch arm64 -mmacosx-version-min=13.0 \
    -std=c11 -O2 -Wall -Wextra -Werror \
    -I "$root/src" \
    -DLINA_TEAM_ID="\"$LINA_TEAM_ID\"" \
    -o "$out/$name" "$@"
  local sign=(--force --options runtime --sign "$identity" --identifier "com.lina.locked-screen.$name")
  if [[ "$identity" != - ]]; then
    sign+=(--timestamp)
  fi
  codesign "${sign[@]}" "$out/$name"
  codesign --verify --strict --verbose=2 "$out/$name"
}

build guard \
  "$root/src/guard_main.c" \
  "$root/src/guard_entry.c" \
  "$root/src/guard.c" \
  "$root/src/install.c" \
  "$root/src/restore.c" \
  "$root/src/rule.c" \
  "$root/src/state.c" \
  "$root/src/sha256.c" \
  "$root/src/darwin_host.c" \
  -framework Security -framework CoreFoundation -framework CoreGraphics

build lina-locked-screen \
  "$root/src/installer_main.c" \
  "$root/src/guard_main.c" \
  "$root/src/guard.c" \
  "$root/src/install.c" \
  "$root/src/restore.c" \
  "$root/src/rule.c" \
  "$root/src/state.c" \
  "$root/src/sha256.c" \
  "$root/src/darwin_host.c" \
  -framework Security -framework CoreFoundation -framework CoreGraphics

cp "$root/com.lina.locked-screen.guard.plist" "$out/com.lina.locked-screen.guard.plist"
cp "$root/measured-builds.txt" "$out/measured-builds.txt"

echo "$out"
