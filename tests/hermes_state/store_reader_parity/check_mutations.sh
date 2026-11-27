#!/usr/bin/env bash
# Run from the checkout root. An unexpected pass, skip, or unrelated error fails this check.
set -euo pipefail
: "${TMPDIR:?set TMPDIR to the disposable run directory}"
scratch=$(mktemp -d "$TMPDIR/store-reader-mutations.XXXXXX")
file=
restore() {
    if [[ -n $file ]]; then
        cp "$scratch/original" "$file"
        cmp "$scratch/original" "$file"
    fi
}
trap restore EXIT
trap 'exit 1' HUP INT TERM

for mutation in scope ordering; do
    case $mutation in
        scope) target=crates/lina-store-reader/src/main.rs; role=bot_b ;;
        ordering) target=crates/lina-store-reader/src/search.sql; role=bot_a ;;
    esac
    cp "$target" "$scratch/original"
    file=$target
    python3 - "$mutation" "$file" <<'PY'
import sys
from pathlib import Path

mutation, filename = sys.argv[1:]
path = Path(filename)
source = path.read_text()
if mutation == "scope":
    # Transaction-local scope expires at commit; subsequent requests lose their own private rows.
    start = "        transaction\n            .query_one("
    end = "        let hits = search("
    assert source.count(start) == source.count(end) == 1
    source = source.replace(start, "        if results.is_empty() {\n" + start).replace(end, "        }\n" + end)
else:
    assert source.count("ORDER BY score DESC, at DESC") == 1
    source = source.replace("ORDER BY score DESC, at DESC", "ORDER BY score DESC")
path.write_text(source)
PY
    log="$scratch/$mutation.log"
    if scripts/run_tests.sh -j 1 --file-retries 0 tests/hermes_state/store_reader_parity/ -- \
        -p tests.hermes_state.store_reader_parity.zone --reader-zone=UTC --tb=short \
        -o "cache_dir=$TMPDIR/pytest-cache" --junitxml="$scratch/$mutation.xml" \
        -o junit_family=legacy > "$log" 2>&1; then
        echo "Expected $mutation to fail. See $log" >&2
        exit 1
    fi
    grep -F "('$role', 'visibility', 'store', 'ordered UID/content hash mismatch')" "$log"
    restore
    file=
    echo "$mutation detected and source restored"
done
