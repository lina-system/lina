#!/usr/bin/env bash
# SPDX-License-Identifier: Apache-2.0
# Reproduce the checked-in engine on Linux x86-64; never overwrite an output.
set -euo pipefail
[[ $# == 1 && ! -e $1 ]] || { echo 'usage: build.sh <new-output.wasm>' >&2; exit 64; }
output=$(realpath -m -- "$1")
work=$(mktemp -d)
trap 'rm -rf -- "$work"' EXIT
cd "$work"

fetch() { curl -fLsS --retry 3 "$1" -o "$2"; }
check() { printf '%s  %s\n' "$1" "$2" | sha256sum -c -; }
module() {
  GOWORK=off go mod download -json "$1" > module.json
  python3 - "$2" <<'PY'
import json, sys
with open('module.json') as f:
    module = json.load(f)
assert module['Sum'] == sys.argv[1], 'Go module checksum differs from pin'
print(module['Dir'])
PY
}
binding=$(module github.com/ncruces/go-sqlite3@v0.30.5 h1:6usmTQ6khriL8oWilkAZSJM/AIpAlVL2zFrlcpDldCE=)
vector=$(module github.com/asg017/sqlite-vec-go-bindings@v0.1.6 h1:Nx0jAzyS38XpkKznJ9xQjFXz2X9tI7KqjwVxV8RNoww=)
cp -R "$binding" binding
chmod -R u+w binding
mkdir -p binding/tools binding/vfs/tests/mptest/wasm binding/vfs/tests/speedtest1/wasm
fetch https://github.com/WebAssembly/wasi-sdk/releases/download/wasi-sdk-29/wasi-sdk-29.0-x86_64-linux.tar.gz wasi.tar.gz
check 87d1d1a2879d139cdc624b968efad3d4a97b8078cdff95e63ac88ecafd1a0171 wasi.tar.gz
tar -xzf wasi.tar.gz
mv wasi-sdk-29.0-x86_64-linux binding/tools/wasi-sdk
fetch https://github.com/WebAssembly/binaryen/releases/download/version_125/binaryen-version_125-x86_64-linux.tar.gz binaryen.tar.gz
check 7c3bc16599c8274a04d34a504fe4be2047884f900e0e2da2f6fb9cd667183be4 binaryen.tar.gz
tar -xzf binaryen.tar.gz
mv binaryen-version_125 binding/tools/binaryen

cd binding/sqlite3
fetch https://sqlite.org/2026/sqlite-autoconf-3510200.tar.gz sqlite.tar.gz
[[ $(openssl dgst -sha3-256 sqlite.tar.gz) == *e0f7ae1c28c4fa551a2ffe8bdfafafa90613dabcef9553050892d02240b44f1d ]] || exit 1
tar -xzf sqlite.tar.gz
cp sqlite-autoconf-3510200/sqlite3{.c,.h,ext.h} .
mkdir -p ext
for name in anycollseq base64 decimal ieee754 regexp series spellfix uint; do
  fetch "https://raw.githubusercontent.com/sqlite/sqlite/version-3.51.2/ext/misc/$name.c" "ext/$name.c"
done
cat busy_timeout.patch vfs_find.patch | patch -p0 --no-backup-if-mismatch
cp "$vector/cgo/sqlite-vec.c" "$vector/cgo/sqlite-vec.h" .
python3 - <<'PY'
from pathlib import Path
p = Path('main.c')
p.write_text(p.read_text().replace('// Bindings', '#include "sqlite-vec.c"\n// Bindings')
    .replace('  sqlite3_initialize();', '  sqlite3_initialize();\n  sqlite3_auto_extension((void (*)(void))sqlite3_vec_init);'))
PY
cd ..
bash embed/build.sh
check 2eaa33d000922ae5369d6fbf55be07ae03f68ebb7bf82ec49e2657282e37718a embed/sqlite3.wasm
# The final digest also verifies every source input against the known engine.
(set -o noclobber; cat embed/sqlite3.wasm > "$output")
