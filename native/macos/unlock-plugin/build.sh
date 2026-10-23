#!/bin/bash
# SPDX-License-Identifier: Apache-2.0
# Build the arm64 unlock plug-in bundle, sign it with the hardened runtime and
# verify the signature. Set LINA_TEAM_ID to LINA's Developer ID team identifier;
# set LINA_SIGN_IDENTITY to the Developer ID Application identity and
# LINA_NOTARY_PROFILE to a notarytool keychain profile to notarize and staple.
set -euo pipefail

root=$(cd "$(dirname "$0")" && pwd)
: "${LINA_TEAM_ID:?set LINA_TEAM_ID to the Developer ID team identifier}"
identity=${LINA_SIGN_IDENTITY:--}
broker=${LINA_BROKER_ID:-com.lina.lock-broker}
bundle="$root/build/LinaUnlock.bundle"

if [[ $(uname -s) != Darwin ]]; then
  echo 'the bundle builds on macOS only' >&2
  exit 1
fi
if [[ $(uname -m) != arm64 ]]; then
  echo 'the bundle builds for arm64 only' >&2
  exit 1
fi

rm -rf "$bundle"
mkdir -p "$bundle/Contents/MacOS"
cp "$root/Info.plist" "$bundle/Contents/Info.plist"

xcrun --sdk macosx clang \
  -arch arm64 -mmacosx-version-min=13.0 \
  -std=c11 -O2 -Wall -Wextra -Werror -fPIC -bundle \
  -I "$root/src" \
  -DLINA_TEAM_ID="\"$LINA_TEAM_ID\"" \
  -DLINA_BROKER_ID="\"$broker\"" \
  -o "$bundle/Contents/MacOS/LinaUnlock" \
  "$root/src/lina_unlock.c" \
  "$root/src/mechanism.c" \
  "$root/src/engine_from_callbacks.c" \
  "$root/src/darwin_host.c" \
  "$root/src/plugin_darwin.c" \
  -framework Security -framework CoreFoundation

sign=(--force --options runtime --sign "$identity" --identifier com.lina.locked-screen.unlock)
if [[ "$identity" != - ]]; then
  sign+=(--timestamp)
fi
codesign "${sign[@]}" "$bundle"
codesign --verify --strict --verbose=2 "$bundle"

if [[ -n "${LINA_NOTARY_PROFILE:-}" ]]; then
  ditto -c -k --keepParent "$bundle" "$root/build/LinaUnlock.zip"
  xcrun notarytool submit "$root/build/LinaUnlock.zip" --keychain-profile "$LINA_NOTARY_PROFILE" --wait
  xcrun stapler staple "$bundle"
fi

lipo -archs "$bundle/Contents/MacOS/LinaUnlock"
echo "$bundle"
