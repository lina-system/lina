"""Shared check for the work settings tests (lina#256): no runner entry the suite leaves behind lacks its ``type``,
neither in a revision nor in a ``config.yaml`` (``lina worker`` would build such an entry as a runner of type None)."""

from __future__ import annotations

from pathlib import Path

from ruamel.yaml import YAMLError

import hermes_yaml


def untyped_runners(base: Path) -> list[str]:
    found = []
    files = [*base.rglob("work-settings/revisions/r*.yaml"), *base.rglob("config.yaml")]
    for path in files:
        try:
            doc = hermes_yaml.safe_load(path.read_text(encoding="utf-8")) or {}
        except YAMLError:  # a test that writes broken YAML on purpose: nothing to scan there
            continue
        runners = ((doc.get("worker") or {}) if isinstance(doc, dict) else {}).get("runners") or {}
        found += [f"{path.relative_to(base)}: worker.runners.{name}" for name, entry in runners.items()
                  if not (isinstance(entry, dict) and entry.get("type"))]
    return found
