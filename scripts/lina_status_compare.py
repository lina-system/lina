#!/usr/bin/env python3
"""The old board and the core side by side (lina#662): each machine's CPU, memory and disk, and each quota service's
total, for the status tab's QA.

Run on the hub, read only:

    python scripts/lina_status_compare.py
    python scripts/lina_status_compare.py --devices devices.json --quota quota.json

The old side is the old board's ``/data`` reply (``--old``, a URL or a saved file; its ``machines`` and ``quota``
HTML). The core side is ``/api/lina/devices`` and ``/api/lina/quota``: saved replies (``--devices``, ``--quota``), or
else the API's own reads run in this process under this environment's LINA root (they ask the nodes and magpie as
the API does, and the quota read logs itself to ``board/quota.jsonl`` as the API's does).

A value matches within one point (the old board shows whole percents). Exit 1 when a value differs or a machine the
old board shows is missing from the core, else 0.
"""

from __future__ import annotations

import argparse
import html
import json
import re
import sys
import urllib.request
from pathlib import Path

OLD_URL = "http://127.0.0.1:8790/data"
TOLERANCE = 1.0
_MACHINE = re.compile(r'<details class="task machine" data-key="m-([^"]*)">(.*?)</summary>', re.DOTALL)
_GAUGE = re.compile(r'<span class="gauge (cpu|mem|disk)">.*?<b>([^<]*)</b>', re.DOTALL)
_SERVICE = re.compile(r'<div class="qhead"><b>([^<]*)</b><span>([^<]*)</span>')
_PERCENT = re.compile(r"\s*(-?\d+(?:\.\d+)?)\s*%")


def _percent(text: str) -> float | None:
    hit = _PERCENT.match(html.unescape(text))
    return float(hit[1]) if hit else None


def old_machines(page: str) -> dict[str, tuple]:
    """``{name: (cpu, memory, disk)}`` of the old board's machine rows (its ``—`` is None)."""
    out = {}
    for name, summary in _MACHINE.findall(page):
        gauges = {kind: _percent(value) for kind, value in _GAUGE.findall(summary)}
        out[html.unescape(name)] = (gauges.get("cpu"), gauges.get("mem"), gauges.get("disk"))
    return out


def old_quota(page: str) -> dict[str, float | None]:
    return {html.unescape(name): _percent(used) for name, used in _SERVICE.findall(page)}


def new_machines(devices: dict) -> dict[str, dict]:
    """``{name: row}`` of every machine the core serves: nodes, machines only Beszel watches, person devices."""
    rows = [row for key in ("machines", "monitored", "person") for row in devices.get(key) or []]
    return {str(row.get("name")): row for row in rows if isinstance(row, dict)}


def _same(old, new) -> bool:
    return (old is None and new is None) or (old is not None and new is not None and abs(old - new) <= TOLERANCE)


def _shown(values) -> str:
    return "/".join("-" if value is None else f"{value:g}" for value in values)


def machine_lines(old: dict[str, tuple], new: dict[str, dict]) -> tuple[list[str], bool]:
    lines, ok = [f"{'machine':<18}{'role':<11}{'old cpu/mem/disk':<20}{'new cpu/mem/disk':<20}{'why':<11}result"], True
    for name in sorted(set(old) | set(new)):
        row = new.get(name)
        values = None if row is None else tuple(row.get(key) for key in ("cpu", "memory", "disk"))
        if row is None:
            result, ok = "old only", False
        elif name not in old:
            result = "new only"
        elif all(_same(a, b) for a, b in zip(old[name], values)):
            result = "same"
        else:
            result, ok = "differs", False
        lines.append(f"{name:<18}{(row or {}).get('role', '-'):<11}{_shown(old[name]) if name in old else '':<20}"
                     f"{'' if values is None else _shown(values):<20}{(row or {}).get('metricsWhy') or '-':<11}{result}")
    return lines, ok


def quota_lines(old: dict[str, float | None], quota: dict) -> tuple[list[str], bool]:
    new = {str(row.get("service")): row.get("percent") for row in quota.get("quota") or [] if isinstance(row, dict)}
    lines, ok = [f"{'service':<14}{'old %':<9}{'new %':<9}result"], True
    for name in sorted(set(old) | set(new)):
        if name not in new:
            result, ok = "old only", False
        elif name not in old:
            result = "new only"
        elif _same(old[name], new[name]):
            result = "same"
        else:
            result, ok = "differs", False
        lines.append(f"{name:<14}{_shown([old.get(name)]) if name in old else '':<9}"
                     f"{_shown([new.get(name)]) if name in new else '':<9}{result}")
    return lines, ok


def _read(where: str) -> dict:
    if where.startswith(("http://", "https://")):
        with urllib.request.urlopen(where, timeout=30) as reply:
            return json.loads(reply.read().decode("utf-8"))
    return json.loads(Path(where).read_text(encoding="utf-8"))


def _core(saved: str | None, name: str) -> dict:
    if saved:
        return _read(saved)
    sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
    from hermes_cli import lina_board
    return getattr(lina_board, name)()


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__.split("\n\n")[0])
    parser.add_argument("--old", default=OLD_URL, help=f"the old board's /data reply: a URL or a file ({OLD_URL})")
    parser.add_argument("--devices", help="a saved /api/lina/devices reply (else read in this process)")
    parser.add_argument("--quota", help="a saved /api/lina/quota reply (else read in this process)")
    args = parser.parse_args(argv)
    old = _read(args.old)
    machines, machines_ok = machine_lines(old_machines(str(old.get("machines") or "")),
                                          new_machines(_core(args.devices, "devices")))
    services, quota_ok = quota_lines(old_quota(str(old.get("quota") or "")), _core(args.quota, "quota"))
    print("\n".join([*machines, "", *services]))
    return 0 if machines_ok and quota_ok else 1


if __name__ == "__main__":
    sys.exit(main())
