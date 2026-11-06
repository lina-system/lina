"""The bash a file node runs (``file_node.py``): short scripts the hub sends over ssh, and the run's ``runner.sh``.

The node offers bash 4.4, GNU find, tar, sha256sum, setsid, nohup, curl and coreutils; it has no ``timeout``,
``pgrep``, ``getconf``, git, service manager or recent Python, and its login shell is not bash. Every script is run
as ``bash -c <script> lina-file-node <args>`` and takes paths relative to ``$HOME`` (the hub never knows the node's
home). omo writes its answers to a file first: through a pipe it stops after the first 64 KiB.
"""

from __future__ import annotations

# The durable session id from omo's open answer, without a JSON parser on the node.
_REF = r"""ref() { sed -n 's/.*"durableSessionId"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' "$T/session.json" 2>/dev/null | head -n 1; }
"""

# A process is the one recorded when its start time (/proc/<pid>/stat field 22) still matches: a reused pid is not.
_START = r"""start_of() { sed 's/.*) //' "/proc/$1/stat" 2>/dev/null | cut -d' ' -f20; }
"""

FACTS = r"""OMO=${1/#\~/$HOME}; URL=$2
export PATH="$HOME/.bun/bin:$HOME/.local/bin:$PATH"
v=""; if command -v "$OMO" >/dev/null 2>&1; then v=$("$OMO" --version < /dev/null 2>/dev/null | head -n 1); [ -n "$v" ] || v="?"; fi
printf 'omo=%s\n' "$v"
printf 'model=%s\n' "$(curl -s -o /dev/null -m 5 -w '%{http_code}' "$URL" 2>/dev/null)"
printf 'free_kb=%s\n' "$(df -Pk "$HOME" 2>/dev/null | tail -n 1 | tr -s ' ' | cut -d' ' -f4)"
printf 'cores=%s\n' "$(grep -c '^processor' /proc/cpuinfo 2>/dev/null)"
printf 'load=%s\n' "$(cut -d' ' -f1 /proc/loadavg 2>/dev/null)"
printf 'bash=%s\n' "$BASH_VERSION"
"""

# stdin: a tar of the task files for this run (id, prompt.<n>, runner.sh, and work/input for run 1).
# A run that already has its pid file was started by an earlier call whose answer was lost: it is adopted, never
# started twice.
LAUNCH = r"""set -e
T="$HOME/$1"; N=$2; shift 2
""" + _START + r"""mkdir -p "$T"
if [ -s "$T/pid.$N" ]; then cat > /dev/null; printf 'started=%s\nadopted=1\n' "$(cat "$T/pid.$N")"; exit 0; fi
tar -xf - -C "$T"
[ -d "$T/work/input" ] && chmod -R a-w "$T/work/input"
mkdir -p "$T/work/out"
rm -f "$T/rc.$N" "$T/outcome.$N"
nohup setsid bash "$T/runner.sh" "$T" "$N" "$@" > "$T/runner.$N.log" 2>&1 < /dev/null &
pid=$!
echo "$pid" > "$T/pid.$N"
start_of "$pid" > "$T/pidstart.$N"
printf 'started=%s\n' "$pid"
"""

# runner.sh <task dir> <run> <omo> <poll s> <hard limit s> [<provider/model>]: one run, then its end marks.
# It only drives omo and writes files; the hub judges the end from what it left (rc.<n> is written last).
RUNNER = r"""#!/usr/bin/env bash
set -u
T=$1; N=$2; OMO=${3/#\~/$HOME}; POLL=$4; HARD=$5; MODEL=${6:-}
export PATH="$HOME/.bun/bin:$HOME/.local/bin:$PATH"
cd "$T/work" || exit 1
date +%s > "$T/started.$N"
o() { local f=$1; shift; "$OMO" "$@" < /dev/null > "$f" 2>> "$T/stderr.$N.txt"; }
""" + _REF + r"""if [ ! -s "$T/session.json" ]; then
  margs=(); [ -n "$MODEL" ] && margs=(--model "$MODEL")
  o "$T/open.$N.json" host session open --cwd "$T/work" --name "$(cat "$T/id")" ${margs[@]+"${margs[@]}"} --prompt @"$T/prompt.$N" --json
  rc=$?
  [ "$rc" = 0 ] && cp "$T/open.$N.json" "$T/session.json"
else
  o "$T/open.$N.json" host session prompt "$(ref)" @"$T/prompt.$N" --json
  rc=$?
fi
date +%s > "$T/accepted.$N"
outcome=start_failed
if [ "$rc" = 0 ]; then
  outcome=timeout
  while [ "$SECONDS" -lt "$HARD" ]; do
    if [ -f "$T/cancelled" ]; then outcome=cancelled; break; fi
    o "$T/state.$N.json" host session state "$(ref)" --json
    if grep -Eq '"state"[[:space:]]*:[[:space:]]*"(done|failed|cancelled)"' "$T/state.$N.json"; then outcome=sealed; break; fi
    if ! grep -q '"lifecycle"' "$T/state.$N.json" && grep -Eq '"isStreaming"[[:space:]]*:[[:space:]]*false' "$T/state.$N.json"; then
      outcome=sealed; break
    fi
    o "$T/wait.$N.json" host session wait "$(ref)" --until idle --timeout 60000 --json
    sleep "$POLL"
  done
  if [ "$outcome" = timeout ]; then
    # At the hard limit the session is stopped before the end marks, so it cannot change files behind them.
    o "$T/abort.$N.json" host session abort "$(ref)" --json
    o "$T/stop.$N.json" host session wait "$(ref)" --until idle --timeout 30000 --json
    o "$T/state.$N.json" host session state "$(ref)" --json
    if grep -Eq '"isStreaming"[[:space:]]*:[[:space:]]*true|"state"[[:space:]]*:[[:space:]]*"running"' "$T/state.$N.json"; then
      outcome=timeout_not_stopped
    else
      outcome=timeout_stopped
    fi
  fi
fi
[ -s "$T/session.json" ] && o "$T/messages.$N.json" host session read "$(ref)" --messages --json
echo "$outcome" > "$T/outcome.$N"
date +%s > "$T/ended.$N"
echo "$rc" > "$T/rc.$N"
"""

STATUS = r"""T="$HOME/$1"; N=$2
""" + _START + r"""[ -d "$T" ] || { echo missing=1; exit 0; }
printf 'rc=%s\n' "$(cat "$T/rc.$N" 2>/dev/null)"
pid=$(cat "$T/pid.$N" 2>/dev/null); want=$(cat "$T/pidstart.$N" 2>/dev/null); now=""
[ -n "$pid" ] && now=$(start_of "$pid")
if [ -n "$now" ] && [ "$now" = "$want" ]; then echo alive=1; else echo alive=0; fi
printf 'lifecycle=%s\n' "$(tail -n 1 "$T/state.$N.json" 2>/dev/null)"
"""

# stdout: a tar of what the run left, read by the hub once rc.<n> is there.
COLLECT = r"""T="$HOME/$1"; N=$2
cd "$T" || exit 1
if [ -d work/input ]; then (cd work/input && find . -type f -exec sha256sum {} +) > "inputs.$N.sha256"; else : > "inputs.$N.sha256"; fi
set --
for f in work/report.md work/out "messages.$N.json" "state.$N.json" "stderr.$N.txt" "started.$N" "accepted.$N" \
         "ended.$N" "outcome.$N" "rc.$N" "inputs.$N.sha256"; do
  [ -e "$f" ] && set -- "$@" "$f"
done
tar -cf - "$@"
"""

# stdin: the text to steer into the live turn.
STEER = r"""T="$HOME/$1"; OMO=${2/#\~/$HOME}
export PATH="$HOME/.bun/bin:$HOME/.local/bin:$PATH"
""" + _REF + r"""f="$T/steer.$(date +%s%N)"; cat > "$f"
r=$(ref); [ -n "$r" ] || { echo '{"action":"error","reason":"no session"}'; exit 1; }
"$OMO" host session steer "$r" @"$f" --json < /dev/null > "$f.json" 2>&1; rc=$?
tail -n 1 "$f.json"; exit "$rc"
"""

# stdin: why. The runner sees the cancel mark on its next round; the abort ends the turn now.
CANCEL = r"""T="$HOME/$1"; OMO=${2/#\~/$HOME}
export PATH="$HOME/.bun/bin:$HOME/.local/bin:$PATH"
""" + _REF + r"""[ -d "$T" ] || { echo missing=1; exit 0; }
cat > "$T/cancelled"
r=$(ref); [ -n "$r" ] && "$OMO" host session abort "$r" --json < /dev/null > "$T/abort.json" 2>&1
echo cancelled=1
"""

# abort <task dir> <omo>: stop the session's turn once more (a run whose session did not stop at the hard limit).
ABORT = r"""T="$HOME/$1"; OMO=${2/#\~/$HOME}
export PATH="$HOME/.bun/bin:$HOME/.local/bin:$PATH"
""" + _REF + r"""r=$(ref); [ -n "$r" ] || exit 0
"$OMO" host session abort "$r" --json < /dev/null > "$T/abort.again.json" 2>&1; rc=$?
tail -n 1 "$T/abort.again.json"; exit "$rc"
"""

# deny_scan <task dir> <run> <limit s> <n roots> <roots…> <n names> <names…> <n skips> <skips…> <volumes…>: files
# changed since the run started under the denied folders (absolute roots, and folders named like a pattern up to 3
# levels into a volume). Changed means a newer mtime or ctime: a file moved or renamed into a denied folder keeps its
# mtime but gets a new ctime. A folder moved in gets a new ctime while its files keep theirs, so a folder of the run's
# user whose ctime alone is newer is listed (with a trailing /) together with everything under it. <skips> are bind-mount aliases of
# undenied folders (``file_node_deny.aliases``). Each line is "<owner uid><TAB><path>"; the hub counts only the run's
# own uid (``file_node_deny.owned``). The finds' exit codes land in deny_rc.<n> and their errors in deny_err.<n>: a
# folder the run's user can neither read nor write could not have been changed by the run and counts as seen; any
# other error means the look did not see everything.
# The look runs in its own session and is killed at the limit: there is no timeout(1) on the node.
DENY_SCAN = r"""T="$HOME/$1"; N=$2; LIMIT=$3; shift 3
nr=$1; shift; roots=("${@:1:$nr}"); shift "$nr"
nn=$1; shift; names=("${@:1:$nn}"); shift "$nn"
np=$1; shift; skips=("${@:1:$np}"); shift "$np"
[ -f "$T/started.$N" ] || { echo complete=0; echo 'why=the run left no start mark'; exit 0; }
cat > "$T/deny_scan.sh" <<'EOF'
export LC_ALL=C
T=$1; N=$2; nr=$3; shift 3
roots=("${@:1:$nr}"); shift "$nr"; nn=$1; shift; names=("${@:1:$nn}"); shift "$nn"
np=$1; shift; skips=("${@:1:$np}"); shift "$np"
S="$T/started.$N"; E="$T/deny_err.$N"; : > "$E"
skip=(-path "$T"); for p in "${skips[@]}"; do skip+=(-o -path "$p"); done
found=(); d=0; s=0; m=0
for r in "${roots[@]}"; do [ -e "$r" ] && found+=("$r"); done
if [ "$nn" -gt 0 ] && [ "$#" -gt 0 ]; then
  expr=(); for p in "${names[@]}"; do [ "${#expr[@]}" -gt 0 ] && expr+=(-o); expr+=(-name "$p"); done
  find "$@" -maxdepth 3 \( "${skip[@]}" \) -prune -o -type d \( "${expr[@]}" \) -prune -print > "$T/deny_dirs.$N.txt" 2>> "$E"; d=$?
  while IFS= read -r dir; do found+=("$dir"); done < "$T/deny_dirs.$N.txt"
fi
: > "$T/moved.$N.txt"
if [ "${#found[@]}" -gt 0 ]; then
  find "${found[@]}" \( "${skip[@]}" \) -prune -o \( -newer "$S" -o -cnewer "$S" \) \( -type f -o -type l \) \
    -printf '%U\t%p\n' -o -type d -uid "$EUID" -cnewer "$S" ! -newer "$S" -printf '%U\t%p/\n' -fprint "$T/moved.$N.txt" \
    2>> "$E"; s=$?
  while IFS= read -r dir; do
    find "$dir" \( "${skip[@]}" \) -prune -o \( -type f -o -type l \) -printf '%U\t%p\n' 2>> "$E" || m=$?
  done < "$T/moved.$N.txt"
fi
echo "$d $s $m" > "$T/deny_rc.$N"
EOF
rm -f "$T/deny_rc.$N"
setsid bash "$T/deny_scan.sh" "$T" "$N" "$nr" "${roots[@]}" "$nn" "${names[@]}" "$np" "${skips[@]}" "$@" \
  > "$T/deny.$N.txt" 2>/dev/null < /dev/null &
pid=$!; i=0; ticks=$((LIMIT * 10))
while kill -0 "$pid" 2>/dev/null && [ "$i" -lt "$ticks" ]; do sleep 0.1; i=$((i + 1)); done
if kill -0 "$pid" 2>/dev/null; then
  kill -- "-$pid" 2>/dev/null; kill "$pid" 2>/dev/null; echo complete=0; echo "why=not finished in ${LIMIT}s"
elif ! read -r d s m < "$T/deny_rc.$N" 2>/dev/null; then
  echo complete=0; echo 'why=the look left no exit code'
elif [ "$d$s$m" = 000 ]; then
  echo complete=1
else
  writable=0; other=0; : > "$T/deny_unreadable.$N"
  while IFS= read -r line; do
    case "$line" in
      *": Permission denied")
        p=${line#*: }; p=${p%: Permission denied}; p=${p#?}; p=${p%?}  # "<find>: '<path>': Permission denied"
        echo "$p" >> "$T/deny_unreadable.$N"; [ -w "$p" ] && writable=$((writable + 1)) ;;
      *) other=$((other + 1)) ;;
    esac
  done < "$T/deny_err.$N"
  denied=$(sort -u "$T/deny_unreadable.$N" | wc -l)
  code=$(for c in $d $s $m; do [ "$c" != 0 ] && { echo "$c"; break; }; done)
  if [ "$denied" -gt 0 ] && [ "$writable" = 0 ] && [ "$other" = 0 ]; then
    echo complete=1; echo "unreadable=$denied"
  else
    echo complete=0; echo "why=find exited $code (unreadable but writable: $writable, other errors: $other)"
  fi
fi
printf 'uid=%s\n' "$EUID"
printf 'count=%s\n' "$(wc -l < "$T/deny.$N.txt")"
echo files:
head -n 50000 "$T/deny.$N.txt"
"""

# mounts <file>: the node's mount table (``/proc/mounts``), for the aliases the denied-folder look skips.
MOUNTS = r"""cat "$1" 2>/dev/null
"""

