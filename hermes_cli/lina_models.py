"""``lina models``: OCX's own ``--json`` output, read from LINA's pinned OCX (lina#32) or, with ``ocx.external``, from
an OCX the operator runs outside LINA (lina#232).

Each view is one ``ocx … --json`` call of the pinned CLI (2.95.0): ``models live``, ``account list``,
``account list --quota`` (may probe upstream quotas), ``status`` and ``usage``. The CLI finds its proxy through the
data dir and falls back to port 10100, which may be another OCX on this account, so a view is read only while the
proxy answering on LINA's port is LINA's own.

With ``ocx.external: {cli, data_dir}`` the reads run that CLI (the caller's environment, plus ``OPENCODEX_HOME`` when
``data_dir`` is given) and nothing is ever written to that OCX: an argv outside ``read_only`` raises before anything
is spawned, the write verbs of ``lina models`` / ``lina services`` refuse (``lina_ocx.refuse_external``), and a read
fails while ``ocx status --json`` says the proxy is not running. Each argv's output is kept for 60 s, so the app's
polling and the per-machine usage fan-out do not multiply OCX calls (a call takes about 0.3 s, ``status`` 2.7 s).
"""

from __future__ import annotations

import json
import os
import re
import subprocess
import threading
import time
from pathlib import Path
from typing import Any

from hermes_cli import lina_ocx

VIEWS: dict[str, tuple[str, ...]] = {
    "list": ("models", "live", "--json"),
    "accounts": ("account", "list", "--json"),
    "quota": ("account", "list", "--quota", "--json"),
    "status": ("status", "--json"),
    "usage": ("usage", "--json"),
}
TIMEOUT_S = 60
RANGES = ("today", "7d", "30d")
STATUS = ("status", "--json")
# OCX 2.97's plain ``usage --json`` is not on the read list; the view's own range (the last 30 days) is named.
EXTERNAL_VIEWS = {**VIEWS, "usage": ("usage", "--range", "30d", "--json")}
READS = frozenset({("account", "list", "--json"), ("account", "list", "--quota", "--json"), ("models", "live", "--json"),
                   ("link", "status", "--json"), ("access", "key", "list", "--json"), STATUS})
CACHE_S = 60
LINK_FRESH_S = 600
_KEY_ID = re.compile(r"[A-Za-z0-9_]+")
_CACHE: dict[tuple, tuple[float, str]] = {}
_LOCKS: dict[tuple, threading.Lock] = {}
_GUARD = threading.Lock()
_clock = time.monotonic


def read_only(args: tuple[str, ...]) -> bool:
    """The argv an external OCX may be run with: the fixed reads, and ``usage --range <r> [--api-key-id <id>]``.
    Never ``--refresh`` (it probes upstream) and no other verb."""
    if args in READS:
        return True
    if len(args) not in (4, 6) or args[:2] != ("usage", "--range") or args[2] not in RANGES or args[-1] != "--json":
        return False
    return len(args) == 4 or args[3] == "--api-key-id" and bool(_KEY_ID.fullmatch(args[4]))


def _call(argv: list[str], env: dict[str, str] | None, args: tuple[str, ...]) -> str:
    try:
        result = subprocess.run(argv, env=env, capture_output=True, text=True, encoding="utf-8", errors="replace",
                                timeout=TIMEOUT_S, check=False)
    except subprocess.TimeoutExpired as exc:
        raise lina_ocx.OcxError(f"ocx {' '.join(args)} took over {TIMEOUT_S}s") from exc
    except OSError as exc:
        raise lina_ocx.OcxError(f"ocx {' '.join(args)}: {exc}") from exc
    if result.returncode != 0:
        tail = (result.stderr or result.stdout).strip().splitlines()[-1:] or [f"exit {result.returncode}"]
        raise lina_ocx.OcxError(f"ocx {' '.join(args)}: {tail[0]}")
    try:
        json.loads(result.stdout)
    except ValueError as exc:
        raise lina_ocx.OcxError(f"ocx {' '.join(args)} printed no JSON") from exc
    return result.stdout


def _external(ext: dict[str, str], args: tuple[str, ...]) -> Any:
    if not read_only(args):
        raise lina_ocx.OcxError(f"`ocx {' '.join(args)}` is not a read; ocx.external is read-only")
    key = (ext["cli"], ext.get("data_dir"), args)
    with _GUARD:
        lock = _LOCKS.setdefault(key, threading.Lock())
    with lock:  # one call per argv at a time: a second reader waits for the first one's output
        hit = _CACHE.get(key)
        if hit is None or _clock() - hit[0] >= CACHE_S:
            env = dict(os.environ)  # health: allow PS-P05 -- the operator's one OCX per host runs with the caller's env
            if ext.get("data_dir"):
                env["OPENCODEX_HOME"] = ext["data_dir"]
            hit = _CACHE[key] = (_clock(), _call([ext["cli"], *args], env, args))
    return json.loads(hit[1])


def _own(root: Path, args: tuple[str, ...]) -> Any:
    version = lina_ocx.current_version(root)
    if version is None:
        raise lina_ocx.OcxError("not installed. Run `lina models install`.")
    port = lina_ocx.configured_port(root)
    if not lina_ocx.own_proxy_running(root, port):
        raise lina_ocx.OcxError(f"LINA's OCX is not running on port {port}. Run `lina services start ocx`.")
    return json.loads(_call(lina_ocx.ocx_argv(root, version, *args), lina_ocx.ocx_env(root), args))


def _read(root: Path, ext: dict[str, str] | None, args: tuple[str, ...]) -> Any:
    if ext is None:
        return _own(root, args)
    if not read_only(args):
        raise lina_ocx.OcxError(f"`ocx {' '.join(args)}` is not a read; ocx.external is read-only")
    if args != STATUS:
        status = _external(ext, STATUS)
        if not (isinstance(status, dict) and (status.get("proxy") or {}).get("running") is True):
            raise lina_ocx.OcxError(f"the OCX of ocx.external ({ext['cli']}) is not running: "
                                    "`ocx status --json` says proxy.running is not true")
    return _external(ext, args)


def run(args: tuple[str, ...], root: Path | None = None) -> Any:
    """``ocx <args>``'s parsed JSON from LINA's own OCX, or from ``ocx.external`` when that is set."""
    root = root or lina_ocx.lina_root()
    return _read(root, lina_ocx.external(root), tuple(args))


def query(view: str, root: Path | None = None) -> Any:
    root = root or lina_ocx.lina_root()
    ext = lina_ocx.external(root)
    return _read(root, ext, (EXTERNAL_VIEWS if ext else VIEWS)[view])


def _number(value) -> float | int | None:
    return value if isinstance(value, (int, float)) and not isinstance(value, bool) else None


def _seconds(value) -> float | None:
    number = _number(value)
    return None if number is None or number <= 0 else number / 1000 if number > 1e11 else float(number)


def _totals(row: dict) -> dict:
    return {"requests": _number(row.get("requests")) or 0,
            "tokens": {"total": _number(row.get("totalTokens")) or 0, "input": _number(row.get("inputTokens")) or 0,
                       "output": _number(row.get("outputTokens")) or 0,
                       "cacheRead": _number(row.get("cacheReadInputTokens")) or 0},
            "costUsd": _number(row.get("estimatedCostUsd"))}


def _rows(data: dict, name: str) -> list[dict]:
    return [row for row in data.get(name) or [] if isinstance(row, dict)]


def usage(range_: str, root: Path | None = None) -> dict:
    """``GET /api/lina/usage``: one range's usage by model, provider, account (OCX's ``accountLogLabel``) and machine
    (one ``usage --api-key-id`` per access key named ``link:<machine>``). There is no split by agent: every agent on
    the hub sends with the same OCX key, so OCX records no agent (lina#232)."""
    if range_ not in RANGES:
        raise ValueError(f"range must be one of {', '.join(RANGES)}")
    root = root or lina_ocx.lina_root()
    data = run(("usage", "--range", range_, "--json"), root)
    keys = run(("access", "key", "list", "--json"), root)
    machines = []
    for key in _rows(keys if isinstance(keys, dict) else {}, "keys"):
        name = str(key.get("name") or "")
        if name.startswith("link:") and name[5:]:
            share = run(("usage", "--range", range_, "--api-key-id", str(key.get("id")), "--json"), root)
            machines.append({"machine": name[5:], **_totals(share.get("summary") or {})})
    return {"range": range_, "generatedAt": _seconds(data.get("generatedAt")), **_totals(data.get("summary") or {}),
            "byModel": [{"model": row.get("model"), "provider": row.get("provider"), **_totals(row)}
                        for row in _rows(data, "models")],
            "byProvider": [{"provider": row.get("provider"), **_totals(row)} for row in _rows(data, "providers")],
            "byAccount": [{"account": row.get("accountLogLabel"), **_totals(row)} for row in _rows(data, "accounts")],
            "byMachine": sorted(machines, key=lambda row: row["machine"])}


def models_state(root: Path | None = None, now: float | None = None) -> dict:
    """``GET /api/lina/models/state``: the models on and off (``models live``), and per machine link its OCX state,
    last report and ``ok`` (connected and reported within the last ten minutes)."""
    root = root or lina_ocx.lina_root()
    now = time.time() if now is None else now
    models = run(("models", "live", "--json"), root)
    links = run(("link", "status", "--json"), root)
    rows = [row for row in models if isinstance(row, dict)] if isinstance(models, list) else []
    machines = []
    for link in _rows(links if isinstance(links, dict) else {}, "links"):
        at = _seconds(link.get("reportedAt"))
        machines.append({"machine": link.get("alias"), "state": link.get("state"), "reportedAt": at,
                         "ok": link.get("state") == "connected" and at is not None and now - at < LINK_FRESH_S})
    on = [row for row in rows if not row.get("disabled")]
    return {"enabled": len(on), "disabled": len(rows) - len(on),
            "enabledModels": [{"provider": row.get("provider"), "model": row.get("namespaced") or row.get("id")}
                              for row in on],
            "machines": machines}


def _table(header: tuple[str, ...], rows: list[tuple]) -> list[str]:
    cells = [header, *[tuple("" if value is None else str(value) for value in row) for row in rows]]
    widths = [max(len(row[i]) for row in cells) for i in range(len(header))]
    return ["  ".join(value.ljust(width) for value, width in zip(row, widths)).rstrip() for row in cells]


def _models(data: list[dict]) -> list[str]:
    return _table(("PROVIDER", "MODEL", "CONTEXT", "STATE"),
                  [(m.get("provider"), m.get("id"), m.get("contextWindow"),
                    "disabled" if m.get("disabled") else "enabled") for m in data])


def _quota(account: dict) -> str:
    quota = account.get("quota")
    if not isinstance(quota, dict):
        return ""
    return " ".join(f"{key}={value}" for key, value in quota.items() if isinstance(value, (int, float, str)))


def _accounts(data: dict) -> list[str]:
    rows = [(a.get("provider"), a.get("id"), a.get("label"), "yes" if a.get("active") else "",
             a.get("health"), _quota(a)) for a in data.get("accounts", [])]
    return _table(("PROVIDER", "ID", "LABEL", "ACTIVE", "HEALTH", "QUOTA"), rows) + list(data.get("notes", []))


def _status(data: dict) -> list[str]:
    proxy, listen = data.get("proxy", {}), data.get("listen", {})
    return _table(("RUNNING", "PID", "PORT", "HEALTH"),
                  [("yes" if proxy.get("running") else "no", proxy.get("pid"), listen.get("port"),
                    proxy.get("health", {}).get("message"))])


def _usage(data: dict) -> list[str]:
    summary = data.get("summary", {})
    return _table(("RANGE", "REQUESTS", "TOKENS", "COST_USD"),
                  [(data.get("range"), summary.get("requests"), summary.get("totalTokens"),
                    summary.get("estimatedCostUsd"))])


RENDER = {"list": _models, "accounts": _accounts, "quota": _accounts, "status": _status, "usage": _usage}


def render(view: str, data: Any) -> list[str]:
    return RENDER[view](data)
