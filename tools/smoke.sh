#!/bin/bash
# Manual GUI smoke test only. Requires a running, policy-enabled, granted helper.
# Usage: tools/smoke.sh BUNDLE_ID AX_PATH X Y [EXPECT_ROLE]
# Choose a harmless press target and click point in a disposable test window.
# The script activates the app, presses the supplied element, clicks, and types.
set -euo pipefail
if [[ $# -lt 4 || $# -gt 5 ]]; then
    echo "Usage: $0 BUNDLE_ID AX_PATH X Y [EXPECT_ROLE]" >&2
    exit 2
fi
ctl=${LINA_HELPERCTL:-lina-helperctl}
bundle_id=$1
ax_path=$2
click_x=$3
click_y=$4
expect_role=${5:-}
smoke_dir=$(mktemp -d "${TMPDIR:-/tmp}/lina-helper-smoke.XXXXXX")
trap 'rm -rf "$smoke_dir"' EXIT
run() {
    printf '\n== %s ==\n' "$1"
    "$ctl" "$@"
}
run status
run permissions
run app.open --arg "bundle_id=$bundle_id"
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
press_args=(ax.press --arg "app=$bundle_id" --arg "path=$ax_path")
if [[ -n "$expect_role" ]]; then press_args+=(--arg "expect_role=$expect_role"); fi
run "${press_args[@]}"
click_json=$(python3 - "$click_x" "$click_y" <<'PY'
import json, sys
print(json.dumps(dict(x=float(sys.argv[1]), y=float(sys.argv[2]), button='left', count=1), allow_nan=False))
PY
)
run input.click --json "$click_json"
run input.keys --json '{"text":"LINA helper smoke test"}'
run input.keys --json '{"key":"tab","modifiers":[]}'
echo 'All smoke actions succeeded.'
