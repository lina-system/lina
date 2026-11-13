"""Nodes whose magpie differs from the hub's (lina#489): the two lines of the old drift check that still mean
something after the old proxy left (lina#590), the model list and the proxy's version, read from magpie.

- The node side (``facts``): what ``hello`` adds to ``capabilities.proxy``. ``names``: the model names its own magpie
  lists (``GET /v1/models``, ids only, in magpie's catalog form ``<provider>/<id>`` without the node's
  ``<remote_id>/``); None when it does not answer. ``version``: what the node's ``magpie --version`` prints; None
  when there is no ``magpie`` or it prints no version. magpie's gateway answers no version (no ``/version`` route,
  2026-11-13), so the binary is asked; it is the installed magpie, which a service not yet restarted may not run.
- The hub side (``compare``): the daily runner sync reads the hub's own magpie the same way and compares each node
  with it. A node that does not answer, a node whose magpie gives no list or no version, and a hub that gives none
  are unknown, never a difference.

Read-only: no key is in any field, nothing is configured or written on a node or in magpie.
"""

from __future__ import annotations

import os
import re
import subprocess
from pathlib import Path

from hermes_cli import lina_magpie
from hermes_cli.worker import proxy as proxies

_VERSION = re.compile(r"^magpie\s+v?([0-9][0-9A-Za-z.+\-]{0,63})$")
_SHOWN = 10  # names shown on a line; the record keeps them all
_versions: dict[tuple[str, int], str | None] = {}


def version() -> str | None:
    """``0.1.3903`` from ``magpie --version``; asked once per installed binary (path and mtime)."""
    from hermes_platform.resolver import locate_command
    from hermes_platform.resolver.known_dirs import user_local_bin
    found = next(iter(locate_command("magpie", known_dirs=user_local_bin()).command), None)
    if found is None:
        return None
    try:
        key = (found, os.stat(found).st_mtime_ns)
    except OSError:
        return None
    if key not in _versions:
        try:
            done = subprocess.run([found, "--version"], capture_output=True, text=True, encoding="utf-8",
                                  errors="replace", stdin=subprocess.DEVNULL, timeout=5, check=False)
        except (OSError, subprocess.TimeoutExpired):
            return None
        match = _VERSION.match(done.stdout.strip()) if done.returncode == 0 else None
        _versions[key] = match.group(1) if match else None
    return _versions[key]


def names(proxy: proxies.Proxy) -> list[str] | None:
    """The sorted model names *proxy*'s magpie lists, None when it does not answer."""
    try:
        data = lina_magpie.get(proxy, "/v1/models")
    except lina_magpie.MagpieError:
        return None
    return sorted({row["id"] for row in lina_magpie.model_rows(data, proxy.remote_id)})


def facts(proxy: proxies.Proxy, *, answered: bool) -> dict:
    """What ``hello``'s ``capabilities.proxy`` adds. The list is asked only of a magpie whose check *answered*, and
    the version is given only with a list: the binary's version says nothing about a proxy that is down, and a down
    proxy is already a ``proxy:`` issue, so it is never also a version difference."""
    listed = names(proxy) if answered else None
    return {"version": version() if listed is not None else None, "names": listed}


def hub_facts(root: Path) -> dict:
    from hermes_cli.worker import settings
    return facts(proxies.of(settings.section(root)), answered=True)


def _shown(items: list[str]) -> str:
    more = f" and {len(items) - _SHOWN} more" if len(items) > _SHOWN else ""
    return ", ".join(items[:_SHOWN]) + more


def node_result(hub: dict, reachable: bool, proxy) -> dict:
    """One node against the hub: ``models`` and ``version`` each ``same``, ``differs`` or ``unknown`` (with ``why``)."""
    if not reachable:
        return {"models": "unknown", "version": "unknown", "why": "node did not answer"}
    proxy = proxy if isinstance(proxy, dict) else {}
    result: dict = {}
    listed, wanted = proxy.get("names"), hub.get("names")
    if not isinstance(listed, list) or not isinstance(wanted, list):
        result["models"] = "unknown"
        result["models_why"] = "the hub's magpie gave no list" if not isinstance(wanted, list) \
            else "the node's magpie gave no list"
    else:
        missing, extra = sorted(set(wanted) - set(listed)), sorted(set(listed) - set(wanted))
        result["models"] = "differs" if missing or extra else "same"
        result.update({"missing": missing, "extra": extra} if missing or extra else {})
    node_version, hub_version = proxy.get("version"), hub.get("version")
    if not node_version or not hub_version:
        result["version"] = "unknown"
        result["version_why"] = "the hub's magpie gave no version" if not hub_version \
            else "the node's magpie gave no version"
    else:
        result["version"] = "same" if node_version == hub_version else "differs"
    result.update({"node_version": node_version or None, "hub_version": hub_version or None})
    return result


def compare(root: Path, nodes: list, hub: dict | None = None) -> dict:
    """The daily run's ``magpie`` part of its record: the hub's version and model count, and each node's result."""
    hub = hub_facts(root) if hub is None else hub
    listed = hub.get("names")
    return {"hub": {"version": hub.get("version"), "models": len(listed) if isinstance(listed, list) else None},
            "nodes": {node.name: node_result(hub, node.reachable, node.proxy) for node in nodes}}


def lines(record: dict) -> tuple[list[str], list[str]]:
    """(issues, notes) of a run's record: a node whose list or version differs is an issue, an unknown one a note."""
    issues, notes = [], []
    for node, found in sorted(((record.get("magpie") or {}).get("nodes") or {}).items()):
        if found.get("why"):
            notes.append(f"- magpie: {node} unknown ({found['why']})")
            continue
        if found.get("models") == "differs":
            parts = [f"missing {_shown(found['missing'])}" if found.get("missing") else "",
                     f"extra {_shown(found['extra'])}" if found.get("extra") else ""]
            issues.append(f"- magpie: {node} model list differs from the hub's ({'; '.join(p for p in parts if p)})")
        elif found.get("models") == "unknown":
            notes.append(f"- magpie: {node} model list unknown ({found.get('models_why')})")
        if found.get("version") == "differs":
            issues.append(f"- magpie: {node} version {found['node_version']}, hub {found['hub_version']}")
        elif found.get("version") == "unknown":
            notes.append(f"- magpie: {node} version unknown ({found.get('version_why')})")
    return issues, notes
