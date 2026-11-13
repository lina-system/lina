"""``lina models``, the client API's model, usage and quota screens and the board's quota view: read from the magpie
gateway ``worker.proxy`` of the LINA root names (``hermes_cli/worker/proxy.py``), read-only, through ``lina_magpie``.
An unreachable or erroring magpie raises ``lina_magpie.MagpieError``, which the screens show as unknown.
"""

from __future__ import annotations

from pathlib import Path
from typing import Any

from hermes_cli import lina_magpie
from hermes_cli.worker import proxy as proxies

VIEWS = ("list", "accounts", "quota", "status", "usage")
RANGES = lina_magpie.PERIODS


def node_proxy(root: Path | None = None) -> proxies.Proxy:
    from hermes_cli import lina_deps
    from hermes_cli.worker import settings
    return proxies.of(settings.section(root or lina_deps.lina_root()))


def query(view: str, root: Path | None = None) -> Any:
    """One ``lina models`` view, in the shape the renderers read."""
    return lina_magpie.view(node_proxy(root), view)


def usage(range_: str, root: Path | None = None) -> dict:
    """``GET /api/lina/usage``: one range's usage by model, provider, account and caller key (``byMachine``)."""
    return lina_magpie.usage(node_proxy(root), range_)


def models_state(root: Path | None = None) -> dict:
    """``GET /api/lina/models/state``: the models magpie lists."""
    return lina_magpie.models_state(node_proxy(root))


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
