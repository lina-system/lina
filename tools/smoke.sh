#!/bin/bash
# Manual GUI smoke test only. Requires a running, policy-enabled, granted helper.
# Usage: tools/smoke.sh BUNDLE_ID
# Open the app manually first. Screen input is served only by cua-driver.
set -euo pipefail
if [[ $# -ne 1 ]]; then
    echo "Usage: $0 BUNDLE_ID" >&2
    exit 2
fi
ctl=${LINA_HELPERCTL:-lina-helperctl}
bundle_id=$1
smoke_dir=$(mktemp -d "${TMPDIR:-/tmp}/lina-helper-smoke.XXXXXX")
trap 'rm -rf "$smoke_dir"' EXIT
run() {
    printf '\n== %s ==\n' "$1"
    "$ctl" "$@"
}
run status
run permissions
printf '\n== screenshot (PNG stdout, JSON last on stderr) ==\n'
"$ctl" screenshot --arg display=main --out - > "$smoke_dir/screenshot.png" 2> "$smoke_dir/screenshot.stderr" || {
    cat "$smoke_dir/screenshot.stderr" >&2
    exit 1
}
cat "$smoke_dir/screenshot.stderr"
python3 - "$smoke_dir" <<'PY'
import json, pathlib, sys
folder = pathlib.Path(sys.argv[1])
assert (folder / 'screenshot.png').read_bytes().startswith(b'\x89PNG\r\n\x1a\n')
response = json.loads((folder / 'screenshot.stderr').read_text().splitlines()[-1])
assert response['ok'] and response['result']['width'] > 0 and response['result']['height'] > 0
print('PNG signature and stderr response: OK')
PY
run ax.tree --arg "app=$bundle_id" --json '{"depth":8,"max_nodes":2000}'
run cua.status
echo 'All helper read actions succeeded.'
