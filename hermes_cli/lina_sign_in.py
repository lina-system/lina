"""Signing a runner in on a node from the hub's signed-in browser (lina#230).

``start(node, runner)`` runs that runner's own sign-in on the node and finishes it in the hub's browser:

1. ssh runs ``NODE_SCRIPT`` on the node. It starts the runner's CLI through LINA's shim with ``BROWSER`` set to a
   helper that writes the sign-in address into a 0600 file in a 0700 temporary folder; the script hands the address
   back over the same ssh channel, so it lives in the hub's memory only. The CLI's own output goes to /dev/null.
2. When the address returns to ``http://127.0.0.1:<port>/``, the hub opens ``ssh -L <port>:127.0.0.1:<port>`` so its
   browser's return lands on the CLI's own listener on the node; the code and token never pass through LINA. A
   runner whose return is a page with a code to paste (agy) gets that code from the browser command's stdout, kept in
   memory and typed into the CLI over the ssh channel.
3. ``worker.sign_in.browser.command`` gets the address on stdin (never in argv) and the account from
   ``worker.sign_in.accounts.<runner>`` in ``LINA_SIGN_IN_ACCOUNT``. It exits 0 when consent is given, or
   ``NEEDS_PERSON_EXIT`` with a ``needs_person: two_step|new_device|account_choice`` line on stderr; then the flow
   stops and one inbox question names the node, the runner and what is needed.
4. The node script stops the CLI, removes its folder and the node's runner report cache; the hub reads hello again
   and the result says whether the node now reports the runner signed in.

Runners that sign in through OCX or a key are only reported here. No address, code or token is printed, logged or
written into a result, the inbox or a card.
"""

from __future__ import annotations

import fcntl  # windows-footgun: ok — the hub that signs nodes in runs on POSIX only
import logging
import queue
import shlex
import subprocess
import threading
import urllib.parse
from dataclasses import dataclass
from pathlib import Path, PurePosixPath

NEEDS_PERSON_EXIT = 3
NEEDS = {"two_step": "the sign-in asks for a two-step check",
         "new_device": "the sign-in asks to confirm a new device",
         "account_choice": "the sign-in asks which account to use"}
URL_WAIT_S = 30.0
BROWSER_TIMEOUT_S = 600.0
FINISH_TIMEOUT_S = 120.0
TUNNEL_TIMEOUT_S = 30.0
CLOSE_TIMEOUT_S = 30.0
STOP_GRACE_S = 10.0
MENU_DELAY_S = 3.0
LOOPBACK = ("127.0.0.1", "localhost")
logger = logging.getLogger(__name__)


@dataclass(frozen=True)
class Method:
    """How one runner's CLI starts its own sign-in: arguments after its command, the credential file (under the
    node's HOME) that exists once it is signed in, whether it needs a terminal, and whether its first screen is a
    menu whose first entry (Enter) starts the browser sign-in."""
    argv: tuple[str, ...]
    credential: str
    pty: bool = False
    menu: bool = False


METHODS = {
    "grok": Method(("login", "--oauth"), ".grok/auth.json"),
    "agy": Method((), ".gemini/antigravity-cli/antigravity-oauth-token", pty=True, menu=True),
}
REPORTED_ONLY = {"ocx": "reported only: signs in through OCX", "cursor": "reported only: signs in with a key"}

# $1 the sign-in folder, $2 report cache to remove, $3 a pid to spare (the caller). Stops every process recorded in
# the folder's `pids` and all their descendants (found before any is signalled, so a CLI that a terminal wrapper put
# in its own session is found too), removes the folder and the cache, and says `cleaned` once the folder is gone.
# The hub runs it as its own short call after the session, so the confirmation never depends on the session's shell.
STOP_SCRIPT = r"""
set -u
d=$1; cache=$2; spare=${3:-}
case ${d##*/} in lina-sign-in.*) ;; *) exit 64 ;; esac
tree() {
  [ "$1" = "$$" ] && return 0
  for c in $(pgrep -P "$1" 2>/dev/null); do tree "$c"; done
  [ "$1" = "$spare" ] || echo "$1"
}
all=''
for p in $(cat "$d/pids" 2>/dev/null); do all="$all $(tree "$p" | tr '\n' ' ')"; done
for p in $all; do kill "$p" 2>/dev/null; done
n=0
for p in $all; do
  while kill -0 "$p" 2>/dev/null && [ "$n" -lt 50 ]; do sleep 0.1; n=$((n + 1)); done
done
for p in $all; do kill -0 "$p" 2>/dev/null && kill -9 "$p" 2>/dev/null; done
rm -rf "$d"
[ -n "$cache" ] && rm -f "$cache"
[ -e "$d" ] || echo cleaned
"""

# $1 command (shell words), $2 credential under $HOME, $3 1 = run under a terminal, $4 report cache to remove,
# $5 seconds before Enter on the first menu ("" = none), $6 STOP_SCRIPT. Lines out: `dir <folder>`, `url <address>`,
# `signed`, `exit <n>`, `no-url`, `cleaned`. Lines in: `code <code>`, `stop`.
NODE_SCRIPT = r"""
set -u
cmd=$1; cred=$2; pty=$3; cache=$4; menu=$5; stop=$6
umask 077
trap '' PIPE
d=$(mktemp -d "${TMPDIR:-/tmp}/lina-sign-in.XXXXXX") || exit 70
echo $$ > "$d/pids"
echo "dir $d"
trap 'sh -c "$stop" lina-sign-in-stop "$d" "$cache" $$' EXIT
trap 'exit 1' INT TERM HUP
cat > "$d/open" <<'EOF'
#!/bin/sh
umask 077
printf '%s\n' "$1" > "${0%/*}/url.part" && mv "${0%/*}/url.part" "${0%/*}/url"
EOF
chmod 700 "$d/open"
mkfifo "$d/in"
if [ "$pty" = 1 ]; then
  enter=$(printf '\r')
  case $(uname) in Darwin) set -- script -q /dev/null sh -c "$cmd" ;; *) set -- script -qfec "$cmd" /dev/null ;; esac
else
  enter=''
  set -- sh -c "$cmd"
fi
( BROWSER="$d/open" "$@" < "$d/in" > /dev/null 2>&1 & echo $! >> "$d/pids"; wait $!; echo "exit $?" ) &
echo $! >> "$d/pids"
exec 3> "$d/in"
( while [ ! -e "$HOME/$cred" ]; do sleep 1; done; echo signed ) &
echo $! >> "$d/pids"
waited=0; sent=0
while [ ! -f "$d/url" ]; do
  if [ -n "$menu" ] && [ "$sent" = 0 ] && [ "$waited" -ge "$menu" ]; then printf '\r' >&3; sent=1; fi
  [ "$waited" -ge 30 ] && { echo no-url; exit 1; }
  sleep 1; waited=$((waited + 1))
done
for p in $(cat "$d/pids"); do [ "$p" = $$ ] || pgrep -P "$p" >> "$d/pids" 2>/dev/null; done
printf 'url %s\n' "$(cat "$d/url")"
rm -f "$d/url"
while IFS= read -r line; do
  case $line in
    "code "*) printf '%s%s\n' "${line#code }" "$enter" >&3 ;;
    stop) break ;;
  esac
done
"""


class SignInError(RuntimeError):
    pass


def _result(node: str, runner: str, state: str, reason: str, **extra) -> dict:
    return {"node": node, "runner": runner, "state": state, "reason": reason, **extra}


class _Lines:
    """A child's stdout as lines on a queue, read on a thread so each wait has its own deadline."""

    def __init__(self, proc: subprocess.Popen):
        self.proc, self.lines = proc, queue.Queue()
        threading.Thread(target=self._pump, daemon=True).start()  # health: allow HX012 -- reads a pipe only, no profile state

    def _pump(self) -> None:
        for raw in self.proc.stdout:
            self.lines.put(raw.rstrip("\r\n"))
        self.lines.put(None)

    def expect(self, words: tuple[str, ...], timeout: float) -> tuple[str | None, str]:
        """The first line whose first word is one of *words*: (word, rest); (None, "") on timeout or end."""
        import time
        deadline = time.monotonic() + timeout
        while (left := deadline - time.monotonic()) > 0:
            try:
                line = self.lines.get(timeout=left)
            except queue.Empty:
                break
            if line is None:
                self.lines.put(None)
                break
            word, _, rest = line.partition(" ")
            if word in words:
                return word, rest
        return None, ""


def _ssh(host: str, *options: str) -> list[str]:
    return ["ssh", "-o", "BatchMode=yes", "-o", "ConnectTimeout=10", *options, host]


class NodeSession:
    """``NODE_SCRIPT`` on one node (``sh`` here for a ``host: local`` device), steered over its stdin."""

    def __init__(self, host: str | None, args: list[str], cache: str):
        self.host, self.cache, self.folder = host, cache, ""
        self.proc = subprocess.Popen(self._argv(NODE_SCRIPT, "lina-sign-in", *args, STOP_SCRIPT),
                                     stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.DEVNULL,
                                     text=True, encoding="utf-8", errors="replace")
        self.out = _Lines(self.proc)

    def _argv(self, script: str, *args: str) -> list[str]:
        words = ["sh", "-c", script, *args]
        return words if self.host is None else [*_ssh(self.host), shlex.join(words)]

    def opened(self) -> bool:
        """Wait for the node's sign-in folder; False when the node did not answer."""
        word, self.folder = self.out.expect(("dir",), URL_WAIT_S)
        return word == "dir"

    def send(self, line: str) -> None:
        try:
            self.proc.stdin.write(line + "\n")
            self.proc.stdin.flush()
        except (BrokenPipeError, OSError, ValueError):
            pass  # the node side has ended; close() reports whether it cleaned up

    def close(self) -> bool:
        """Ask the session to stop, end it if it does not within ``STOP_GRACE_S``, then stop what is left on the node
        with ``STOP_SCRIPT`` as its own call; True once that call says the folder is gone."""
        self.send("stop")
        try:
            self.proc.stdin.close()
        except OSError:
            pass
        try:
            self.proc.wait(timeout=STOP_GRACE_S)
        except subprocess.TimeoutExpired:
            self.proc.kill()
            self.proc.wait()
        if not self.folder:
            return False
        try:
            done = subprocess.run(self._argv(STOP_SCRIPT, "lina-sign-in-stop", self.folder, self.cache, ""),
                                  stdin=subprocess.DEVNULL, capture_output=True, text=True, encoding="utf-8",
                                  errors="replace", timeout=CLOSE_TIMEOUT_S, check=False)
        except (OSError, subprocess.TimeoutExpired):
            return False
        return "cleaned" in done.stdout.split()


class Tunnel:
    """``ssh -L <port>:127.0.0.1:<port> <node>``: the browser's return to this port reaches the node's listener."""

    def __init__(self, host: str, port: int):
        argv = [*_ssh(host, "-o", "ExitOnForwardFailure=yes", "-L", f"{port}:127.0.0.1:{port}"),
                "echo ready; exec cat > /dev/null"]
        self.proc = subprocess.Popen(argv, stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.DEVNULL,
                                     text=True, encoding="utf-8", errors="replace")
        self.ready = _Lines(self.proc).expect(("ready",), TUNNEL_TIMEOUT_S)[0] == "ready"

    def close(self) -> None:
        try:
            self.proc.stdin.close()
        except OSError:
            pass
        self.proc.terminate()
        try:
            self.proc.wait(timeout=CLOSE_TIMEOUT_S)
        except subprocess.TimeoutExpired:
            self.proc.kill()
            self.proc.wait()


def loopback_port(address: str) -> int | None:
    """The port of an ``http://127.0.0.1:<port>/`` return in the sign-in address; None for any other return."""
    query = urllib.parse.parse_qs(urllib.parse.urlsplit(address).query)
    back = urllib.parse.urlsplit((query.get("redirect_uri") or [""])[0])
    try:
        port = back.port
    except ValueError:
        return None
    return port if back.scheme == "http" and back.hostname in LOOPBACK and port else None


def _browser(conf: dict, runner: str, address: str, mode: str) -> tuple[str, str]:
    """The hub's browser command on *address*: ("ok", code-or-""), ("needs_person", need) or ("failed", why). Its
    output is read, never shown."""
    command = conf["command"]
    account = (conf.get("accounts") or {}).get(runner) or ""
    from tools.environments.local import build_subprocess_env
    env = build_subprocess_env(scrub_secrets=True, extra={
        "LINA_SIGN_IN_RUNNER": runner, "LINA_SIGN_IN_ACCOUNT": str(account), "LINA_SIGN_IN_RETURN": mode})
    try:
        done = subprocess.run(command, input=address + "\n", capture_output=True, text=True, encoding="utf-8",
                              errors="replace", env=env, timeout=BROWSER_TIMEOUT_S, check=False)
    except subprocess.TimeoutExpired:
        return "failed", f"the browser command did not finish in {BROWSER_TIMEOUT_S:g}s"
    except OSError as exc:
        return "failed", f"the browser command could not start ({type(exc).__name__})"
    if done.returncode == NEEDS_PERSON_EXIT:
        needs = [line.split(":", 1)[1].strip() for line in done.stderr.splitlines()
                 if line.startswith("needs_person:")]
        if needs and needs[-1] in NEEDS:
            return "needs_person", needs[-1]
        return "failed", "the browser command stopped for a person without naming two_step|new_device|account_choice"
    if done.returncode != 0:
        return "failed", f"the browser command ended with exit {done.returncode}"
    code = next((line.strip() for line in done.stdout.splitlines() if line.strip()), "")
    if mode == "code" and not code:
        return "failed", "the browser command gave no code for a runner that returns to a page"
    return "ok", code if mode == "code" else ""


def _finish(session: NodeSession) -> tuple[str, str]:
    """After consent: wait until the node's CLI holds its credential or ends."""
    word, rest = session.out.expect(("signed", "exit"), FINISH_TIMEOUT_S)
    if word == "signed" or (word == "exit" and rest.strip() == "0"):
        return "ok", ""
    if word == "exit":
        return "failed", f"the runner's sign-in ended with exit {rest.strip()}"
    return "failed", f"the runner did not finish its sign-in in {FINISH_TIMEOUT_S:g}s"


def _steps(session: NodeSession, host: str | None, conf: dict, runner: str) -> tuple[str, str]:
    tunnel = None
    try:
        if not session.opened():
            return "failed", "the node did not answer"
        word, address = session.out.expect(("url", "no-url", "exit"), URL_WAIT_S + MENU_DELAY_S + 30)
        if word != "url":
            return "failed", "the runner gave no sign-in address" if word else "the node did not answer"
        port = loopback_port(address)
        if port is not None and host is not None:
            tunnel = Tunnel(host, port)
            if not tunnel.ready:
                return "failed", "the return tunnel to the node could not be set up (is that port busy on the hub?)"
        outcome, detail = _browser(conf, runner, address, "loopback" if port is not None else "code")
        if outcome != "ok":
            return outcome, detail
        if detail:
            session.send(f"code {detail}")
        return _finish(session)
    finally:
        if tunnel is not None:
            tunnel.close()


def _flow(host: str | None, args: list[str], conf: dict, runner: str) -> tuple[str, str]:
    """One sign-in on the node: ("ok", ""), ("needs_person", need) or ("failed", why). The tunnel and the node
    session are always closed; a node that does not confirm its cleanup turns the outcome into a failure."""
    session = NodeSession(host, args, cache=args[3])
    try:
        outcome, detail = _steps(session, host, conf, runner)
    finally:
        cleaned = session.close()
    if not cleaned:
        return "failed", "the node did not confirm that it stopped the runner's sign-in and removed its files"
    return outcome, detail


def _lock(worker_root: Path, node: str, runner: str):
    from hermes_cli.worker import settings
    path = settings.hub_dir(worker_root) / "sign-in" / f"{node}.{runner}.lock"
    path.parent.mkdir(parents=True, exist_ok=True)
    handle = open(path, "w", encoding="utf-8")
    try:
        fcntl.flock(handle, fcntl.LOCK_EX | fcntl.LOCK_NB)
    except BlockingIOError:
        handle.close()
        return None
    return handle


def ask_person(base: Path, node: str, runner: str, need: str) -> str:
    """One inbox question for the owner (an open owner's to-do whose decision names the node, the runner and what is
    needed), written into the work record the board's inbox reads (``work.board.inbox``). An open question for the
    same node and runner is kept and its id returned. The text is written by ``ledger.append`` under the work lock,
    not through ``work add``: the writing rules refuse device names, and this question must name the node."""
    from hermes_cli.lina_board import _WORK_LOCK
    from hermes_cli.work import env, ledger
    from hermes_cli.work.common import Lock, now
    from hermes_cli.work.items import next_id
    with _WORK_LOCK:
        bound = env.bind(env.load(base))
        try:
            with Lock():
                recs, order = ledger.read_all()
                for i in order:
                    mark = recs[i].get("sign_in") or {}
                    if recs[i].get("state") == "open" and (mark.get("node"), mark.get("runner")) == (node, runner):
                        return i
                rec = {"id": next_id(recs, "할 일"), "kind": "할 일", "by": bound.owner,
                       "title": f"Sign in to {runner} on {node}", "basis": [], "promise": False,
                       "done": f"`lina devices sign-in {node} {runner}` ends signed_in", "check": "",
                       "decide": f"Sign in to {runner} on {node}: {NEEDS[need]}. Finish it in the hub's browser?",
                       "parent": "", "projects": [], "session": "", "links": [], "every": "", "stall": None,
                       "notes": [], "evidence": [], "wait": {}, "state": "open", "created": now(),
                       "progress_at": now(), "why": "a node's runner waits for its sign-in",
                       "sign_in": {"node": node, "runner": runner, "need": need}}
                if bound.agents:
                    rec["from"] = next(iter(bound.agents))
                ledger.append(rec, "add", bound.owner)
                return rec["id"]
        finally:
            ledger.close()
            env.reset()


def _row(dev, worker_root: Path, runner: str) -> dict | str:
    from hermes_cli.lina_devices import runner_status
    rows = runner_status(dev, worker_root)
    if isinstance(rows, dict):
        return f"the node's hello could not be read: {rows.get('error') or 'no answer'}"
    return next((item for item in rows if item.get("name") == runner), None) or \
        f"{runner} is not a runner {dev.name} offers"


def _refusal(row: dict, conf: dict) -> tuple[str, str] | None:
    """Why this sign-in is not started, as (state, reason); None when it may start."""
    from hermes_cli.lina_runners import SIGN_IN_OK
    kind = row.get("sign_in_by") or ""
    if kind not in METHODS:
        state = "signed_in" if row.get("sign_in") in SIGN_IN_OK else "failed"
        return state, REPORTED_ONLY.get(kind, "reported only: LINA cannot start this runner's sign-in")
    if row.get("sign_in") == "signed_in":
        return "signed_in", "already signed in"
    if not row.get("shim"):
        return "failed", f"not installed by LINA on this node: `lina runners install {row.get('pin') or ''}` there"
    command = (conf.get("browser") or {}).get("command") if isinstance(conf.get("browser"), dict) else None
    if not isinstance(command, list) or not command or not all(isinstance(part, str) and part for part in command):
        return "failed", "worker.sign_in.browser.command is not set on the hub (see docs/lina-sign-in.md)"
    return None


def _node_args(dev, worker_root: Path, row: dict, method: Method) -> list[str]:
    from hermes_cli.lina_runners import REPORT_CACHE
    shim = PurePosixPath(row["shim"])
    cache = "" if dev.local else str(shim.parents[2] / "worker" / REPORT_CACHE)
    return [f"exec {shlex.join([str(shim), *method.argv])}", method.credential, "1" if method.pty else "0", cache,
            str(int(MENU_DELAY_S)) if method.menu else ""]


def start(node: str, runner: str, *, base: Path | None = None) -> dict:
    """``lina devices sign-in <node> <runner>`` (and the device screen's sign-in button, lina#234): the result is
    ``{node, runner, state: signed_in|needs_person|failed, reason}``, plus ``question`` (the inbox item) when a
    person is needed and ``sign_in`` (what the node reports) once it has been asked again."""
    from hermes_cli import lina_runners
    from hermes_cli.lina_devices import DevicesError, _base, device
    from hermes_cli.worker import settings
    base = _base(base)
    worker_root = settings.root(base)
    try:
        dev = device(node, base)
    except DevicesError as exc:
        return _result(node, runner, "failed", str(exc))
    row = _row(dev, worker_root, runner)
    if isinstance(row, str):
        return _result(node, runner, "failed", row)
    conf = settings.section(base).get("sign_in")
    conf = conf if isinstance(conf, dict) else {}
    refused = _refusal(row, conf)
    if refused is not None:
        return _result(node, runner, refused[0], refused[1], sign_in=row.get("sign_in"))
    held = _lock(worker_root, node, runner)
    if held is None:
        return _result(node, runner, "failed", f"a sign-in of {runner} on {node} is already running")
    try:
        method = METHODS[row["sign_in_by"]]
        browser = {"command": conf["browser"]["command"], "accounts": conf.get("accounts") or {}}
        outcome, detail = _flow(None if dev.local else dev.host, _node_args(dev, worker_root, row, method), browser,
                                runner)
    finally:
        held.close()
    if outcome == "needs_person":
        return _needs_person(base, node, runner, detail)
    if outcome == "failed":
        return _result(node, runner, "failed", detail)
    if dev.local:
        lina_runners.clear_report_cache(worker_root)
    return _verify(dev, worker_root, node, runner)


def _needs_person(base: Path, node: str, runner: str, need: str) -> dict:
    try:
        question = ask_person(base, node, runner, need)
    except (Exception, SystemExit) as exc:  # health: allow BLE001 -- the work store is psql or a file; logged, reported
        logger.warning("inbox question for %s on %s not written", runner, node, exc_info=True)
        return _result(node, runner, "needs_person", f"{NEEDS[need]}; the inbox question was not written "
                                                     f"({type(exc).__name__})", need=need)
    return _result(node, runner, "needs_person", f"{NEEDS[need]}: asked in the inbox", need=need, question=question)


def _verify(dev, worker_root: Path, node: str, runner: str) -> dict:
    row = _row(dev, worker_root, runner)
    if isinstance(row, str):
        return _result(node, runner, "failed", f"signed in on the node, but {row}")
    if row.get("sign_in") == "signed_in":
        return _result(node, runner, "signed_in", "the node reports it signed in", sign_in="signed_in")
    return _result(node, runner, "failed", f"the node still reports {row.get('sign_in') or 'nothing'}",
                   sign_in=row.get("sign_in"))
