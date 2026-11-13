"""What the daily runner sync reads on a person's devices (lina#490): the person-device lines of the old daily drift
check that still mean something after the old proxy left (lina#590), each read-only and each ok, an issue, or
unknown/none -- an unknown is a note, never a difference.

* ``proxy`` / ``magpie`` (local model state): the device's magpie model list (``GET`` of its ``model_check``, default
  ``settings.MODEL_CHECK``, with no key), judged as a node's (``worker.device.proxy_up``). HTTP 200 with a model is
  ok; any other answer is a ``proxy:`` issue; no answer, 401 or 403 is a ``magpie: ... unknown`` note. A list proves
  the device's magpie is up, not that its link to the hub carries a call: magpie lists a linked hub's models without
  asking the hub.
* ``tool address``: where the person's tools send model calls -- ``env.ANTHROPIC_BASE_URL`` of Claude's
  ``~/.claude/settings.json`` and ``providers.magpie.baseUrl`` of Aside's ``models.json`` (omo's agent dir). Only that
  one field is taken, by parsing the JSON on the device; user, password, query and fragment are cut there, and a
  value that is not an http(s) address leaves as ``other``. An address that is not the device's local magpie (or no
  address at all) is an issue; no file is a ``none`` note; an unreadable file is an ``unknown`` note.
* ``auto-update``: Claude's ``env.DISABLE_AUTOUPDATER`` in the same file. A Claude that updates itself leaves the
  hub's version; ``1`` is ok, anything else an issue, no file ``none``, unreadable ``unknown``.
* ``manual``: a runner copy an app manages (its path, or where its link resolves, is inside a ``*.app/Contents/``
  bundle) whose version is not the hub's pin. Only the app updates it, so the line names the app.

One ssh call per device in the person's login shell, as the devices view reads it (``lina_board_devices.person_read``):
``command -v``, ``realpath``/``readlink``, each runner's ``--version``, one ``curl`` GET and the JSON reads above (a
real ``python3``, else macOS's ``plutil``). Nothing on the device is written, installed, started or updated, and no
key is in the command or the output. The result goes to ``runner-sync/person.json``; ``sync status --issues`` lists
its issues, manual lines and notes (``lines``), and a run with an issue or a manual line sends one notice through
``worker.notice.command``. ``lina_runner_sync_daily`` starts this with the day's first sync when the hub has a person
device; ``python -m hermes_cli.lina_runner_sync_person`` runs it by hand.
"""

from __future__ import annotations

import json
import re
import shlex
import subprocess
import time
from pathlib import Path
from typing import Callable
from urllib.parse import urlsplit

from hermes_cli import lina_runner_sync_state as state

PERSON_FILE = "person.json"
TIMEOUT_S = 30
LINK = "@@magpie"
CODE = "@@code "
CONFIG = "@@config "
DEFAULT_PORT = 3425
LOOPBACK = frozenset({"127.0.0.1", "localhost", "::1"})
UNKNOWN_CODES = frozenset({401, 403})  # the device's magpie wants a key: what it lists is unknown, not down
CLAUDE_SETTINGS = "$HOME/.claude/settings.json"
# (tool, file as the login shell spells it, JSON key path, kind): the only fields read from a person's tool settings.
CONFIGS = (("claude", CLAUDE_SETTINGS, "env.ANTHROPIC_BASE_URL", "address"),
           ("aside", "${OMO_CODING_AGENT_DIR:-$HOME/.omo/agent}/models.json", "providers.magpie.baseUrl", "address"),
           ("claude", CLAUDE_SETTINGS, "env.DISABLE_AUTOUPDATER", "flag"))
_SHOWN_PATH = {CLAUDE_SETTINGS: "~/.claude/settings.json", CONFIGS[1][1]: "~/.omo/agent/models.json"}
_STATES = ("none", "unknown", "absent", "other")
_VERSION = re.compile(r"\d+\.\d+\.\d+(?:-[0-9A-Za-z.\-]+)?")
_APP = re.compile(r"([^/]+\.app)/Contents/")
# One JSON field by its dotted key path; prints the string, or absent / other / unknown.
_PY_READ = ('import json,sys\n'
            'try:\n d=json.load(open(sys.argv[1],encoding="utf-8-sig"))\n'
            'except Exception:\n print("unknown");sys.exit()\n'
            'for k in sys.argv[2].split("."):\n'
            ' if not isinstance(d,dict) or k not in d:\n  print("absent");sys.exit()\n'
            ' d=d[k]\n'
            'print(d if isinstance(d,str) else "other")\n')
# A real python3, never macOS's stand-in that offers to install the developer tools; else plutil, whose failure cannot
# tell a missing key from a bad file, so it is unknown.
_READ_FN = ("_v() { if [ ! -e \"$1\" ]; then echo none; elif [ ! -r \"$1\" ]; then echo unknown; "
            "elif command -v python3 >/dev/null 2>&1 && { [ \"$(uname -s)\" != Darwin ] || "
            "xcode-select -p >/dev/null 2>&1; }; then python3 -I -c \"$PYX\" \"$1\" \"$2\" 2>/dev/null || echo unknown; "
            "elif command -v plutil >/dev/null 2>&1; then plutil -extract \"$2\" raw -o - \"$1\" 2>/dev/null "
            "|| echo unknown; else echo unknown; fi; }")
_ADDRESS_FN = ("_u() { v=$(_v \"$1\" \"$2\" | head -n 1); case \"$v\" in none|unknown|absent) echo \"$v\";; "
               "http://*|https://*) printf '%s\\n' \"$v\" | "
               "sed -E 's|^([A-Za-z][A-Za-z0-9+.-]*://)[^/?#]*@|\\1|; s|[?#].*$||' | cut -c1-200;; "
               "*) echo other;; esac; }")
_FLAG_FN = ("_f() { v=$(_v \"$1\" \"$2\" | head -n 1); case \"$v\" in none|unknown|absent|0|1|true|false) "
            "echo \"$v\";; *) echo other;; esac; }")


def tools(root: Path) -> list[tuple[str, str, str]]:
    """``(name, bin, pinned version)`` of each runner the hub pins, left out as the devices view leaves it out: an
    adapter checked by its package has no ``--version`` worth starting."""
    from hermes_cli.lina_runners import pins
    return [(pin.name, pin.bin, pin.version) for pin in pins(root=root).values() if pin.check != "package"]


def script(bins: list[str], url: str) -> str:
    lines = [f"PYX={shlex.quote(_PY_READ)}", _READ_FN, _ADDRESS_FN, _FLAG_FN]
    for name in bins:
        quoted = shlex.quote(name)
        lines.append(f"printf '@@%s\\n' {quoted}; p=$(command -v {quoted} 2>/dev/null) && {{ printf 'path %s\\n' \"$p\"; "
                     "printf 'real %s\\n' \"$(realpath \"$p\" 2>/dev/null || readlink \"$p\" 2>/dev/null)\"; "
                     "printf 'version %s\\n' \"$(\"$p\" --version </dev/null 2>/dev/null | head -n 1)\"; }")
    for tool, path, key, kind in CONFIGS:
        reader = "_u" if kind == "address" else "_f"
        lines.append(f"printf '{CONFIG}%s %s\\n' {tool} {key}; {reader} \"{path}\" {key}")
    lines.append(f"printf '{LINK}\\n'; curl -s -m 5 -w '\\n{CODE}%{{http_code}}\\n' {shlex.quote(url)} 2>/dev/null")
    return "; ".join(lines) + "; true"


def _models(body: list[str]) -> int:
    try:
        data = json.loads("\n".join(body)).get("data")
    except (ValueError, AttributeError):
        return 0
    return len(data) if isinstance(data, list) else 0


def parse(text: str) -> dict:
    """``{"tools": {bin: {path, real, version}}, "configs": {"<tool> <key>": value}, "link": {health, models}}`` from
    :func:`script`'s output; ``health`` is None when nothing answered."""
    found: dict[str, dict] = {}
    configs: dict[str, str] = {}
    link: dict = {"health": None, "models": 0}
    body: list[str] = []
    current = None
    for line in text.splitlines():
        if line == LINK:
            current = LINK
        elif current == LINK:
            if line.startswith(CODE):
                code = line[len(CODE):].strip()
                link["health"] = int(code) if code.isdigit() and int(code) else None
                link["models"] = _models(body)
            else:
                body.append(line)
        elif line.startswith(CONFIG):
            current = ("config", line[len(CONFIG):].strip())
        elif isinstance(current, tuple):
            configs.setdefault(current[1], line.strip())
        elif line.startswith("@@"):
            current = line[2:].strip()
            found[current] = {}
        elif current is not None:
            key, _, value = line.partition(" ")
            if key in ("path", "real", "version") and value.strip():
                found[current][key] = value.strip()
    for copy in found.values():
        if "version" in copy:
            hit = _VERSION.search(copy["version"])
            copy["version"] = hit.group(0) if hit else copy["version"][:40]
    return {"tools": found, "configs": configs, "link": link}


def read(alias: str, text: str) -> dict:
    """One read-only ssh call running *text* in the person's login shell; ``reachable`` False when ssh fails."""
    remote = 'exec "${SHELL:-/bin/sh}" -lc ' + shlex.quote(text)
    argv = ["ssh", "-o", "BatchMode=yes", "-o", "ConnectTimeout=5", "--", alias, remote]
    try:
        done = subprocess.run(argv, capture_output=True, text=True, encoding="utf-8", errors="replace",
                              stdin=subprocess.DEVNULL, timeout=TIMEOUT_S, check=False)
    except (OSError, subprocess.TimeoutExpired):
        return {"reachable": False}
    if done.returncode != 0:
        return {"reachable": False}
    return {"reachable": True, **parse(done.stdout)}


def safe_endpoint(address: str) -> str | None:
    """``scheme://host[:port]/path`` of an http(s) address, an IPv6 host kept in brackets; user, password, query and
    fragment are dropped. None when it is not such an address."""
    parts = urlsplit(address.strip())
    try:
        port = parts.port
    except ValueError:
        return None
    if parts.scheme not in ("http", "https") or not parts.hostname:
        return None
    host = f"[{parts.hostname}]" if ":" in parts.hostname else parts.hostname
    return f"{parts.scheme}://{host}{f':{port}' if port is not None else ''}{parts.path}"


def _port(address: str) -> int | None:
    parts = urlsplit(address)
    try:
        return parts.port or {"http": 80, "https": 443}.get(parts.scheme)
    except ValueError:
        return None


def is_local_magpie(address: str, magpie_url: str) -> bool:
    """The address reaches the device's own magpie: a loopback host on the port its model check asks."""
    host = urlsplit(address).hostname
    return host in LOOPBACK and _port(address) == (urlsplit(magpie_url).port or DEFAULT_PORT)


def managing_app(copy: dict) -> str | None:
    """The ``*.app`` bundle a copy lives in (by its path or where its link resolves), None for any other copy."""
    for where in (copy.get("real"), copy.get("path")):
        hit = _APP.search(where or "")
        if hit:
            return hit.group(1)
    return None


def _link_lines(device: str, link: dict, url: str, out: dict) -> None:
    from hermes_cli.worker.device import proxy_down_why, proxy_up
    check = {"url": url, "health": link.get("health"), "models": link.get("models") or 0}
    if proxy_up(check):
        return
    if check["health"] is None:
        out["notes"].append(f"- magpie: {device} unknown (no answer at {url})")
    elif check["health"] in UNKNOWN_CODES:
        out["notes"].append(f"- magpie: {device} unknown (HTTP {check['health']} at {url}: it wants a key)")
    else:
        out["issues"].append(f"- proxy: {device} {proxy_down_why(check)}")


def _config_lines(device: str, configs: dict, url: str, out: dict) -> None:
    for tool, path, key, kind in CONFIGS:
        value = configs.get(f"{tool} {key}", "unknown")
        where = f"{tool} on {device} ({_SHOWN_PATH[path]} {key})"
        label = "tool address" if kind == "address" else "auto-update"
        if value in ("none", "unknown"):
            out["notes"].append(f"- {label}: {where} {value}")
        elif kind == "flag":
            if value != "1":
                out["issues"].append(f"- {label}: {where} is {value}: it updates itself away from the hub's version")
        elif value == "absent":
            out["issues"].append(f"- {label}: {where} not set: it does not go through the local magpie")
        elif (shown := safe_endpoint(value)) is None:
            out["notes"].append(f"- {label}: {where} unknown (not an http address)")
        elif not is_local_magpie(shown, url):
            out["issues"].append(f"- {label}: {where} points at {shown}, not the local magpie")


def judge(device: str, seen: dict, pinned: list[tuple[str, str, str]], url: str) -> dict:
    """``{"issues", "manual", "notes"}`` lines for one person device as :func:`read` saw it."""
    out: dict[str, list[str]] = {"issues": [], "manual": [], "notes": []}
    if not seen.get("reachable"):
        out["notes"].append(f"- unreachable: {device}")
        return out
    _link_lines(device, seen.get("link") or {}, url, out)
    _config_lines(device, seen.get("configs") or {}, url, out)
    for name, tool_bin, pinned_version in pinned:
        copy = (seen.get("tools") or {}).get(tool_bin) or {}
        app = managing_app(copy)
        if app and copy.get("version") != pinned_version:
            out["manual"].append(f"- manual: {name} on {device} {copy.get('version') or 'unknown'}, hub "
                                 f"{pinned_version}: managed by {app}, update it in the app")
    return out


def check(root: Path, *, reader: Callable[[str, str], dict] = read, now: float | None = None) -> dict:
    from hermes_cli.worker import settings
    pinned = tools(root)
    result: dict = {"checked": time.time() if now is None else now, "devices": {}, "issues": [], "manual": [],
                    "notes": []}
    for dev in settings.devices(root):
        if dev.role != "person":
            continue
        url = dev.model_check or settings.MODEL_CHECK
        seen = reader(dev.host, script([tool_bin for _, tool_bin, _ in pinned], url))
        result["devices"][dev.name] = seen
        for key, lines_ in judge(dev.name, seen, pinned, url).items():
            result[key] += lines_
    return result


def lines(root: Path) -> tuple[list[str], list[str]]:
    """(issues, notes) of the last check for ``sync status --issues``: its issues and manual lines, and its notes."""
    saved = state.read_json(state.sync_dir(root) / PERSON_FILE, {})

    def listed(*keys: str) -> list[str]:
        return [line for key in keys for line in saved.get(key) or [] if isinstance(line, str)]
    return listed("issues", "manual"), listed("notes")


def run(root: Path | None = None, *, reader: Callable[[str, str], dict] = read,
        notify: Callable[[Path, str], str] | None = None, now: float | None = None) -> int:
    from hermes_cli import lina_deps, lina_runner_sync
    root = root or lina_deps.lina_root()
    result = check(root, reader=reader, now=now)
    state.write_json(state.sync_dir(root) / PERSON_FILE, result)
    lines_ = [lina_runner_sync.scrub(line) for line in result["issues"] + result["manual"]]
    if lines_:
        text = "\n".join([f"person devices {state.day(result['checked'])}:", *lines_]) + "\n"
        (notify or lina_runner_sync.notify)(root, text)
    return 0


if __name__ == "__main__":
    raise SystemExit(run())
