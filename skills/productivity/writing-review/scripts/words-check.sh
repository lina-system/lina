#!/usr/bin/env bash
# Usage: words-check.sh FILE...; exits 0 clean, 1 matches, 2 errors.
set -eu
if [ "$#" -eq 0 ]; then
  printf '%s\n' 'Usage: words-check.sh FILE...' >&2
  exit 2
fi
D="$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)"
P=$(grep -v -e '^#' -e '^[[:space:]]*$' "$D/references/banned-words.txt")
status=0
grep -n -i -F -e "$P" -- "$@" || status=$?
case "$status" in
  0) printf '%s\n' 'Style warnings found; review each match in context.' >&2; exit 1 ;;
  1) exit 0 ;;
  *) exit 2 ;;
esac
