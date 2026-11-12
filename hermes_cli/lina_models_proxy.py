"""Which model proxy the usage, quota and model screens read (lina#588): ``worker.proxy`` of the LINA root
(``hermes_cli/worker/proxy.py``). Its ``read`` list (default: ``kind`` alone) names the proxies read; absent, that is
``ocx`` and every screen reads OCX exactly as before (``lina_models``). ``magpie`` is read through
``lina_magpie``. With both (the hub on OCX while nodes move to magpie) each screen shows both, labelled by kind.

Shapes when not OCX alone: one kind answers OCX's shape with ``proxy: <kind>``; several answer ``proxies``, one
``{kind, data}`` each, where a proxy that could not be read has ``data: null`` and ``unknown: <why>`` (never zeros).
When no proxy answers, the first one's error is raised, as an OCX read error is today.
"""

from __future__ import annotations

import json
from pathlib import Path
from typing import Any, Callable

from hermes_cli.lina_ocx import OcxError
from hermes_cli.worker import proxy as proxies


def node_proxy(root: Path) -> proxies.Proxy:
    from hermes_cli.worker import settings
    return proxies.of(settings.section(root))


def _root(root: Path | None) -> Path:
    from hermes_cli.lina_ocx import lina_root
    return root or lina_root()


def side_by_side(proxy: proxies.Proxy, read: Callable[[str], Any]) -> list[dict]:
    """``read(kind)`` for each proxy read, in order; raises the first error when none answered."""
    parts, first = [], None
    for kind in proxy.reads():
        try:
            parts.append({"kind": kind, "data": read(kind)})
        except OcxError as exc:
            first = first or exc
            parts.append({"kind": kind, "data": None, "unknown": str(exc)})
    if first is not None and all(part["data"] is None for part in parts):
        raise first
    return parts


def _view_reader(name: str, root: Path, proxy: proxies.Proxy) -> Callable[[str], Any]:
    from hermes_cli import lina_magpie, lina_models
    return lambda kind: lina_models.query(name, root) if kind == proxies.OCX else lina_magpie.view(proxy, name)


def view_parts(name: str, root: Path | None = None) -> list[dict] | None:
    """One ``lina models`` view per proxy read; None when OCX alone is read (``lina_models.query`` as before)."""
    root = _root(root)
    proxy = node_proxy(root)
    if proxy.reads() == (proxies.OCX,):
        return None
    return side_by_side(proxy, _view_reader(name, root, proxy))


def cli_lines(name: str, as_json: bool, parts: list[dict]) -> list[str]:
    """What ``lina models <view>`` prints for *parts*: one proxy as OCX's table, several each under ``[kind]``."""
    from hermes_cli.lina_models import render
    if as_json:
        return [json.dumps(parts[0]["data"] if len(parts) == 1 else parts, indent=2)]
    if len(parts) == 1:
        return render(name, parts[0]["data"])
    lines: list[str] = []
    for part in parts:
        lines.append(f"[{part['kind']}]")
        lines += render(name, part["data"]) if part["data"] is not None else [f"unknown: {part['unknown']}"]
    return lines


def api_body(name: str, root: Path | None = None) -> dict:
    """``GET /api/lina/models*``'s body: ``{view, data}`` from OCX alone as before; else labelled by kind."""
    from hermes_cli.lina_models import query
    parts = view_parts(name, root)
    if parts is None:
        return {"view": name, "data": query(name)}
    if len(parts) == 1:
        return {"view": name, "proxy": parts[0]["kind"], "data": parts[0]["data"]}
    return {"view": name, "proxies": parts}


def _labelled(parts: list[dict]) -> dict:
    first = next(part for part in parts if part["data"] is not None)
    out = {**first["data"], "proxy": first["kind"]}
    if len(parts) > 1:
        out["proxies"] = parts
    return out


def usage(range_: str, root: Path | None = None) -> dict:
    """``GET /api/lina/usage``: OCX's as before, or magpie's in the same shape (``byMachine`` by caller key name);
    with both, the first that answered at the top and each under ``proxies``."""
    from hermes_cli import lina_magpie, lina_models
    root = _root(root)
    proxy = node_proxy(root)
    if proxy.reads() == (proxies.OCX,):
        return lina_models.usage(range_, root)
    return _labelled(side_by_side(proxy, lambda kind: lina_models.usage(range_, root) if kind == proxies.OCX
                                  else lina_magpie.usage(proxy, range_)))


def models_state(root: Path | None = None) -> dict:
    """``GET /api/lina/models/state`` from the proxies read, as :func:`usage` combines them."""
    from hermes_cli import lina_magpie, lina_models
    root = _root(root)
    proxy = node_proxy(root)
    if proxy.reads() == (proxies.OCX,):
        return lina_models.models_state(root)
    return _labelled(side_by_side(proxy, lambda kind: lina_models.models_state(root) if kind == proxies.OCX
                                  else lina_magpie.models_state(proxy)))
