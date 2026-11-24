#!/usr/bin/env bash
set -euo pipefail
cd -- "$(dirname -- "$0")/.."
exec "${MEASURE_PYTHON:-python3}" -B measure/turn_overhead.py "$@"
