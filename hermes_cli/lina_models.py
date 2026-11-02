"""``lina models``: OCX's own ``--json`` output, read from LINA's pinned OCX (lina#32).

Each view is one ``ocx … --json`` call of the pinned CLI (2.95.0): ``models live``, ``account list``,
``account list --quota`` (may probe upstream quotas), ``status`` and ``usage``. The CLI finds its proxy through the
data dir and falls back to port 10100, which may be another OCX on this account, so a view is read only while the
proxy answering on LINA's port is LINA's own.
"""

from __future__ import annotations

import json
import subprocess
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


def query(view: str, root: Path | None = None) -> Any:
    root = root or lina_ocx.lina_root()
    version = lina_ocx.current_version(root)
    if version is None:
        raise lina_ocx.OcxError("not installed. Run `lina models install`.")
    port = lina_ocx.configured_port(root)
    if not lina_ocx.own_proxy_running(root, port):
        raise lina_ocx.OcxError(f"LINA's OCX is not running on port {port}. Run `lina services start ocx`.")
    try:
        result = subprocess.run(lina_ocx.ocx_argv(root, version, *VIEWS[view]), env=lina_ocx.ocx_env(root),
                                capture_output=True, text=True, encoding="utf-8", errors="replace",
                                timeout=TIMEOUT_S, check=False)
    except subprocess.TimeoutExpired as exc:
        raise lina_ocx.OcxError(f"ocx {' '.join(VIEWS[view])} took over {TIMEOUT_S}s") from exc
    if result.returncode != 0:
        tail = (result.stderr or result.stdout).strip().splitlines()[-1:] or [f"exit {result.returncode}"]
        raise lina_ocx.OcxError(f"ocx {' '.join(VIEWS[view])}: {tail[0]}")
    try:
        return json.loads(result.stdout)
    except ValueError as exc:
        raise lina_ocx.OcxError(f"ocx {' '.join(VIEWS[view])} printed no JSON") from exc


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
